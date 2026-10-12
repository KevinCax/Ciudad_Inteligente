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

// Copia embebida del contrato. Sincronizar con `make sync-contracts`.
//
// El notificador solo embebe el contrato de alerta: no consume el topic de
// eventos, de modo que el de evento no le hace falta.
//
//go:embed alert-schema.json
var alertSchemaJSON []byte

// printer traduce los ErrorKind del validador a texto legible.
// No puede ser nil: jsonschema lo desreferencia sin comprobarlo.
var printer = message.NewPrinter(language.English)

// Validator valida las alertas que llegan del topic.
//
// Es defensa en profundidad (seccion 2.3 de la guia: validar en productor y
// nuevamente en consumidor). Aqui tiene un motivo adicional al del persister:
// de este contrato sale una accion hacia afuera. Despachar bomberos por una
// alerta malformada es peor que no despacharlos, porque consume un recurso de
// emergencia real y resta credibilidad al sistema entero.
type Validator struct {
	alert *jsonschema.Schema
}

// NewValidator compila el contrato embebido.
func NewValidator() (*Validator, error) {
	alert, err := compile("correlated-alert.json", alertSchemaJSON)
	if err != nil {
		return nil, fmt.Errorf("contrato de alerta: %w", err)
	}
	return &Validator{alert: alert}, nil
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

// ValidateAlertBytes valida el JSON crudo de una alerta consumida de Kafka.
func (v *Validator) ValidateAlertBytes(raw []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("JSON invalido: %w", err)
	}
	err = v.alert.Validate(doc)
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

// AlertSchemaJSON expone el contrato de alerta vigente.
func AlertSchemaJSON() []byte { return alertSchemaJSON }

// ParseAlert deserializa el JSON crudo de una alerta.
func ParseAlert(raw []byte) (*Alert, error) {
	var a Alert
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("no se pudo mapear la alerta: %w", err)
	}
	return &a, nil
}
