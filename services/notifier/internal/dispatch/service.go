package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"ciudadinteligente/notifier/internal/broker"
	"ciudadinteligente/notifier/internal/canonical"
	"ciudadinteligente/notifier/internal/obs"
	"ciudadinteligente/notifier/internal/store"
)

// Service despacha avisos a terceros a partir de alertas correlacionadas.
//
// No agrupa en lotes, al contrario que el persister y el indexer. La razon es
// que un aviso es una accion y no un archivo: esperar dos segundos a llenar un
// lote para notificar un incendio seria optimizar el recurso equivocado. El
// volumen lo permite, porque hay ordenes de magnitud menos alertas que eventos.
type Service struct {
	matriz     Matriz
	canal      Canal
	almacen    *store.Store
	validador  *canonical.Validator
	publicador *broker.Publisher
	metrics    *obs.Metrics
	log        *slog.Logger
}

// New construye el servicio de despacho.
func New(matriz Matriz, canal Canal, almacen *store.Store,
	validador *canonical.Validator, publicador *broker.Publisher,
	metrics *obs.Metrics, log *slog.Logger) *Service {

	return &Service{
		matriz: matriz, canal: canal, almacen: almacen,
		validador: validador, publicador: publicador,
		metrics: metrics, log: log,
	}
}

// Procesar despacha los avisos de una alerta.
//
// Devuelve error SOLO ante fallos de infraestructura, para que el llamador no
// confirme el offset y el mensaje se reintente cuando la dependencia vuelva.
// Un fallo de contenido se aparta en la DLQ y devuelve nil: reintentar un
// mensaje que nunca va a ser valido bloquearia la particion para siempre.
func (s *Service) Procesar(ctx context.Context, m *broker.Mensaje) error {
	ctx = obs.WithTrace(ctx, m.TraceID)
	log := obs.LoggerFrom(ctx, s.log)

	if err := s.validador.ValidateAlertBytes(m.Valor); err != nil {
		s.metrics.AlertasRechazadas.WithLabelValues("contrato").Inc()
		s.aDLQ(ctx, "contrato", detalleDeValidacion(err), m)
		return nil
	}

	a, err := canonical.ParseAlert(m.Valor)
	if err != nil {
		s.metrics.AlertasRechazadas.WithLabelValues("mapeo").Inc()
		s.aDLQ(ctx, "mapeo", err.Error(), m)
		return nil
	}

	destinatarios, contemplado := s.matriz.Destinatarios(a.Type)

	// Un tipo que la matriz no contempla no es un mensaje invalido: es un
	// hueco en la configuracion de este servicio. Mandarlo a la DLQ lo
	// escondería entre los errores de contrato, cuando lo que hace falta es
	// que alguien anada la fila que falta.
	if !contemplado {
		s.metrics.AlertasSinDestinatario.WithLabelValues(a.Type).Inc()
		log.Error("tipo de alerta sin destinatarios en la matriz de despacho: no se avisa a nadie",
			slog.String("alert_id", a.AlertID),
			slog.String("tipo", a.Type),
			slog.String("zona", a.Zone))
		return nil
	}

	if len(destinatarios) == 0 {
		log.Info("tipo de alerta configurado sin destinatarios, no se despacha",
			slog.String("alert_id", a.AlertID),
			slog.String("tipo", a.Type))
		return nil
	}

	for _, organismo := range destinatarios {
		if err := s.despachar(ctx, a, organismo, log); err != nil {
			// Fallo de base: no se confirma el offset. La alerta se
			// reprocesara entera, incluidos los organismos ya registrados,
			// pero eso es inofensivo porque RegistrarAviso es idempotente.
			return err
		}
	}

	return nil
}

