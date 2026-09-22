package canonical

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Copias embebidas de los contratos. Sincronizar con `make sync-contracts`.
//
//go:embed schema.json
var eventSchemaJSON []byte

//go:embed alert-schema.json
var alertSchemaJSON []byte

// printer traduce los ErrorKind del validador a texto legible.
// No puede ser nil: jsonschema lo desreferencia sin comprobarlo.
var printer = message.NewPrinter(language.English)

// Validator valida eventos de entrada y alertas de salida.
//
// Validar el evento de entrada es defensa en profundidad segun la guia
// (seccion 2.3, "validar en productor y nuevamente en consumidor"): el
// correlator no confia en que el ingestor haya validado bien.
//
// Tambien se valida la alerta de salida, no solo el evento de entrada: evita
// publicar en correlated.alerts mensajes que los consumidores aguas abajo no
// puedan interpretar.
type Validator struct {
	event *jsonschema.Schema
	alert *jsonschema.Schema
}

// NewValidator compila ambos contratos embebidos.
func NewValidator() (*Validator, error) {
	event, err := compile("canonical-event.json", eventSchemaJSON)
	if err != nil {
		return nil, fmt.Errorf("contrato de evento: %w", err)
	}
	alert, err := compile("correlated-alert.json", alertSchemaJSON)
	if err != nil {
		return nil, fmt.Errorf("contrato de alerta: %w", err)
	}
	return &Validator{event: event, alert: alert}, nil
}

func compile(nombre string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema ilegible: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(nombre, doc); err != nil {
		return nil, fmt.Errorf("no se pudo registrar el schema: %w", err)
	}
	return c.Compile(nombre)
}

// ValidationError agrupa los fallos de contrato de un documento.
type ValidationError struct {
	Fields []FieldError `json:"errors"`
}

// FieldError es un fallo puntual: que campo y por que.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"error"`
}

func (v *ValidationError) Error() string {
	parts := make([]string, 0, len(v.Fields))
	for _, f := range v.Fields {
		parts = append(parts, f.Field+": "+f.Reason)
	}
	return "no cumple el contrato [" + strings.Join(parts, "; ") + "]"
}

// ValidateEventBytes valida el JSON crudo de un evento consumido de Kafka.
func (v *Validator) ValidateEventBytes(raw []byte) error {
	return validateRaw(v.event, raw)
}

// ValidateAlert valida una alerta antes de publicarla.
func (v *Validator) ValidateAlert(a *Alert) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("no se pudo serializar la alerta: %w", err)
	}
	return validateRaw(v.alert, raw)
}

func validateRaw(schema *jsonschema.Schema, raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("JSON invalido: %w", err)
	}
	err = schema.Validate(doc)
	if err == nil {
		return nil
	}
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err
	}
	return &ValidationError{Fields: flatten(verr)}
}

// flatten aplana el arbol de causas a una lista de campos concretos.
func flatten(e *jsonschema.ValidationError) []FieldError {
	if len(e.Causes) == 0 {
		field := strings.Join(e.InstanceLocation, ".")
		if field == "" {
			field = "(raiz)"
		}
		return []FieldError{{Field: field, Reason: e.ErrorKind.LocalizedString(printer)}}
	}
	var out []FieldError
	for _, c := range e.Causes {
		out = append(out, flatten(c)...)
	}
	return out
}

// EventSchemaJSON expone el contrato de evento vigente.
func EventSchemaJSON() []byte { return eventSchemaJSON }

// AlertSchemaJSON expone el contrato de alerta vigente.
func AlertSchemaJSON() []byte { return alertSchemaJSON }

// ParseEvent deserializa el JSON crudo de Kafka al struct Event.
func ParseEvent(raw []byte) (*Event, error) {
	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("no se pudo mapear el evento: %w", err)
	}
	return &ev, nil
}
