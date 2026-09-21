package privacy

import (
	"strings"
	"testing"
)

func TestTokenEsEstable(t *testing.T) {
	tok := NewTokenizador("llave-de-prueba")

	a := tok.Tokenizar("ABC123")
	b := tok.Tokenizar("ABC123")

	if a != b {
		t.Errorf("la misma placa deberia producir el mismo token: %q vs %q", a, b)
	}
	if a == "ABC123" {
		t.Error("el token no puede ser la placa en claro")
	}
	if !strings.HasPrefix(a, "plate_") {
		t.Errorf("el token deberia llevar el prefijo plate_: %q", a)
	}
}

// Sin normalizar, el mismo vehiculo generaria seudonimos distintos segun como
// lo escribiera cada sensor y se perderia la correlacion entre sus apariciones.
func TestNormalizacionDeFormato(t *testing.T) {
	tok := NewTokenizador("llave-de-prueba")
	esperado := tok.Tokenizar("ABC123")

	for _, variante := range []string{"abc123", "ABC-123", " abc 123 ", "A-B-C-1-2-3"} {
		if got := tok.Tokenizar(variante); got != esperado {
			t.Errorf("%q deberia normalizarse al mismo token", variante)
		}
	}
}

func TestPlacasDistintasDanTokensDistintos(t *testing.T) {
	tok := NewTokenizador("llave-de-prueba")
	if tok.Tokenizar("ABC123") == tok.Tokenizar("XYZ789") {
		t.Error("placas distintas no deberian colisionar")
	}
}

// La llave es lo que hace inviable reconstruir la tabla por fuerza bruta: sin
// ella, el espacio de placas es pequeno y enumerable.
func TestLlavesDistintasDanTokensDistintos(t *testing.T) {
	a := NewTokenizador("llave-a").Tokenizar("ABC123")
	b := NewTokenizador("llave-b").Tokenizar("ABC123")

	if a == b {
		t.Error("el token no deberia poder reproducirse sin conocer la llave")
	}
}

func TestSinLlaveNoTokeniza(t *testing.T) {
	tok := NewTokenizador("")

	if tok.Activo() {
		t.Error("sin llave el tokenizador deberia estar inactivo")
	}
	if got := tok.Tokenizar("ABC123"); got != "ABC123" {
		t.Errorf("sin llave deberia devolver la placa intacta, se obtuvo %q", got)
	}
}

// El payload original alimenta la evidencia ya registrada en la ventana:
// mutarlo corromperia la correlacion en curso.
func TestTokenizarPayloadNoMutaElOriginal(t *testing.T) {
	tok := NewTokenizador("llave-de-prueba")
	original := map[string]any{
		"placa_vehicular":    "ABC123",
		"velocidad_estimada": 95.0,
	}

	copia := tok.TokenizarPayload(original)

	if original["placa_vehicular"] != "ABC123" {
		t.Error("se modifico el payload original")
	}
	if copia["placa_vehicular"] == "ABC123" {
		t.Error("la copia deberia llevar la placa tokenizada")
	}
	if copia["velocidad_estimada"] != 95.0 {
		t.Error("los demas campos deben conservarse intactos")
	}
}

func TestPayloadSinPlacaNoCambia(t *testing.T) {
	tok := NewTokenizador("llave-de-prueba")
	payload := map[string]any{"tipo_de_alerta": "panico"}

	copia := tok.TokenizarPayload(payload)

	if copia["tipo_de_alerta"] != "panico" {
		t.Error("un payload sin placa deberia pasar intacto")
	}
}
