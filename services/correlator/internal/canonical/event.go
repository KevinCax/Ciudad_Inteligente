// Package canonical implementa los contratos que el correlator consume y produce:
// el evento canonico v1.0 de entrada y la alerta correlacionada v1.0 de salida.
//
// El contrato vive en contracts/canonical-event/v1.0/schema.json a nivel del
// repositorio; este paquete mantiene una copia embebida (schema.json) que se
// sincroniza con `make sync-contracts`. Ver contracts/README.md para el porque.
package canonical

import (
	"fmt"
	"time"
)

// Tipos de evento admitidos por el contrato v1.0.
const (
	TypePanicButton   = "panic.button"
	TypeSensorLPR     = "sensor.lpr"
	TypeSensorSpeed   = "sensor.speed"
	TypeSensorAcustic = "sensor.acoustic"
	TypeCitizenReport = "citizen.report"
)

// Version del contrato que este servicio produce y acepta.
const Version = "1.0"

// Event es el evento canonico v1.0.
//
// Timestamp se conserva como string a proposito: el contrato exige
// date-time ISO-8601 y deserializar a time.Time reformatearia el valor al
// volver a serializarlo, lo que puede cambiar la representacion que el
// productor firmo (p. ej. perder la precision de microsegundos o el offset
// original). Usar TimestampUTC() cuando se necesite el valor como time.Time.
type Event struct {
	EventVersion  string         `json:"event_version"`
	EventType     string         `json:"event_type"`
	EventID       string         `json:"event_id"`
	Producer      string         `json:"producer"`
	Source        string         `json:"source"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	TraceID       string         `json:"trace_id,omitempty"`
	Timestamp     string         `json:"timestamp"`
	PartitionKey  string         `json:"partition_key"`
	Geo           Geo            `json:"geo"`
	Severity      string         `json:"severity"`
	Payload       map[string]any `json:"payload"`
}

// Geo es la ubicacion del evento. Zone es obligatoria; lat/lon son opcionales.
//
// Lat y Lon son punteros porque 0.0 es una coordenada valida: con float64 y
// omitempty, una coordenada legitima en el ecuador o el meridiano de Greenwich
// se perderia al serializar.
type Geo struct {
	Zone string   `json:"zone"`
	Lat  *float64 `json:"lat,omitempty"`
	Lon  *float64 `json:"lon,omitempty"`
}

// TimestampUTC parsea el timestamp del evento a time.Time en UTC.
func (e *Event) TimestampUTC() (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q no es RFC3339: %w", e.Timestamp, err)
	}
	return t.UTC(), nil
}

// Zone devuelve la zona geografica del evento.
func (e *Event) Zone() string {
	return e.Geo.Zone
}

// PayloadString lee una clave del payload como string.
// Devuelve ok=false si la clave no existe o no es string.
func (e *Event) PayloadString(key string) (string, bool) {
	if e.Payload == nil {
		return "", false
	}
	v, ok := e.Payload[key].(string)
	return v, ok
}

// PayloadFloat lee una clave del payload como float64.
//
// El payload llega como map[string]any desde encoding/json, donde todo numero
// es float64. Se acepta tambien string numerico porque los simuladores no son
// consistentes: algunos envian "velocidad_estimada": 95 y otros "95".
func (e *Event) PayloadFloat(key string) (float64, bool) {
	if e.Payload == nil {
		return 0, false
	}
	switch v := e.Payload[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
