// Package correlate orquesta el ciclo del correlator: consumir, correlacionar,
// persistir y publicar.
package correlate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"ciudadinteligente/correlator/internal/broker"
	"ciudadinteligente/correlator/internal/canonical"
	"ciudadinteligente/correlator/internal/obs"
	"ciudadinteligente/correlator/internal/rules"
	"ciudadinteligente/correlator/internal/window"
)

// Service es el motor del correlator.
//
// No escribe en la base de datos. Las alertas se publican en Kafka y es el
// servicio persister el que las archiva, de modo que el recorrido completo
// evento -> alerta ocurre integramente dentro de la LAN municipal: una caida
// del enlace a internet retrasa el archivo historico, pero no interrumpe la
// deteccion.
type Service struct {
	ventanas   *window.Store
	publicador *broker.Publisher
	validador  *canonical.Validator
	reglas     rules.Config
	metrics    *obs.Metrics
	log        *slog.Logger
}

// New construye el motor.
// La tokenizacion de placas tampoco vive aqui: el correlator no escribe nada
// fuera de la LAN. Quien cruza la frontera hacia la nube es el persister, y es
// alli donde la placa se convierte en seudonimo.
func New(v *window.Store, p *broker.Publisher, val *canonical.Validator,
	cfg rules.Config, m *obs.Metrics, log *slog.Logger) *Service {
	return &Service{
		ventanas: v, publicador: p, validador: val,
		reglas: cfg, metrics: m, log: log,
	}
}

// Procesar atiende un mensaje del topic de eventos.
//
// Devuelve error solo cuando el fallo es de infraestructura y conviene
// reintentar; los eventos que no cumplen el contrato se derivan a la DLQ y se
// dan por atendidos, para no bloquear la particion indefinidamente.
func (s *Service) Procesar(ctx context.Context, m *broker.Mensaje) error {
	ctx = obs.WithTrace(ctx, m.TraceID)
	log := obs.LoggerFrom(ctx, s.log)

	// Defensa en profundidad: la guia pide validar en productor y de nuevo en
	// consumidor. El correlator no confia en que el ingestor haya validado.
	if err := s.validador.ValidateEventBytes(m.Valor); err != nil {
		s.metrics.EventsSkipped.WithLabelValues("contrato").Inc()
		s.aDLQ(ctx, "contrato", detalleDeValidacion(err), m)
		return nil
	}

	ev, err := canonical.ParseEvent(m.Valor)
	if err != nil {
		s.metrics.EventsSkipped.WithLabelValues("mapeo").Inc()
		s.aDLQ(ctx, "mapeo", err.Error(), m)
		return nil
	}

	momento, err := ev.TimestampUTC()
	if err != nil {
		s.metrics.EventsSkipped.WithLabelValues("timestamp").Inc()
		s.aDLQ(ctx, "timestamp", err.Error(), m)
		return nil
	}

	// Idempotencia atomica: SET NX en una sola operacion. Comprobar y despues
	// escribir dejaria una ventana de carrera por la que dos instancias
	// podrian procesar el mismo evento.
	nuevo, err := s.ventanas.MarcarVisto(ctx, ev.EventID)
	if err != nil {
		return err // Redis caido: conviene reintentar
	}
	if !nuevo {
		s.metrics.EventsSkipped.WithLabelValues("duplicado").Inc()
		log.Debug("evento ya procesado", slog.String("event_id", ev.EventID))
		return nil
	}

	zona := ev.Zone()

	if err := s.ventanas.AgregarEvento(ctx, &window.Resumen{
		EventID:   ev.EventID,
		EventType: ev.EventType,
		Timestamp: ev.Timestamp,
		Zone:      zona,
		Payload:   ev.Payload,
	}, momento); err != nil {
		return err
	}

	s.metrics.EventsProcessed.WithLabelValues(ev.EventType, zona).Inc()

	// La ventana se consulta hasta el maximo que necesita cualquier regla;
	// cada regla acota despues su propio rango.
	desde := momento.Add(-s.ventanaMaxima())
	resumenes, err := s.ventanas.EventosEnVentana(ctx, zona, desde, momento)
	if err != nil {
		return err
	}
	s.metrics.WindowSize.WithLabelValues(zona).Set(float64(len(resumenes)))

	resultado := rules.Evaluar(rules.Ventana{
		Zona:    zona,
		Ahora:   momento,
		Eventos: rules.DesdeResumenes(resumenes),
	}, s.reglas)

	for _, det := range resultado.Detecciones {
		if err := s.emitir(ctx, det, ev, momento); err != nil {
			log.Error("no se pudo emitir la alerta",
				slog.String("tipo", det.Tipo),
				slog.String("zona", zona),
				slog.String("error", err.Error()))
		}
	}

	for _, p := range resultado.Parciales {
		if err := s.ventanas.RegistrarParcial(ctx, window.Pendiente{
			Regla:      p.Regla,
			Zona:       zona,
			Disparador: p.Disparador,
			Faltante:   p.Faltante,
			Expira:     p.Expira,
		}); err != nil {
			log.Warn("no se pudo registrar la coincidencia parcial",
				slog.String("regla", p.Regla),
				slog.String("error", err.Error()))
		}
	}

	return nil
}

