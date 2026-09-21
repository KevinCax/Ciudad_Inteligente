// Package persist archiva eventos canonicos en la base de datos.
//
// Este servicio existe para sacar la escritura del camino critico. Cuando el
// INSERT ocurria dentro de la peticion HTTP del ingestor, cada evento pagaba
// una ida y vuelta contra una base que esta en la nube, y una caida del enlace
// detenia la ingesta por completo. Ahora el ingestor solo valida y publica en
// Kafka, que vive en la LAN, y el archivado ocurre aparte y a su propio ritmo.
//
// El persister se situa justo sobre la frontera LAN -> nube, asi que es el
// lugar natural para tokenizar la placa vehicular antes de que el dato salga
// del control municipal.
package persist

import (
	"context"
	"log/slog"
	"time"

	"ciudadinteligente/persister/internal/broker"
	"ciudadinteligente/persister/internal/canonical"
	"ciudadinteligente/persister/internal/obs"
	"ciudadinteligente/persister/internal/privacy"
	"ciudadinteligente/persister/internal/store"
)

// Service archiva lotes de eventos.
type Service struct {
	almacen     *store.Store
	publicador  *broker.Publisher
	validador   *canonical.Validator
	tokenizador *privacy.Tokenizador
	metrics     *obs.Metrics
	log         *slog.Logger
}

// New construye el servicio.
func New(a *store.Store, p *broker.Publisher, v *canonical.Validator,
	tok *privacy.Tokenizador, m *obs.Metrics, log *slog.Logger) *Service {
	return &Service{almacen: a, publicador: p, validador: v,
		tokenizador: tok, metrics: m, log: log}
}

// ArchivarLote valida, tokeniza y escribe un lote de mensajes.
//
// Devuelve error solo cuando el fallo es de infraestructura y el lote debe
// reintentarse sin confirmar offsets. Los eventos que no cumplen el contrato
// se apartan a la cola de mensajes muertos y no bloquean al resto.
func (s *Service) ArchivarLote(ctx context.Context, mensajes []*broker.Mensaje) error {
	if len(mensajes) == 0 {
		return nil
	}

	eventos := make([]*canonical.Event, 0, len(mensajes))
	origen := make(map[string]*broker.Mensaje, len(mensajes))

	for _, m := range mensajes {
		// Defensa en profundidad: lo que se escriba aqui es el archivo
		// historico, asi que no se confia en la validacion del productor.
		if err := s.validador.ValidateEventBytes(m.Valor); err != nil {
			s.metrics.EventosRechazados.WithLabelValues("contrato").Inc()
			s.aDLQ(ctx, "contrato", detalleDeValidacion(err), m)
			continue
		}

		ev, err := canonical.ParseEvent(m.Valor)
		if err != nil {
			s.metrics.EventosRechazados.WithLabelValues("mapeo").Inc()
			s.aDLQ(ctx, "mapeo", err.Error(), m)
			continue
		}

		// La placa se convierte en seudonimo ANTES de cruzar hacia la nube.
		// El mensaje de Kafka, que se queda en la LAN, conserva el valor en
		// claro para que la correlacion pueda seguir usandolo.
		ev.Payload = s.tokenizador.TokenizarPayload(ev.Payload)

		eventos = append(eventos, ev)
		origen[ev.EventID] = m
	}

	if len(eventos) == 0 {
		return nil
	}

	inicio := time.Now()
	res, err := s.almacen.InsertarLote(ctx, eventos)
	duracion := time.Since(inicio)

	if err != nil {
		// Fallo de infraestructura: no se confirman offsets y el lote entero
		// se reintenta cuando la base vuelva.
		s.log.Error("fallo al escribir el lote, se reintentara",
			slog.Int("eventos", len(eventos)),
			slog.String("error", err.Error()))
		return err
	}

	s.metrics.LotesEscritos.Inc()
	s.metrics.TamanoLote.Observe(float64(len(eventos)))
	s.metrics.DuracionEscritura.Observe(duracion.Seconds())
	s.metrics.EventosDuplicados.Add(float64(res.Duplicados))

	for _, ev := range eventos {
		s.metrics.EventosPersistidos.WithLabelValues(ev.EventType).Inc()

		// Retraso de archivo: cuanto tardo el evento en quedar registrado
		// desde que ocurrio. Si crece de forma sostenida, la base no sigue
		// el ritmo de la ingesta.
		if momento, err := ev.TimestampUTC(); err == nil {
			s.metrics.RetrasoArchivo.Observe(time.Since(momento).Seconds())
		}
	}

	// Los eventos con identificadores mal formados no se pueden escribir,
	// pero tampoco deben desaparecer: van a la cola de mensajes muertos.
	for _, r := range res.Rechazados {
		s.metrics.EventosRechazados.WithLabelValues("uuid_invalido").Inc()
		if m, ok := origen[r.Evento.EventID]; ok {
			s.aDLQ(ctx, "uuid_invalido", r.Motivo, m)
		}
	}

	s.log.Info("lote archivado",
		slog.Int("insertados", res.Insertados),
		slog.Int("duplicados", res.Duplicados),
		slog.Int("rechazados", len(res.Rechazados)),
		slog.Float64("duracion_s", duracion.Seconds()))

	return nil
}

