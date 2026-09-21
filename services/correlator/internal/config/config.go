// Package config carga la configuracion del correlator desde el entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config es la configuracion completa del correlator.
type Config struct {
	// HTTP (API de gestion)
	Port            string
	ShutdownTimeout time.Duration

	// Kafka
	KafkaBrokers  []string
	TopicEvents   string
	TopicAlerts   string
	TopicDLQ      string
	ConsumerGroup string

	// ConsumeFromStart hace que un grupo nuevo lea desde el offset mas
	// antiguo. Por defecto es false: el sistema Java usaba earliest con un
	// TTL de idempotencia de 10 minutos, de modo que cada reinicio reprocesaba
	// horas de eventos y regeneraba alertas historicas en masa. Se activa solo
	// para la migracion en paralelo, cuando se quiere comparar salidas sobre
	// el mismo historico.
	ConsumeFromStart bool

	// Redis (ventanas de correlacion e idempotencia)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// PostgreSQL
	DatabaseURL string

	// Correlacion
	SeenTTL          time.Duration
	WindowTTL        time.Duration
	AlertSuppressTTL time.Duration
	RuleVersion      string

	// Seguridad
	APIToken string

	// Observabilidad
	LogLevel  string
	LogFormat string
	Env       string
}

// Load lee la configuracion del entorno y la valida.
func Load() (*Config, error) {
	c := &Config{
		Port:            env("SERVER_PORT", "8080"),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),

		KafkaBrokers:     envList("KAFKA_BROKERS", nil),
		TopicEvents:      env("KAFKA_TOPIC_EVENTS", "events.standardized"),
		TopicAlerts:      env("KAFKA_TOPIC_ALERTS", "correlated.alerts"),
		TopicDLQ:         env("KAFKA_TOPIC_DLQ", "events.dlq"),
		ConsumerGroup:    env("KAFKA_CONSUMER_GROUP", "correlator-go"),
		ConsumeFromStart: envBool("KAFKA_CONSUME_FROM_START", false),

		RedisAddr:     env("REDIS_ADDR", ""),
		RedisPassword: env("REDIS_PASSWORD", ""),
		RedisDB:       envInt("REDIS_DB", 0),

		DatabaseURL: env("DATABASE_URL", ""),

		SeenTTL:          envDuration("CORRELATION_SEEN_TTL", 10*time.Minute),
		WindowTTL:        envDuration("CORRELATION_WINDOW_TTL", 10*time.Minute),
		AlertSuppressTTL: envDuration("ALERT_SUPPRESS_TTL", 5*time.Minute),
		RuleVersion:      env("RULE_VERSION", "1.0"),

		APIToken: env("API_TOKEN", ""),

		LogLevel:  env("LOG_LEVEL", "info"),
		LogFormat: env("LOG_FORMAT", "json"),
		Env:       env("ENVIRONMENT", "development"),
	}

	var missing []string
	if len(c.KafkaBrokers) == 0 {
		missing = append(missing, "KAFKA_BROKERS")
	}
	if c.RedisAddr == "" {
		missing = append(missing, "REDIS_ADDR")
	}
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}

	// El group id distingue el consumidor Go del consumidor Java durante la
	// migracion en paralelo: ambos leen el mismo topic sin robarse offsets.
	if c.ConsumerGroup == "" {
		return nil, fmt.Errorf("KAFKA_CONSUMER_GROUP no puede estar vacio")
	}

	return c, nil
}

// AuthEnabled indica si la API de gestion exige token.
func (c *Config) AuthEnabled() bool { return c.APIToken != "" }

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

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return fallback
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
