// Package config carga la configuracion del notificador desde el entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Canales de entrega admitidos.
const (
	CanalSimulado = "simulado"
	CanalWebhook  = "webhook"
)

// Config es la configuracion completa del notificador.
type Config struct {
	// HTTP (solo salud y metricas; el notificador no expone API de negocio)
	Port            string
	ShutdownTimeout time.Duration

	// Kafka
	KafkaBrokers     []string
	TopicAlerts      string
	TopicDLQ         string
	ConsumerGroup    string
	ConsumeFromStart bool

	// PostgreSQL
	DatabaseURL string
	MaxConns    int32

	// Entrega
	Canal          string
	WebhookURL     string
	WebhookTimeout time.Duration

	// Matriz de despacho tomada del entorno (claves DISPATCH_*).
	DespachoDesdeEntorno map[string]string

	// Observabilidad
	LogLevel  string
	LogFormat string
	Env       string
}

// Load lee la configuracion del entorno y la valida.
func Load() (*Config, error) {
	c := &Config{
		Port:            env("SERVER_PORT", "8005"),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 30*time.Second),

		KafkaBrokers:  envList("KAFKA_BROKERS", nil),
		TopicAlerts:   env("KAFKA_TOPIC_ALERTS", "correlated.alerts"),
		TopicDLQ:      env("KAFKA_TOPIC_DLQ", "events.dlq"),
		ConsumerGroup: env("KAFKA_CONSUMER_GROUP", "notifier-go"),

		// Por defecto NO se lee desde el principio del topic, al reves que en
		// el persister. Un grupo de consumo nuevo que arrancara desde el
		// origen despacharia avisos de todas las alertas historicas: bomberos
		// a un incendio de la semana pasada.
		ConsumeFromStart: envBool("KAFKA_CONSUME_FROM_START", false),

		DatabaseURL: env("DATABASE_URL", ""),
		MaxConns:    int32(envInt("DB_MAX_CONNS", 5)),

		Canal:          strings.ToLower(env("NOTIFIER_CANAL", CanalSimulado)),
		WebhookURL:     env("NOTIFIER_WEBHOOK_URL", ""),
		WebhookTimeout: envDuration("NOTIFIER_WEBHOOK_TIMEOUT", 5*time.Second),

		DespachoDesdeEntorno: prefijo("DISPATCH_"),

		LogLevel:  env("LOG_LEVEL", "info"),
		LogFormat: env("LOG_FORMAT", "json"),
		Env:       env("ENVIRONMENT", "development"),
	}

	var missing []string
	if len(c.KafkaBrokers) == 0 {
		missing = append(missing, "KAFKA_BROKERS")
	}
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}

	switch c.Canal {
	case CanalSimulado:
	case CanalWebhook:
		// Un canal webhook sin URL arrancaria y fallaria en cada aviso. Vale
		// mas no arrancar: el fallo es evidente ahora y silencioso despues.
		if c.WebhookURL == "" {
			return nil, fmt.Errorf("NOTIFIER_CANAL=webhook exige NOTIFIER_WEBHOOK_URL")
		}
	default:
		return nil, fmt.Errorf("NOTIFIER_CANAL debe ser %q o %q, se recibio %q",
			CanalSimulado, CanalWebhook, c.Canal)
	}

	return c, nil
}

// prefijo recoge las variables de entorno que empiezan por p.
//
// Una variable vacia se ignora, igual que en el resto del proyecto. No es un
// detalle: docker compose escribe DISPATCH_FIRE="" cuando el .env no define
// nada, y si eso contara como "lista de destinatarios vacia", el despliegue
// por defecto dejaria de avisar a los bomberos de un incendio sin que nadie lo
// notara. Para silenciar un tipo a proposito esta el valor "ninguno".
func prefijo(p string) map[string]string {
	out := map[string]string{}
	for _, e := range os.Environ() {
		k, v, ok := strings.Cut(e, "=")
		if ok && strings.HasPrefix(k, p) && strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
