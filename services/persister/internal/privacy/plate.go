// Package privacy protege los datos personales que circulan por el sistema.
//
// La placa vehicular y la ubicacion son datos personales: identifican de forma
// indirecta a una persona. El enunciado lo senala de forma explicita (secciones
// 4.2 y 9) y aclara que la obligacion aplica aunque los datos sean sinteticos,
// porque forma parte del ejercicio de defensa ante el area legal.
//
// El cliente declarado es la Municipalidad de Guatemala, una entidad publica,
// de modo que el tratamiento queda sujeto a la normativa guatemalteca de
// acceso a la informacion publica y proteccion de datos personales en poder
// del Estado. El ADR de privacidad debe citar el articulado concreto.
package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Tokenizador convierte placas en seudonimos estables e irreversibles.
//
// Se usa HMAC-SHA256 y no un hash simple: sin llave, un atacante con el
// listado de placas posibles (que es un espacio pequeno y enumerable) puede
// reconstruir la tabla completa por fuerza bruta en segundos. La llave
// convierte ese ataque en inviable mientras se custodie fuera del alcance del
// dato tokenizado.
//
// La llave vive on-premise y no acompana al dato cuando este cruza a la nube:
// ese es el punto de corte del modelo hibrido, y es lo que permite sostener
// que la nube nunca recibe placas en claro.
type Tokenizador struct {
	llave []byte
}

// NewTokenizador construye el tokenizador. Una llave vacia lo deja inactivo.
func NewTokenizador(llave string) *Tokenizador {
	if llave == "" {
		return &Tokenizador{}
	}
	return &Tokenizador{llave: []byte(llave)}
}

// Activo indica si la tokenizacion esta habilitada.
func (t *Tokenizador) Activo() bool { return len(t.llave) > 0 }

// Tokenizar devuelve el seudonimo de una placa.
//
// Normaliza antes de calcular para que "abc-123", "ABC123" y " ABC 123 "
// produzcan el mismo token: sin normalizar, el mismo vehiculo generaria
// seudonimos distintos segun como lo escribiera cada sensor y se perderia la
// capacidad de correlacionar sus apariciones.
func (t *Tokenizador) Tokenizar(placa string) string {
	if !t.Activo() || placa == "" {
		return placa
	}

	mac := hmac.New(sha256.New, t.llave)
	mac.Write([]byte(normalizar(placa)))

	// 16 bytes (128 bits) bastan para que las colisiones sean despreciables y
	// dejan un token mas manejable en logs y dashboards.
	return "plate_" + hex.EncodeToString(mac.Sum(nil))[:32]
}

// TokenizarPayload sustituye la placa dentro del payload de un evento.
//
// Devuelve una copia: mutar el payload original afectaria a la evidencia ya
// registrada en la ventana de correlacion.
func (t *Tokenizador) TokenizarPayload(payload map[string]any) map[string]any {
	if !t.Activo() || payload == nil {
		return payload
	}

	copia := make(map[string]any, len(payload))
	for k, v := range payload {
		copia[k] = v
	}

	if placa, ok := copia["placa_vehicular"].(string); ok && placa != "" {
		copia["placa_vehicular"] = t.Tokenizar(placa)
	}
	return copia
}

// normalizar deja la placa en mayusculas y sin separadores ni espacios.
func normalizar(placa string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(placa) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
