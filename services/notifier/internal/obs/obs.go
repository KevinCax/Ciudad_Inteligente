// Package obs concentra logging estructurado y metricas.
//
// Cubre el requisito de observabilidad del enunciado (seccion 4.2): logs
// estructurados con trace_id propagado de extremo a extremo, y metricas que
// permitan medir un SLO concreto. En este servicio la pregunta que deben
// responder las metricas es una sola: de cuantas alertas se aviso, a quien, y
// cuantas se quedaron sin avisar.
package obs

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// claveContexto evita colisiones de claves en el context.Context.
type claveContexto string

const claveTrace claveContexto = "trace_id"

// NewLogger construye el logger estructurado del servicio.
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

// WithTrace adjunta el trace_id al contexto para que viaje por toda la cadena
// de llamadas sin tener que pasarlo como parametro explicito.
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
func LoggerFrom(ctx context.Context, base *slog.Logger) *slog.Logger {
	if t := TraceFrom(ctx); t != "" {
		return base.With(slog.String("trace_id", t))
	}
	return base
}

// Metrics agrupa los instrumentos del notificador.
type Metrics struct {
	AvisosEmitidos         *prometheus.CounterVec
	AvisosDuplicados       prometheus.Counter
	AlertasSinDestinatario *prometheus.CounterVec
	AlertasRechazadas      *prometheus.CounterVec
	DuracionEntrega        *prometheus.HistogramVec
	RetrasoAviso           prometheus.Histogram
	DLQPublicados          *prometheus.CounterVec

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

		AvisosEmitidos: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifier_avisos_emitidos_total",
			Help: "Avisos registrados, por tipo de alerta, organismo y estado.",
		}, []string{"tipo", "organismo", "estado"}),

		// Un duplicado no es un error: significa que la alerta se reproceso,
		// cosa normal tras un rebalanceo de Kafka. Lo relevante es que la
		// restriccion UNIQUE impidio avisar dos veces al mismo organismo.
		AvisosDuplicados: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notifier_avisos_duplicados_total",
			Help: "Avisos que ya estaban registrados al reprocesar la alerta.",
		}),

		// Esta es la metrica mas importante del servicio. Un tipo de alerta
		// sin destinatarios no produce ningun error visible: el sistema sigue
		// funcionando y sencillamente no avisa a nadie. Sin contador, ese
		// fallo solo se descubre cuando ocurre la emergencia.
		AlertasSinDestinatario: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifier_alertas_sin_destinatario_total",
			Help: "Alertas cuyo tipo no tiene ningun organismo asignado en la matriz de despacho.",
		}, []string{"tipo"}),

		AlertasRechazadas: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifier_alertas_rechazadas_total",
			Help: "Alertas que no se pudieron despachar, por motivo.",
		}, []string{"motivo"}),

		DuracionEntrega: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "notifier_duracion_entrega_seconds",
			Help:    "Tiempo que tarda el canal en entregar un aviso.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2, 5},
		}, []string{"canal"}),

		// Distancia entre el momento en que se genero la alerta y el momento
		// en que se despacho el aviso. Es el indicador que importa a un cuerpo
		// de emergencias: una alerta correcta que llega tarde no sirve.
		RetrasoAviso: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "notifier_retraso_aviso_seconds",
			Help:    "Tiempo entre la creacion de la alerta y el despacho del aviso.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		}),

		DLQPublicados: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifier_dlq_publicados_total",
			Help: "Mensajes enviados a la dead-letter queue, por motivo.",
		}, []string{"motivo"}),
	}

	reg.MustRegister(
		m.AvisosEmitidos,
		m.AvisosDuplicados,
		m.AlertasSinDestinatario,
		m.AlertasRechazadas,
		m.DuracionEntrega,
		m.RetrasoAviso,
		m.DLQPublicados,
	)

	return m
}

// Handler expone /metrics en formato Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
