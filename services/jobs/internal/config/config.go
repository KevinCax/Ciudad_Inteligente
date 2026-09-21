// Package config carga la configuracion del servicio de tareas.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config es la configuracion completa.
type Config struct {
	Port            string
	ShutdownTimeout time.Duration

	// Zona horaria en la que se interpretan los horarios de las tareas.
	// Importa: una purga programada a las 03:00 debe correr de madrugada en
	// Guatemala, no en UTC.
	ZonaHoraria string

	DatabaseURL string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	ElasticURL string

	// Horarios
	HoraAgregados string
	HoraRetencion string

	// Politica de retencion (ADR-007)
	DiasPlaca       int
	DiasCoordenadas int
	DiasBorrado     int
	MesesIndices    int

	APIToken string

	LogLevel  string
	LogFormat string
	Env       string
}

// Load lee la configuracion del entorno y la valida.
func Load() (*Config, error) {
	c := &Config{
		Port:            env("SERVER_PORT", "8004"),
		ShutdownTimeout: envDuration("SHUTDOWN_TIMEOUT", 60*time.Second),
		ZonaHoraria:     env("TZ_TAREAS", "America/Guatemala"),

		DatabaseURL: env("DATABASE_URL", ""),

		RedisAddr:     env("REDIS_ADDR", ""),
		RedisPassword: env("REDIS_PASSWORD", ""),
		RedisDB:       envInt("REDIS_DB", 0),

		ElasticURL: env("ELASTICSEARCH_URL", ""),

		// Los agregados corren ANTES que la retencion a proposito: si la purga
		// borrara los eventos primero, el resumen del dia saldria incompleto.
		HoraAgregados: env("HORA_AGREGADOS", "03:00"),
		HoraRetencion: env("HORA_RETENCION", "03:30"),

		DiasPlaca:       envInt("RETENCION_DIAS_PLACA", 90),
		DiasCoordenadas: envInt("RETENCION_DIAS_COORDENADAS", 365),
		DiasBorrado:     envInt("RETENCION_DIAS_BORRADO", 730),
		MesesIndices:    envInt("RETENCION_MESES_INDICES", 12),

		APIToken: env("API_TOKEN", ""),

		LogLevel:  env("LOG_LEVEL", "info"),
		LogFormat: env("LOG_FORMAT", "json"),
		Env:       env("ENVIRONMENT", "development"),
	}

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.RedisAddr == "" {
		missing = append(missing, "REDIS_ADDR")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("faltan variables de entorno obligatorias: %s", strings.Join(missing, ", "))
	}

	// Una politica incoherente borraria datos antes de degradarlos, o
	// degradaria datos ya borrados. Mejor no arrancar que purgar mal.
	if c.DiasPlaca > c.DiasCoordenadas {
		return nil, fmt.Errorf("RETENCION_DIAS_PLACA (%d) debe ser menor o igual que RETENCION_DIAS_COORDENADAS (%d)",
			c.DiasPlaca, c.DiasCoordenadas)
	}
	if c.DiasCoordenadas > c.DiasBorrado {
		return nil, fmt.Errorf("RETENCION_DIAS_COORDENADAS (%d) debe ser menor o igual que RETENCION_DIAS_BORRADO (%d)",
			c.DiasCoordenadas, c.DiasBorrado)
	}

	return c, nil
}

// AuthEnabled indica si la API de gestion exige token.
func (c *Config) AuthEnabled() bool { return c.APIToken != "" }

// ElasticEnabled indica si la purga de indices esta habilitada.
func (c *Config) ElasticEnabled() bool { return c.ElasticURL != "" }

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
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
