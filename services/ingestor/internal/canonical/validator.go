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

// printer traduce los ErrorKind del validador a texto legible.
//
// No puede ser nil: jsonschema lo desreferencia sin comprobarlo y provoca un
// panic al formatear ciertos errores (additionalProperties entre ellos).
var printer = message.NewPrinter(language.English)

// schemaJSON es la copia embebida del contrato.
// Sincronizar con `make sync-contracts`; no editar a mano.
//
//go:embed schema.json
var schemaJSON []byte

// SchemaJSON expone el contrato tal cual, para servirlo en GET /events/schema.
func SchemaJSON() []byte { return schemaJSON }

// Validator valida documentos contra el evento canonico v1.0.
//
// Se valida el JSON *crudo* del cliente, no el struct ya deserializado, y el
// orden importa mas de lo que parece: es lo que hace que la restriccion
// "additionalProperties": false del contrato se aplique de verdad. Al
// deserializar primero, los campos desconocidos se descartan silenciosamente
// antes de llegar al validador y la restriccion no detecta nada.
type Validator struct {
	schema *jsonschema.Schema
}

// NewValidator compila el schema embebido.
func NewValidator() (*Validator, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("schema canonico ilegible: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("canonical-event.json", doc); err != nil {
		return nil, fmt.Errorf("no se pudo registrar el schema: %w", err)
	}

	schema, err := c.Compile("canonical-event.json")
	if err != nil {
		return nil, fmt.Errorf("no se pudo compilar el schema: %w", err)
	}

	return &Validator{schema: schema}, nil
}

// ValidationError agrupa los fallos de contrato de un documento.
// Se devuelve al cliente como lista de campos, segun exige la guia
// (400 Bad Request con la lista de campos que fallaron).
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
	return "evento no cumple el contrato v1.0 [" + strings.Join(parts, "; ") + "]"
}

// Validate verifica un documento ya deserializado a any.
//
// El documento debe venir de jsonschema.UnmarshalJSON (que usa json.Number),
// no de un encoding/json.Unmarshal corriente: el validador necesita los
// numeros sin convertir para aplicar bien las restricciones numericas.
func (v *Validator) Validate(doc any) error {
	err := v.schema.Validate(doc)
	if err == nil {
		return nil
	}

	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return err
	}

	return &ValidationError{Fields: flatten(verr)}
}

// flatten aplana el arbol de causas del validador a una lista plana de campos.
// jsonschema devuelve las causas anidadas; al cliente le sirve mas la hoja
// concreta ("geo.zone es obligatorio") que la rama intermedia.
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

// ParseDocument deserializa JSON crudo a un documento apto para validar.
func ParseDocument(raw []byte) (any, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("JSON invalido: %w", err)
	}
	return doc, nil
}

// DocumentToEvent convierte un documento validado al struct Event.
func DocumentToEvent(doc any) (*Event, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("no se pudo reserializar el documento: %w", err)
	}

	var ev Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("no se pudo mapear el evento: %w", err)
	}
	return &ev, nil
}
