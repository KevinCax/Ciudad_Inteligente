// Package ingest orquesta el pipeline de ingesta: enriquecer, validar,
// deduplicar y publicar.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"ciudadinteligente/ingestor/internal/broker"
	"ciudadinteligente/ingestor/internal/canonical"
	"ciudadinteligente/ingestor/internal/dedupe"
	"ciudadinteligente/ingestor/internal/obs"
)

// Errores del pipeline. El handler HTTP los traduce a codigos de estado.
//
// Se distinguen por tipo a proposito: un duplicado, una caida de Kafka y un
// JSON mal formado exigen reacciones distintas del cliente, y colapsarlos en
// un solo error los vuelve indistinguibles.
var (
	// ErrContrato: el evento no cumple el contrato v1.0 -> 400.
	ErrContrato = errors.New("el evento no cumple el contrato")
	// ErrDuplicado: el event_id ya fue ingerido -> 409.
	ErrDuplicado = errors.New("event_id duplicado")
	// ErrInfraestructura: Redis o el broker no disponibles -> 503.
	ErrInfraestructura = errors.New("dependencia no disponible")
)

// ErrorDetallado acompana al error con los campos concretos que fallaron.
type ErrorDetallado struct {
	Err     error
	Mensaje string
	Campos  []canonical.FieldError
}

func (e *ErrorDetallado) Error() string { return e.Mensaje }
func (e *ErrorDetallado) Unwrap() error { return e.Err }

// Service es el pipeline de ingesta.
type Service struct {
	dedupe    *dedupe.Store
	publisher *broker.Publisher
	validator *canonical.Validator
	enricher  *canonical.Enricher
	metrics   *obs.Metrics
	log       *slog.Logger
}

// New construye el pipeline.
func New(d *dedupe.Store, p *broker.Publisher, v *canonical.Validator,
	e *canonical.Enricher, m *obs.Metrics, log *slog.Logger) *Service {
	return &Service{dedupe: d, publisher: p, validator: v, enricher: e,
		metrics: m, log: log}
}

