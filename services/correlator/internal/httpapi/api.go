package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ciudadinteligente/correlator/internal/canonical"
	"ciudadinteligente/correlator/internal/store"
	"ciudadinteligente/correlator/internal/window"
)

// RespuestaError es el cuerpo de error uniforme de la API.
type RespuestaError struct {
	Estado    string `json:"status"`
	Mensaje   string `json:"message"`
	TipoError string `json:"error_type"`
	Timestamp string `json:"timestamp"`
}

// API agrupa los handlers de gestion del correlator.
type API struct {
	ventanas *window.Store
	almacen  *store.Store
	log      *slog.Logger
	salud    func(context.Context) map[string]any
}

// NewAPI construye los handlers.
func NewAPI(v *window.Store, a *store.Store, log *slog.Logger,
	salud func(context.Context) map[string]any) *API {
	return &API{ventanas: v, almacen: a, log: log, salud: salud}
}

// AlertasActivas atiende GET /alerts/active?zone=...
//
// Devuelve las alertas vigentes de una zona desde Redis, de la mas reciente a
// la mas antigua.
func (a *API) AlertasActivas(w http.ResponseWriter, r *http.Request) {
	zona := strings.TrimSpace(r.URL.Query().Get("zone"))
	if zona == "" {
		escribirError(w, http.StatusBadRequest,
			"el parametro zone es obligatorio", "parametro_faltante")
		return
	}

	alertas, err := a.ventanas.AlertasActivas(r.Context(), zona)
	if err != nil {
		a.log.Error("no se pudieron leer las alertas activas",
			slog.String("zona", zona), slog.String("error", err.Error()))
		escribirError(w, http.StatusServiceUnavailable,
			"no se pudieron leer las alertas activas", "dependencia_no_disponible")
		return
	}

	if alertas == nil {
		alertas = []json.RawMessage{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"zone":      zona,
		"count":     len(alertas),
		"alerts":    alertas,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// AlertasPersistidas atiende GET /alerts/db?zone=...&limit=...
//
// El endpoint equivalente del sistema Java hacia findAll() sin filtro ni tope
// y devolvia la tabla completa en cada llamada.
func (a *API) AlertasPersistidas(w http.ResponseWriter, r *http.Request) {
	zona := strings.TrimSpace(r.URL.Query().Get("zone"))

	limite := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limite = n
		}
	}

	alertas, err := a.almacen.AlertasPorZona(r.Context(), zona, limite)
	if err != nil {
		a.log.Error("no se pudieron consultar las alertas",
			slog.String("error", err.Error()))
		escribirError(w, http.StatusServiceUnavailable,
			"no se pudieron consultar las alertas", "dependencia_no_disponible")
		return
	}

	if alertas == nil {
		alertas = []store.AlertaPersistida{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"zone":      zona,
		"count":     len(alertas),
		"alerts":    alertas,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Health atiende GET /health con el estado real de las dependencias.
func (a *API) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	estado := a.salud(ctx)

	codigo := http.StatusOK
	if estado["status"] != "UP" {
		codigo = http.StatusServiceUnavailable
	}
	WriteJSON(w, codigo, estado)
}

// SchemaEvento atiende GET /schema/event.
func (a *API) SchemaEvento(w http.ResponseWriter, r *http.Request) {
	escribirSchema(w, canonical.EventSchemaJSON())
}

// SchemaAlerta atiende GET /schema/alert.
func (a *API) SchemaAlerta(w http.ResponseWriter, r *http.Request) {
	escribirSchema(w, canonical.AlertSchemaJSON())
}

func escribirSchema(w http.ResponseWriter, schema []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(schema)
}

func escribirError(w http.ResponseWriter, codigo int, mensaje, tipo string) {
	WriteJSON(w, codigo, RespuestaError{
		Estado:    "error",
		Mensaje:   mensaje,
		TipoError: tipo,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// ConAutenticacion exige un token Bearer en los endpoints de consulta.
//
// Las alertas revelan donde y cuando ocurren incidentes en la ciudad: es
// informacion operativa de seguridad ciudadana, no un dato publico. En el
// sistema Java estos endpoints estaban abiertos sin ninguna comprobacion.
func ConAutenticacion(token string, log *slog.Logger, siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			// Sin token configurado la API queda abierta; ya se advierte en
			// el arranque. Facilita las pruebas locales.
			siguiente.ServeHTTP(w, r)
			return
		}

		recibido := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(recibido), []byte(token)) != 1 {
			log.Warn("consulta rechazada por credenciales invalidas",
				slog.String("ruta", r.URL.Path),
				slog.String("remoto", r.RemoteAddr))
			escribirError(w, http.StatusUnauthorized,
				"credenciales invalidas o ausentes", "no_autorizado")
			return
		}
		siguiente.ServeHTTP(w, r)
	})
}

// ConRecuperacion evita que un panic tumbe el proceso.
func ConRecuperacion(log *slog.Logger, siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic atendiendo la peticion",
					slog.Any("panic", rec),
					slog.String("ruta", r.URL.Path))
				escribirError(w, http.StatusInternalServerError,
					"error interno del servidor", "interno")
			}
		}()
		siguiente.ServeHTTP(w, r)
	})
}