// emitir persiste, publica y registra una alerta detectada.
func (s *Service) emitir(ctx context.Context, det rules.Deteccion, ev *canonical.Event, momento time.Time) error {
	log := obs.LoggerFrom(ctx, s.log)

	// Supresion: si ya hay una alerta equivalente vigente en la zona, no se
	// emite otra. Sin esto, cada evento nuevo de la ventana vuelve a disparar
	// la misma regla sobre la misma evidencia, y un solo incidente en curso
	// llena la tabla de alertas casi identicas.
	zona := det.Zona

	reservada, err := s.ventanas.ReservarAlerta(ctx, det.Tipo, zona)
	if err != nil {
		return err
	}
	if !reservada {
		s.metrics.AlertsSuppressed.WithLabelValues(det.Tipo).Inc()
		return nil
	}

	alerta := canonical.NewAlert(
		det.Tipo,
		zona,
		det.Score,
		canonical.Window{
			Start: det.Inicio.UTC().Format(time.RFC3339Nano),
			End:   det.Fin.UTC().Format(time.RFC3339Nano),
		},
		det.Evidencia,
		ev.CorrelationID,
		s.reglas.Version,
	)

	// Validar la salida antes de publicarla. Sin un contrato formal para la
	// alerta, cualquier cambio de forma llegaria sin control a los
	// consumidores aguas abajo y se descubriria alli, no aqui.
	if err := s.validador.ValidateAlert(alerta); err != nil {
		s.metrics.EventsSkipped.WithLabelValues("alerta_invalida").Inc()
		log.Error("la alerta generada no cumple su contrato",
			slog.String("tipo", det.Tipo),
			slog.String("error", err.Error()))
		return err
	}

	cuerpo, err := json.Marshal(alerta)
	if err != nil {
		return err
	}

	// Publicar es la unica escritura del camino critico, y va contra un broker
	// que esta en la LAN. El archivado en la base lo hace despues el persister,
	// consumiendo este mismo topic.
	if err := s.publicador.PublicarAlerta(ctx, zona, cuerpo, ev.TraceID); err != nil {
		return err
	}

	if err := s.ventanas.GuardarAlertaActiva(ctx, zona, cuerpo, momento); err != nil {
		log.Warn("la alerta no quedo disponible en /alerts/active",
			slog.String("error", err.Error()))
	}

	// La alerta se genero: las parciales de esta regla y zona dejan de ser
	// candidatas a falso positivo.
	if err := s.ventanas.ResolverParciales(ctx, det.Tipo, zona); err != nil {
		log.Warn("no se pudieron resolver las parciales",
			slog.String("error", err.Error()))
	}

	// Metrica del SLO: tiempo entre el momento del evento disparador y la
	// emision de la alerta. Es lo que permite afirmar y verificar "alerta
	// generada en menos de 2 segundos".
	latencia := time.Since(momento).Seconds()
	s.metrics.CorrelationLatency.WithLabelValues(det.Tipo).Observe(latencia)
	s.metrics.AlertsGenerated.WithLabelValues(det.Tipo, zona).Inc()

	log.Info("alerta generada",
		slog.String("alert_id", alerta.AlertID),
		slog.String("tipo", alerta.Type),
		slog.String("zona", zona),
		slog.Float64("score", alerta.Score),
		slog.Int("evidencia", len(alerta.Evidence)),
		slog.Float64("latencia_s", latencia),
		slog.String("rule_version", alerta.RuleVersion))

	return nil
}

// BarrerFalsosPositivos cuenta las coincidencias parciales que expiraron sin
// completarse. Lo invoca un temporizador desde main.
func (s *Service) BarrerFalsosPositivos(ctx context.Context) {
	expiradas, err := s.ventanas.BarrerExpiradas(ctx, time.Now().UTC())
	if err != nil {
		s.log.Warn("no se pudo barrer las coincidencias parciales",
			slog.String("error", err.Error()))
		return
	}

	for _, p := range expiradas {
		s.metrics.FalsePositives.WithLabelValues(p.Regla, p.Zona).Inc()
		s.log.Info("falso positivo confirmado",
			slog.String("regla", p.Regla),
			slog.String("zona", p.Zona),
			slog.String("disparador", p.Disparador),
			slog.String("faltaba", p.Faltante))
	}
}

// ventanaMaxima es el rango que hay que traer de Redis para que ninguna regla
// se quede corta.
func (s *Service) ventanaMaxima() time.Duration {
	maximo := s.reglas.VentanaRobo
	for _, d := range []time.Duration{
		s.reglas.VentanaAccidente,
		s.reglas.VentanaIncendio,
		s.reglas.VentanaVelocidad,
	} {
		if d > maximo {
			maximo = d
		}
	}
	return maximo
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
		obs.LoggerFrom(ctx, s.log).Error("no se pudo escribir en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}
	s.metrics.DLQPublished.WithLabelValues(motivo).Inc()
}

func detalleDeValidacion(err error) any {
	var verr *canonical.ValidationError
	if errors.As(err, &verr) {
		return verr.Fields
	}
	return err.Error()
}
