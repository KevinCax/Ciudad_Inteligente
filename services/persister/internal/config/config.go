// Package config carga la configuracion del persister desde el entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config es la configuracion completa del persister.
type Config struct {
	// HTTP (solo salud y metricas; el persister no expone API de negocio)
	Port            string
	ShutdownTimeout time.Duration

	// Kafka
	KafkaBrokers     []string
	TopicEvents      string
	TopicAlerts      string
	TopicDLQ         string
	ConsumerGroup    string
	ConsumeFromStart bool

	// PostgreSQL
	DatabaseURL string
	MaxConns    int32

	// Lote
	TamanoLote int
	EsperaLote time.Duration

	// Privacidad
	PlateHMACKey string

	// Observabilidad
	LogLevel  string
	LogFormat string
	Env       string
}

// Load lee la configuracion del entorno y la valida.
func Load() (*Config, error) {
	c := &Config{
		Port:            env("SERVER_PORT", "8002"),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 30*time.Second),

		KafkaBrokers:     envList("KAFKA_BROKERS", nil),
		TopicEvents:      env("KAFKA_TOPIC_EVENTS", "events.standardized"),
		TopicAlerts:      env("KAFKA_TOPIC_ALERTS", "correlated.alerts"),
		TopicDLQ:         env("KAFKA_TOPIC_DLQ", "events.dlq"),
		ConsumerGroup:    env("KAFKA_CONSUMER_GROUP", "persister-go"),
		ConsumeFromStart: envBool("KAFKA_CONSUME_FROM_START", true),

		DatabaseURL: env("DATABASE_URL", ""),
		MaxConns:    int32(envInt("DB_MAX_CONNS", 10)),

		TamanoLote: envInt("BATCH_SIZE", 100),
		EsperaLote: envDuration("BATCH_TIMEOUT", 2*time.Second),

		PlateHMACKey: env("PLATE_HMAC_KEY", ""),

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

	if c.TamanoLote < 1 {
		return nil, fmt.Errorf("BATCH_SIZE debe ser al menos 1, se recibio %d", c.TamanoLote)
	}

	return c, nil
}

// PrivacyEnabled indica si las placas se tokenizan antes de persistirlas.
func (c *Config) PrivacyEnabled() bool { return c.PlateHMACKey != "" }

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
