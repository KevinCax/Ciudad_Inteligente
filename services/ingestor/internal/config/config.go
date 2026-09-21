// Package config carga la configuracion del servicio desde el entorno.
//
// Sin dependencias externas a proposito: son ~100 lineas de stdlib y evita
// tener que justificar una libreria mas en el ADR de dependencias.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config es la configuracion completa del ingestor.
type Config struct {
	// HTTP
	Port            string
	ShutdownTimeout time.Duration

	// Kafka
	KafkaBrokers []string
	TopicEvents  string
	TopicDLQ     string

	// Redis (deduplicacion). Vive en la LAN, igual que Kafka: el ingestor
	// no depende del enlace a internet para ninguna de sus operaciones.
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	DedupeTTL     time.Duration

	// Dominio
	PartitionKeyStrategy string

	// Seguridad
	APIKey string

	// Observabilidad
	LogLevel  string
	LogFormat string
	Env       string
}

// Load lee la configuracion del entorno y la valida.
//
// Falla de inmediato si falta algo obligatorio: es preferible que el
// contenedor no arranque a que arranque a medias y falle en el primer evento.
func Load() (*Config, error) {
	c := &Config{
		Port:                 env("SERVER_PORT", "8000"),
		ShutdownTimeout:      envDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
		KafkaBrokers:         envList("KAFKA_BROKERS", nil),
		TopicEvents:          env("KAFKA_TOPIC_EVENTS", "events.standardized"),
		TopicDLQ:             env("KAFKA_TOPIC_DLQ", "events.dlq"),
		RedisAddr:            env("REDIS_ADDR", ""),
		RedisPassword:        env("REDIS_PASSWORD", ""),
		RedisDB:              envInt("REDIS_DB", 0),
		DedupeTTL:            envDuration("DEDUPE_TTL", 24*time.Hour),
		PartitionKeyStrategy: env("PARTITION_KEY_STRATEGY", "zone"),
		APIKey:               env("API_KEY", ""),
		LogLevel:             env("LOG_LEVEL", "info"),
		LogFormat:            env("LOG_FORMAT", "json"),
		Env:                  env("ENVIRONMENT", "development"),
	}

	var missing []string
	if len(c.KafkaBrokers) == 0 {
		missing = append(missing, "KAFKA_BROKERS")
	}
	if c.RedisAddr == "" {
		missing = append(missing, "REDIS_ADDR")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}

	if c.PartitionKeyStrategy != "zone" && c.PartitionKeyStrategy != "event_type" {
		return nil, fmt.Errorf("PARTITION_KEY_STRATEGY debe ser 'zone' o 'event_type', se recibio %q", c.PartitionKeyStrategy)
	}

	return c, nil
}

// AuthEnabled indica si se exige API key en los endpoints de ingesta.
func (c *Config) AuthEnabled() bool { return c.APIKey != "" }

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return fallback
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