// contextoDeLimpieza devuelve un contexto que sobrevive a la cancelacion del
// original.
//
// Las tareas de limpieza —liberar una reserva, escribir en la DLQ— hay que
// ejecutarlas precisamente cuando el contexto de la peticion ya murio: si el
// cliente aborta a mitad de vuelo, heredar su cancelacion hace que la limpieza
// falle tambien y deje el sistema en un estado peor que el error original.
//
// Se observo en la prueba de carga: al cancelarse una peticion en curso, la
// publicacion fallaba, la liberacion de la reserva fallaba por el mismo motivo
// y el event_id quedaba retenido en Redis durante 24 horas. El reintento del
// cliente recibia entonces un 409 por un evento que nunca llego a publicarse.
//
// WithoutCancel conserva los valores del contexto (el trace_id, entre ellos)
// pero descarta la cancelacion. El plazo acotado evita que la limpieza se
// quede colgada indefinidamente.
func contextoDeLimpieza(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Service) aDLQ(ctx context.Context, motivo string, detalle any, m *broker.Mensaje) {
	limpieza, cancel := contextoDeLimpieza(ctx)
	defer cancel()

	if err := s.publicador.PublicarDLQ(limpieza, motivo, detalle, m); err != nil {
		s.log.Error("no se pudo escribir en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}
	s.metrics.DLQPublicados.WithLabelValues(motivo).Inc()
}

func detalleDeValidacion(err error) any {
	var verr *canonical.ValidationError
	if asValidation(err, &verr) {
		return verr.Fields
	}
	return err.Error()
}

// ArchivarLoteAlertas valida y escribe un lote de alertas correlacionadas.
//
// Las alertas llegan por el topic correlated.alerts, no por HTTP: el
// correlator las publica y sigue su trabajo sin esperar a la base. Gracias a
// eso, el recorrido completo evento -> alerta ocurre integramente dentro de la
// LAN municipal y una caida del enlace a internet no interrumpe la deteccion.
func (s *Service) ArchivarLoteAlertas(ctx context.Context, mensajes []*broker.Mensaje) error {
	if len(mensajes) == 0 {
		return nil
	}

	alertas := make([]*canonical.Alert, 0, len(mensajes))
	origen := make(map[string]*broker.Mensaje, len(mensajes))

	for _, m := range mensajes {
		if err := s.validador.ValidateAlertBytes(m.Valor); err != nil {
			s.metrics.EventosRechazados.WithLabelValues("alerta_contrato").Inc()
			s.aDLQ(ctx, "alerta_contrato", detalleDeValidacion(err), m)
			continue
		}

		a, err := canonical.ParseAlert(m.Valor)
		if err != nil {
			s.metrics.EventosRechazados.WithLabelValues("alerta_mapeo").Inc()
			s.aDLQ(ctx, "alerta_mapeo", err.Error(), m)
			continue
		}

		alertas = append(alertas, a)
		origen[a.AlertID] = m
	}

	if len(alertas) == 0 {
		return nil
	}

	inicio := time.Now()
	res, err := s.almacen.InsertarLoteAlertas(ctx, alertas)
	duracion := time.Since(inicio)

	if err != nil {
		s.log.Error("fallo al escribir el lote de alertas, se reintentara",
			slog.Int("alertas", len(alertas)),
			slog.String("error", err.Error()))
		return err
	}

	s.metrics.LotesEscritos.Inc()
	s.metrics.DuracionEscritura.Observe(duracion.Seconds())
	s.metrics.AlertasDuplicadas.Add(float64(res.Duplicadas))

	for _, a := range alertas {
		s.metrics.AlertasPersistidas.WithLabelValues(a.Type).Inc()
	}

	for _, r := range res.Rechazadas {
		s.metrics.EventosRechazados.WithLabelValues("alerta_uuid_invalido").Inc()
		if m, ok := origen[r.Alerta.AlertID]; ok {
			s.aDLQ(ctx, "alerta_uuid_invalido", r.Motivo, m)
		}
	}

	s.log.Info("lote de alertas archivado",
		slog.Int("insertadas", res.Insertadas),
		slog.Int("duplicadas", res.Duplicadas),
		slog.Int("rechazadas", len(res.Rechazadas)),
		slog.Float64("duracion_s", duracion.Seconds()))

	return nil
}
