package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ciudadinteligente/jobs/internal/scheduler"
	"ciudadinteligente/jobs/internal/store"
)

// API expone el estado del planificador y la ejecucion manual.
//
// Es la respuesta minima a lo unico que de verdad se echa de menos de Airflow:
// poder ver que tareas hay y volver a lanzar la que fallo anoche, sin tener
// que entrar al contenedor.
type API struct {
	plan    *scheduler.Scheduler
	almacen *store.Store
	token   string
	log     *slog.Logger
	salud   func(context.Context) map[string]any
}

// NewAPI construye los handlers.
func NewAPI(plan *scheduler.Scheduler, almacen *store.Store, token string,
	log *slog.Logger, salud func(context.Context) map[string]any) *API {
	return &API{plan: plan, almacen: almacen, token: token, log: log, salud: salud}
}

// Health atiende GET /health.
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

// Listar atiende GET /tareas: qué hay programado y a qué hora.
func (a *API) Listar(w http.ResponseWriter, r *http.Request) {
	tareas := a.plan.Tareas()
	salida := make([]map[string]string, 0, len(tareas))
	for _, t := range tareas {
		salida = append(salida, map[string]string{"nombre": t.Nombre, "hora": t.Hora})
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"tareas":    salida,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Ejecutar atiende POST /tareas/{nombre}/ejecutar.
//
// Corre la tarea de inmediato, saltandose el horario y el cerrojo diario. Es
// para reprocesar manualmente lo que fallo, que en Airflow seria un clic en la
// interfaz.
func (a *API) Ejecutar(w http.ResponseWriter, r *http.Request) {
	nombre := r.PathValue("nombre")

	// Parametros opcionales por query. Hoy solo se usa "dia", que permite
	// recalcular el resumen de una fecha concreta.
	params := scheduler.Parametros{}
	if dia := strings.TrimSpace(r.URL.Query().Get("dia")); dia != "" {
		params["dia"] = dia
	}

	// Plazo generoso: una purga sobre un historico grande puede tardar.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()

	a.log.Info("ejecucion manual solicitada",
		slog.String("tarea", nombre),
		slog.String("remoto", r.RemoteAddr))

	res, err := a.plan.EjecutarAhora(ctx, nombre, params)
	if err != nil {
		if strings.Contains(err.Error(), "no existe la tarea") {
			WriteJSON(w, http.StatusNotFound, map[string]any{
				"status":  "error",
				"message": err.Error(),
			})
			return
		}
		WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"tarea":   nombre,
			"message": err.Error(),
		})
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "completada",
		"tarea":     nombre,
		"afectadas": res.Afectadas,
		"detalle":   res.Detalle,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Proteger exige el token en los endpoints que modifican datos.
//
// Lanzar la retencion a mano borra informacion de forma irreversible: no puede
// quedar al alcance de cualquiera que alcance el puerto.
func (a *API) Proteger(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.token == "" {
			siguiente.ServeHTTP(w, r)
			return
		}

		recibido := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(recibido), []byte(a.token)) != 1 {
			a.log.Warn("ejecucion manual rechazada por credenciales invalidas",
				slog.String("ruta", r.URL.Path),
				slog.String("remoto", r.RemoteAddr))
			WriteJSON(w, http.StatusUnauthorized, map[string]any{
				"status":  "error",
				"message": "credenciales invalidas o ausentes",
			})
			return
		}
		siguiente.ServeHTTP(w, r)
	})
}
