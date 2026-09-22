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

// Metrics agrupa los instrumentos del indexer.
type Metrics struct {
	Indexados          *prometheus.CounterVec
	Rechazados         *prometheus.CounterVec
	LotesIndexados     *prometheus.CounterVec
	TamanoLote         *prometheus.HistogramVec
	DuracionIndexacion *prometheus.HistogramVec
	RetrasoIndice      prometheus.Histogram
	DLQPublicados      *prometheus.CounterVec

	registry *prometheus.Registry
}

// NewMetrics registra los instrumentos en un registry propio.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,

		Indexados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_documentos_indexados_total",
			Help: "Documentos indexados en Elasticsearch, por flujo.",
		}, []string{"flujo"}),

		Rechazados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_documentos_rechazados_total",
			Help: "Documentos que no se pudieron indexar, por flujo y motivo.",
		}, []string{"flujo", "motivo"}),

		LotesIndexados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_lotes_indexados_total",
			Help: "Peticiones bulk enviadas a Elasticsearch, por flujo.",
		}, []string{"flujo"}),

		TamanoLote: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "indexer_tamano_lote",
			Help:    "Documentos por peticion bulk.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
		}, []string{"flujo"}),

		DuracionIndexacion: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "indexer_duracion_indexacion_seconds",
			Help:    "Tiempo de una peticion bulk contra Elasticsearch.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
		}, []string{"flujo"}),

		// Distancia entre el momento del evento y su disponibilidad para
		// busqueda. Los buckets llegan a 3600 s a proposito: con una copia
		// periodica desde la base en lugar de consumo del broker, este valor
		// se acercaria al intervalo de esa copia.
		RetrasoIndice: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "indexer_retraso_indice_seconds",
			Help:    "Tiempo entre el timestamp del evento y su indexacion.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 300, 3600},
		}),

		DLQPublicados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "indexer_dlq_publicados_total",
			Help: "Mensajes enviados a la dead-letter queue, por motivo.",
		}, []string{"motivo"}),
	}

	reg.MustRegister(
		m.Indexados, m.Rechazados, m.LotesIndexados,
		m.TamanoLote, m.DuracionIndexacion, m.RetrasoIndice, m.DLQPublicados,
	)

	return m
}

// Handler expone /metrics en formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
