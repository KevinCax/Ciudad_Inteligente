// Package obs concentra logging estructurado y metricas.
//
// Cubre dos requisitos explicitos del enunciado 2026 (seccion 4.2,
// "Observabilidad y SLOs"): logs estructurados con trace_id propagado de
// extremo a extremo, y metricas que permitan definir y medir un SLO concreto
// en lugar de limitarse a tener dashboards.
package obs

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

// claveContexto evita colisiones de claves en el context.Context.
type claveContexto string

const claveTrace claveContexto = "trace_id"

// NewLogger construye el logger estructurado del servicio.
//
// En produccion emite JSON (consumible por Elasticsearch/Loki); en desarrollo
// texto legible. El campo service va en todas las lineas para poder filtrar
// por microservicio cuando los logs de varios se agregan en un mismo indice.
func NewLogger(service, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}

	return slog.New(h).With(slog.String("service", service))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithTrace adjunta el trace_id al contexto para que viaje por toda la
// cadena de llamadas sin tener que pasarlo como parametro explicito.
func WithTrace(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, claveTrace, traceID)
}

// TraceFrom recupera el trace_id del contexto.
func TraceFrom(ctx context.Context) string {
	if v, ok := ctx.Value(claveTrace).(string); ok {
		return v
	}
	return ""
}

// LoggerFrom devuelve un logger que ya incluye el trace_id del contexto.
// Es el que se debe usar dentro de handlers y servicios: garantiza que toda
// linea emitida durante el procesamiento de un evento sea correlacionable.
func LoggerFrom(ctx context.Context, base *slog.Logger) *slog.Logger {
	if t := TraceFrom(ctx); t != "" {
		return base.With(slog.String("trace_id", t))
	}
	return base
}

// Metrics agrupa los instrumentos del ingestor.
type Metrics struct {
	EventsIngested  *prometheus.CounterVec
	EventsRejected  *prometheus.CounterVec
	EventsEnriched  *prometheus.CounterVec
	IngestDuration  *prometheus.HistogramVec
	PublishDuration prometheus.Histogram
	DLQPublished    *prometheus.CounterVec

	registry *prometheus.Registry
}

// NewMetrics registra los instrumentos en un registry propio.
//
// Registry propio en vez del global: evita que una libreria registre metricas
// por su cuenta y hace que los tests puedan crear instancias aisladas.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,

		EventsIngested: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ingestor_events_ingested_total",
			Help: "Eventos aceptados, persistidos y publicados, por tipo.",
		}, []string{"event_type", "zone"}),

		EventsRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ingestor_events_rejected_total",
			Help: "Eventos rechazados, por motivo (schema, duplicado, json, persistencia, kafka).",
		}, []string{"reason"}),

		EventsEnriched: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ingestor_events_enriched_total",
			Help: "Campos que el ingestor tuvo que completar porque el productor los omitio.",
		}, []string{"field"}),

		IngestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ingestor_request_duration_seconds",
			Help:    "Duracion de las peticiones HTTP de ingesta.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}, []string{"endpoint", "status"}),

		PublishDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ingestor_kafka_publish_duration_seconds",
			Help:    "Latencia de publicacion en Kafka con acks=all.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}),

		DLQPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ingestor_dlq_published_total",
			Help: "Mensajes enviados a la dead-letter queue, por motivo.",
		}, []string{"reason"}),
	}

	reg.MustRegister(
		m.EventsIngested,
		m.EventsRejected,
		m.EventsEnriched,
		m.IngestDuration,
		m.PublishDuration,
		m.DLQPublished,
	)

	return m
}

// Handler expone /metrics en formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
