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

// Metrics agrupa los instrumentos del persister.
type Metrics struct {
	EventosPersistidos *prometheus.CounterVec
	EventosDuplicados  prometheus.Counter
	EventosRechazados  *prometheus.CounterVec
	LotesEscritos      prometheus.Counter
	TamanoLote         prometheus.Histogram
	DuracionEscritura  prometheus.Histogram
	RetrasoArchivo     prometheus.Histogram
	AlertasPersistidas *prometheus.CounterVec
	AlertasDuplicadas  prometheus.Counter
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

		EventosPersistidos: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "persister_eventos_persistidos_total",
			Help: "Eventos escritos en la base, por tipo.",
		}, []string{"event_type"}),

		// Los duplicados no son un error: significan que el evento se
		// reproceso, cosa normal tras un rebalanceo de Kafka o un reinicio.
		EventosDuplicados: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "persister_eventos_duplicados_total",
			Help: "Eventos que ya existian en la base al reprocesarse.",
		}),

		EventosRechazados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "persister_eventos_rechazados_total",
			Help: "Eventos que no se pudieron escribir, por motivo.",
		}, []string{"motivo"}),

		LotesEscritos: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "persister_lotes_escritos_total",
			Help: "Lotes enviados a la base.",
		}),

		TamanoLote: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "persister_tamano_lote",
			Help:    "Eventos por lote. Lotes consistentemente pequenos indican que el disparador es el tiempo de espera y no el volumen.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500},
		}),

		DuracionEscritura: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "persister_duracion_escritura_seconds",
			Help:    "Tiempo de escritura de un lote completo contra la base en la nube.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
		}),

		// Distancia entre el momento del evento y el momento en que quedo
		// archivado. Es el indicador de salud del archivo historico: si crece
		// de forma sostenida, la base no sigue el ritmo de la ingesta.
		RetrasoArchivo: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "persister_retraso_archivo_seconds",
			Help:    "Tiempo entre el timestamp del evento y su escritura en la base.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 300, 900},
		}),

		AlertasPersistidas: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "persister_alertas_persistidas_total",
			Help: "Alertas correlacionadas escritas en la base, por tipo.",
		}, []string{"type"}),

		AlertasDuplicadas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "persister_alertas_duplicadas_total",
			Help: "Alertas que ya existian en la base al reprocesarse.",
		}),

		DLQPublicados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "persister_dlq_publicados_total",
			Help: "Mensajes enviados a la dead-letter queue, por motivo.",
		}, []string{"motivo"}),
	}

	reg.MustRegister(
		m.EventosPersistidos,
		m.EventosDuplicados,
		m.EventosRechazados,
		m.LotesEscritos,
		m.TamanoLote,
		m.DuracionEscritura,
		m.RetrasoArchivo,
		m.AlertasPersistidas,
		m.AlertasDuplicadas,
		m.DLQPublicados,
	)

	return m
}

// Handler expone /metrics en formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
