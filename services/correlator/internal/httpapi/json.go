package httpapi

import (
	"encoding/json"
	"net/http"
)

// WriteJSON serializa v como respuesta JSON con el status indicado.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
