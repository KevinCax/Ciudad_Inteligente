package canonical

import (
	"time"

	"github.com/google/uuid"
)

// Tipos de alerta que el correlator puede emitir.
const (
	AlertPossibleRobbery       = "possible_robbery"
	AlertAccident              = "accident"
	AlertFire                  = "fire"
	AlertTrafficSpeedViolation = "traffic_speed_violation"

	// Enlaza las infracciones sucesivas de un mismo vehiculo con el hecho que
	// las origino. Es la unica alerta que no nace de una sola zona.
	AlertVehiclePursuit = "vehicle_pursuit"
)

// Alert es la alerta correlacionada v1.0, contrato de salida hacia el topic
// correlated.alerts y hacia la tabla alerts.
//
// Score es float64 y se persiste en una columna numeric. Declararlo entero
// parece inocuo y no lo es: al redondear, 0.85, 0.90 y 0.95 se guardarian los
// tres como 1 y toda la gradacion de confianza se perderia al persistir.
type Alert struct {
	AlertID       string   `json:"alert_id"`
	CorrelationID string   `json:"correlation_id,omitempty"`
	Type          string   `json:"type"`
	Score         float64  `json:"score"`
	Zone          string   `json:"zone"`
	Window        Window   `json:"window"`
	Evidence      []string `json:"evidence"`
	CreatedAt     string   `json:"created_at"`
	RuleVersion   string   `json:"rule_version,omitempty"`
}

// Window es el intervalo temporal que sustenta la alerta.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// NewAlert construye una alerta con identificador y marca de tiempo propios.
func NewAlert(tipo, zona string, score float64, w Window, evidencia []string, correlationID, ruleVersion string) *Alert {
	return &Alert{
		AlertID:       uuid.NewString(),
		CorrelationID: correlationID,
		Type:          tipo,
		Score:         score,
		Zone:          zona,
		Window:        w,
		Evidence:      evidencia,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		RuleVersion:   ruleVersion,
	}
}
