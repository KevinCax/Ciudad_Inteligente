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
	// antiguo. Por defecto es false: como el TTL de idempotencia es de 10
	// minutos y la retencion del topic de 72 horas, leer desde el principio
	// en cada reinicio reprocesaria horas de eventos ya deduplicados y
	// regeneraria alertas historicas en masa. Se activa solo para reprocesar
	// un historico a proposito.
	ConsumeFromStart bool

	// Redis (ventanas de correlacion e idempotencia)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// PostgreSQL
	DatabaseURL string

	// ReglasDesdeEntorno son los umbrales del motor de correlacion que el
	// operador puede ajustar sin recompilar. Solo contiene los valores que
	// el entorno define; el resto se queda en su valor por defecto.
	//
	// Existe porque el comentario del motor prometia ajustabilidad y nadie
	// la habia cableado: main.go usaba los valores por defecto y ninguna
	// variable de entorno los tocaba.
	ReglasDesdeEntorno map[string]string

	// Correlacion
	SeenTTL   time.Duration
	WindowTTL time.Duration

	// VehicleWindowTTL acota la ventana indexada por vehiculo. Es mayor que
	// WindowTTL porque una huida se extiende mas que cualquier ventana de
	// zona: el coche cambia de zona justamente para salir de ella.
	VehicleWindowTTL time.Duration

	// PlateHMACKey es la MISMA llave que usan persister e indexer. El
	// correlator no la necesita para proteger un dato que escriba —no escribe
	// placas en ningun sitio— sino para que el seudonimo con el que indexa la
	// ventana de vehiculo coincida con el archivado en PostgreSQL. Si
	// difirieran, una alerta de persecucion no se podria cruzar con el
	// historico del vehiculo que la provoco.
	PlateHMACKey     string
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

		ReglasDesdeEntorno: reglasDesdeEntorno(),

		SeenTTL:          envDuration("CORRELATION_SEEN_TTL", 10*time.Minute),
		WindowTTL:        envDuration("CORRELATION_WINDOW_TTL", 10*time.Minute),
		VehicleWindowTTL: envDuration("VEHICLE_WINDOW_TTL", 20*time.Minute),
		PlateHMACKey:     env("PLATE_HMAC_KEY", ""),
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

	// El grupo de consumo no puede quedar vacio: dos despliegues que
	// compartan grupo se reparten las particiones en lugar de leer cada uno
	// el flujo completo, y eso no se manifiesta como error sino como eventos
	// que simplemente no llegan.
	if c.ConsumerGroup == "" {
		return nil, fmt.Errorf("KAFKA_CONSUMER_GROUP no puede estar vacio")
	}

	return c, nil
}

// AuthEnabled indica si la API de gestion exige token.
func (c *Config) AuthEnabled() bool { return c.APIToken != "" }

// reglasDesdeEntorno recoge las variables que ajustan el motor de reglas.
//
// Se devuelven como texto sin interpretar para que el paquete rules —que es
// quien conoce el significado de cada umbral— haga la conversion y valide los
// rangos. Config no deberia saber que una ventana se mide en minutos ni que
// una velocidad va en kilometros por hora.
func reglasDesdeEntorno() map[string]string {
	claves := []string{
		"RULE_VENTANA_ROBO",
		"RULE_VENTANA_ACCIDENTE",
		"RULE_VENTANA_INCENDIO",
		"RULE_VENTANA_VELOCIDAD",
		"RULE_VENTANA_PERSECUCION",
		"RULE_UMBRAL_VELOCIDAD",
		"RULE_VELOCIDAD_MAXIMA_PLAUSIBLE",
		"RULE_MIN_LPR_VELOCIDAD",
		"RULE_MIN_INFRACCIONES_PERSECUCION",
		"RULE_CAIDA_DESDE",
		"RULE_CAIDA_HASTA",
		"RULE_UMBRAL_DECIBELES",
		"RULE_ACCIDENTE_EXIGE_CAIDA",
	}
	m := map[string]string{}
	for _, k := range claves {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			m[k] = v
		}
	}
	return m
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