// Ingest procesa un evento individual.
//
// Todo el recorrido ocurre dentro de la LAN municipal: validacion en memoria,
// deduplicacion en Redis y publicacion en Kafka. El archivado en la base de la
// nube corre aparte, en el servicio persister.
//
// Esa separacion es deliberada. Antes el INSERT ocurria aqui dentro, de modo
// que cada peticion pagaba una ida y vuelta contra una base remota y una caida
// del enlace detenia la ingesta por completo. Ahora una caida del enlace solo
// retrasa el archivo historico: la ciudad sigue detectando incidentes.
//
// El 202 significa que Kafka confirmo la recepcion con acks=all. Responder
// antes de esa confirmacion seria mas rapido y falso: si la publicacion
// fallara, el cliente ya se habria llevado un "aceptado" por un evento que no
// llegaria nunca al correlator.
func (s *Service) Ingest(ctx context.Context, raw []byte) (*canonical.Event, error) {
	traceID := obs.TraceFrom(ctx)
	log := obs.LoggerFrom(ctx, s.log)

	// 1. Parsear. Un JSON ilegible no llega siquiera a tener trace propio.
	doc, err := canonical.ParseDocument(raw)
	if err != nil {
		s.metrics.EventsRejected.WithLabelValues("json_invalido").Inc()
		s.aDLQ(ctx, "json_invalido", err.Error(), traceID, raw)
		return nil, &ErrorDetallado{Err: ErrContrato, Mensaje: err.Error()}
	}

	// 2. Enriquecer ANTES de validar: el contrato exige campos que el
	// productor puede omitir legitimamente (timestamp, trace_id, ...).
	res := s.enricher.Enrich(doc)
	for _, campo := range res.Fields() {
		s.metrics.EventsEnriched.WithLabelValues(campo).Inc()
	}

	// 3. Validar contra el contrato.
	if err := s.validator.Validate(doc); err != nil {
		s.metrics.EventsRejected.WithLabelValues("contrato").Inc()
		var verr *canonical.ValidationError
		var campos []canonical.FieldError
		if errors.As(err, &verr) {
			campos = verr.Fields
		}
		s.aDLQ(ctx, "contrato", campos, traceID, raw)
		return nil, &ErrorDetallado{Err: ErrContrato, Mensaje: err.Error(), Campos: campos}
	}

	ev, err := canonical.DocumentToEvent(doc)
	if err != nil {
		s.metrics.EventsRejected.WithLabelValues("mapeo").Inc()
		return nil, &ErrorDetallado{Err: ErrContrato, Mensaje: err.Error()}
	}

	// 4. Reservar el event_id. Atomico: comprobar y luego escribir dejaria
	// pasar dos peticiones simultaneas con el mismo identificador.
	nuevo, err := s.dedupe.Reservar(ctx, ev.EventID)
	if err != nil {
		s.metrics.EventsRejected.WithLabelValues("redis_no_disponible").Inc()
		log.Error("fallo la deduplicacion", slog.String("error", err.Error()))
		return nil, &ErrorDetallado{Err: ErrInfraestructura, Mensaje: "no se pudo verificar el evento"}
	}
	if !nuevo {
		s.metrics.EventsRejected.WithLabelValues("duplicado").Inc()
		return ev, &ErrorDetallado{
			Err:     ErrDuplicado,
			Mensaje: "el evento " + ev.EventID + " ya fue ingerido",
		}
	}

	// 5. Publicar. Se envia el documento validado, no el struct
	// re-serializado: es exactamente el JSON que paso el contrato.
	valor, err := json.Marshal(doc)
	if err != nil {
		s.liberar(ctx, ev.EventID)
		return nil, fmt.Errorf("no se pudo serializar el evento enriquecido: %w", err)
	}

	cabeceras := map[string]string{
		"trace_id":   ev.TraceID,
		"event_type": ev.EventType,
		"zone":       ev.Geo.Zone,
	}

	inicio := time.Now()
	err = s.publisher.Publish(ctx, ev.PartitionKey, valor, cabeceras)
	s.metrics.PublishDuration.Observe(time.Since(inicio).Seconds())

	if err != nil {
		// Se libera la reserva: sin esto, el reintento del cliente chocaria
		// contra su propia reserva y recibiria un 409 por un evento que en
		// realidad nunca se publico.
		s.liberar(ctx, ev.EventID)
		s.metrics.EventsRejected.WithLabelValues("kafka_no_disponible").Inc()
		log.Error("fallo al publicar en kafka",
			slog.String("event_id", ev.EventID),
			slog.String("error", err.Error()))
		s.aDLQ(ctx, "kafka_no_disponible", err.Error(), traceID, raw)
		return nil, &ErrorDetallado{Err: ErrInfraestructura, Mensaje: "no se pudo publicar el evento"}
	}

	s.metrics.EventsIngested.WithLabelValues(ev.EventType, ev.Geo.Zone).Inc()
	log.Info("evento ingerido",
		slog.String("event_id", ev.EventID),
		slog.String("event_type", ev.EventType),
		slog.String("zone", ev.Geo.Zone),
		slog.String("partition_key", ev.PartitionKey),
		slog.Any("campos_enriquecidos", res.Fields()))

	return ev, nil
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

// liberar deshace la reserva de un event_id sin interrumpir el flujo de error
// que se esta atendiendo.
func (s *Service) liberar(ctx context.Context, eventID string) {
	limpieza, cancel := contextoDeLimpieza(ctx)
	defer cancel()

	if err := s.dedupe.Liberar(limpieza, eventID); err != nil {
		obs.LoggerFrom(ctx, s.log).Warn("no se pudo liberar la reserva del event_id",
			slog.String("event_id", eventID),
			slog.String("error", err.Error()))
	}
}

// aDLQ publica en la dead-letter queue sin interrumpir el flujo principal:
// si la propia DLQ falla, se registra y se sigue, porque el error que el
// cliente debe ver es el original y no el del envio a la DLQ.
func (s *Service) aDLQ(ctx context.Context, motivo string, detalle any, traceID string, original []byte) {
	limpieza, cancel := contextoDeLimpieza(ctx)
	defer cancel()

	if err := s.publisher.PublishDLQ(limpieza, motivo, detalle, traceID, original); err != nil {
		obs.LoggerFrom(ctx, s.log).Error("no se pudo escribir en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}
	s.metrics.DLQPublished.WithLabelValues(motivo).Inc()
}
