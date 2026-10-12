// Package canonical contiene el contrato de alerta que el notificador consume.
package canonical

// Tipos de alerta que el correlator puede emitir.
//
// El notificador no los crea: los recibe. La lista existe aqui para que la
// matriz de despacho se escriba contra constantes y no contra literales
// sueltos, de modo que un tipo nuevo sin entrada en la matriz se detecte al
// compilar y no en produccion, cuando una emergencia no avise a nadie.
const (
	AlertPossibleRobbery       = "possible_robbery"
	AlertAccident              = "accident"
	AlertFire                  = "fire"
	AlertTrafficSpeedViolation = "traffic_speed_violation"
	AlertVehiclePursuit        = "vehicle_pursuit"
)

// Alert es la alerta correlacionada v1.0, contrato de entrada desde el topic
// correlated.alerts.
//
// Score es float64 y no entero: al redondear, 0.85, 0.90 y 0.95 se volverian
// los tres 1 y se perderia toda la gradacion de confianza, que es justo lo que
// permite decidir si un aviso merece movilizar un recurso.
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