// despachar entrega el aviso a un organismo y deja constancia del resultado.
func (s *Service) despachar(ctx context.Context, a *canonical.Alert,
	organismo string, log *slog.Logger) error {

	aviso := Aviso{
		AlertID:   a.AlertID,
		Tipo:      a.Type,
		Zona:      a.Zone,
		Score:     a.Score,
		Organismo: organismo,
		CreadoEn:  a.CreatedAt,
	}

	inicio := time.Now()
	errEnvio := s.canal.Enviar(ctx, aviso)
	s.metrics.DuracionEntrega.WithLabelValues(s.canal.Nombre()).Observe(time.Since(inicio).Seconds())

	fila := store.Aviso{
		AlertID:   a.AlertID,
		AlertType: a.Type,
		Zona:      a.Zone,
		Organismo: organismo,
		Canal:     s.canal.Nombre(),
	}

	// El resultado se registra tanto si se entrego como si no. Un aviso
	// fallido que no deja rastro es indistinguible de uno que nunca se
	// intento, y esa diferencia es justo lo que una investigacion posterior
	// necesita saber.
	if errEnvio != nil {
		fila.Estado = store.EstadoFallido
		fila.Detalle = errEnvio.Error()
		log.Error("no se pudo entregar el aviso",
			slog.String("alert_id", a.AlertID),
			slog.String("organismo", organismo),
			slog.String("canal", s.canal.Nombre()),
			slog.String("error", errEnvio.Error()))
	} else {
		ahora := time.Now().UTC()
		fila.Estado = store.EstadoEntregado
		fila.EnviadoEn = &ahora
		s.observarRetraso(a.CreatedAt, ahora)
	}

	nuevo, err := s.almacen.RegistrarAviso(ctx, fila)
	if err != nil {
		var uuidErr *store.ErrUUIDInvalido
		if errors.As(err, &uuidErr) {
			// El alert_id no es un UUID: ningun reintento lo va a arreglar.
			s.metrics.AlertasRechazadas.WithLabelValues("uuid").Inc()
			log.Error("alerta con identificador invalido, no se registra el aviso",
				slog.String("error", err.Error()))
			return nil
		}
		return err
	}

	if !nuevo {
		s.metrics.AvisosDuplicados.Inc()
		log.Debug("el aviso ya estaba registrado",
			slog.String("alert_id", a.AlertID),
			slog.String("organismo", organismo))
		return nil
	}

	s.metrics.AvisosEmitidos.WithLabelValues(a.Type, organismo, fila.Estado).Inc()
	return nil
}

// observarRetraso mide la distancia entre la creacion de la alerta y el aviso.
//
// Si created_at no es interpretable se omite la medicion en lugar de registrar
// un valor inventado: un histograma con datos falsos es peor que uno con menos
// muestras, porque nadie sabe cuales descartar.
func (s *Service) observarRetraso(creadaEn string, ahora time.Time) {
	t, err := time.Parse(time.RFC3339Nano, creadaEn)
	if err != nil {
		return
	}
	if d := ahora.Sub(t); d >= 0 {
		s.metrics.RetrasoAviso.Observe(d.Seconds())
	}
}

// aDLQ aparta el mensaje y registra el motivo.
func (s *Service) aDLQ(ctx context.Context, motivo string, detalle any, m *broker.Mensaje) {
	log := obs.LoggerFrom(ctx, s.log)

	// El contexto de publicacion se desacopla de la cancelacion del original:
	// si el servicio se esta apagando, el ctx ya viene cancelado y el mensaje
	// se perderia justo cuando mas importa conservarlo.
	limpio, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.publicador.PublicarDLQ(limpio, motivo, detalle, m); err != nil {
		log.Error("no se pudo publicar en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}

	s.metrics.DLQPublicados.WithLabelValues(motivo).Inc()
	log.Warn("alerta apartada en la DLQ",
		slog.String("motivo", motivo),
		slog.Int("particion", m.Particion),
		slog.Int64("offset", m.Offset))
}

// detalleDeValidacion extrae la lista de campos cuando el error es de
// contrato, para que la DLQ guarde el detalle estructurado y no una cadena.
func detalleDeValidacion(err error) any {
	var verr *canonical.ValidationError
	if errors.As(err, &verr) {
		return verr.Fields
	}
	return err.Error()
}
