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
	"time"

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

// Metrics agrupa los instrumentos del servicio de tareas.
//
// Implementa scheduler.Observador, de modo que cada ejecucion queda
// instrumentada sin que el planificador tenga que conocer Prometheus.
type Metrics struct {
	Ejecuciones *prometheus.CounterVec
	Duracion    *prometheus.HistogramVec
	Afectadas   *prometheus.CounterVec
	UltimoExito *prometheus.GaugeVec

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

		Ejecuciones: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jobs_ejecuciones_total",
			Help: "Ejecuciones de tareas, por tarea y resultado.",
		}, []string{"tarea", "resultado"}),

		Duracion: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jobs_duracion_seconds",
			Help:    "Duracion de cada ejecucion.",
			Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900, 3600},
		}, []string{"tarea"}),

		Afectadas: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jobs_filas_afectadas_total",
			Help: "Filas o documentos modificados o eliminados por cada tarea.",
		}, []string{"tarea"}),

		// Marca de tiempo del ultimo exito. Es la metrica que permite alertar
		// por AUSENCIA de ejecucion: un contador no sirve para eso, porque una
		// tarea que dejo de correr simplemente deja de incrementarlo y la serie
		// queda plana, indistinguible de un periodo sin trabajo.
		UltimoExito: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jobs_ultimo_exito_timestamp_seconds",
			Help: "Momento del ultimo exito de cada tarea, en segundos Unix.",
		}, []string{"tarea"}),
	}

	reg.MustRegister(m.Ejecuciones, m.Duracion, m.Afectadas, m.UltimoExito)
	return m
}

// Handler expone /metrics en formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// --- Implementacion de scheduler.Observador ---

// Inicio registra que una tarea comenzo.
func (m *Metrics) Inicio(tarea string) {}

// Exito registra una ejecucion correcta.
func (m *Metrics) Exito(tarea string, duracion time.Duration, afectadas int64) {
	m.Ejecuciones.WithLabelValues(tarea, "exito").Inc()
	m.Duracion.WithLabelValues(tarea).Observe(duracion.Seconds())
	m.Afectadas.WithLabelValues(tarea).Add(float64(afectadas))
	m.UltimoExito.WithLabelValues(tarea).SetToCurrentTime()
}

// Fallo registra una ejecucion fallida.
func (m *Metrics) Fallo(tarea string, duracion time.Duration, err error) {
	m.Ejecuciones.WithLabelValues(tarea, "fallo").Inc()
	m.Duracion.WithLabelValues(tarea).Observe(duracion.Seconds())
}

// Omitida registra una ejecucion que no llego a correr.
func (m *Metrics) Omitida(tarea, motivo string) {
	m.Ejecuciones.WithLabelValues(tarea, "omitida_"+motivo).Inc()
}
