package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"ciudadinteligente/ingestor/internal/obs"
)

// CabeceraTrace es la cabecera por la que entra y sale el identificador de
// traza. Permite que el productor imponga su propio trace_id y que la
// correlacion extremo a extremo empiece antes del ingestor.
const CabeceraTrace = "X-Trace-Id"

// CabeceraAPIKey es la cabecera de autenticacion de los endpoints de ingesta.
const CabeceraAPIKey = "X-API-Key"

// Middleware es una capa que envuelve un handler.
type Middleware func(http.Handler) http.Handler

// Encadenar aplica los middlewares en el orden en que se declaran: el primero
// de la lista es el mas externo.
func Encadenar(h http.Handler, ms ...Middleware) http.Handler {
	for i := len(ms) - 1; i >= 0; i-- {
		h = ms[i](h)
	}
	return h
}

// registrador captura el codigo de estado para poder medirlo.
type registrador struct {
	http.ResponseWriter
	estado   int
	escribio bool
}

func (r *registrador) WriteHeader(code int) {
	if !r.escribio {
		r.estado = code
		r.escribio = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *registrador) Write(b []byte) (int, error) {
	if !r.escribio {
		r.estado = http.StatusOK
		r.escribio = true
	}
	return r.ResponseWriter.Write(b)
}

// ConTrace asegura que toda peticion tenga trace_id y lo propaga por el
// contexto y por la respuesta.
//
// Es la pieza que hace posible seguir un evento desde el productor hasta la
// alerta: el mismo identificador viaja en el log del ingestor, en la cabecera
// del mensaje de Kafka y en el log del correlator.
func ConTrace() Middleware {
	return func(siguiente http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID := r.Header.Get(CabeceraTrace)
			if traceID == "" {
				traceID = uuid.NewString()
			}
			w.Header().Set(CabeceraTrace, traceID)
			siguiente.ServeHTTP(w, r.WithContext(obs.WithTrace(r.Context(), traceID)))
		})
	}
}

// ConMetricas mide duracion y codigo de estado por endpoint.
func ConMetricas(m *obs.Metrics, endpoint string) Middleware {
	return func(siguiente http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inicio := time.Now()
			rec := &registrador{ResponseWriter: w, estado: http.StatusOK}
			siguiente.ServeHTTP(rec, r)
			m.IngestDuration.
				WithLabelValues(endpoint, strconv.Itoa(rec.estado)).
				Observe(time.Since(inicio).Seconds())
		})
	}
}

// ConRecuperacion evita que un panic tumbe el proceso entero.
//
// Sin esto, un evento con una forma inesperada podria derribar el ingestor y
// con el toda la ingesta, que es justo el fallo que el enunciado pide prever
// en el ADR de resiliencia.
func ConRecuperacion(log *slog.Logger) Middleware {
	return func(siguiente http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					obs.LoggerFrom(r.Context(), log).Error("panic atendiendo la peticion",
						slog.Any("panic", rec),
						slog.String("ruta", r.URL.Path))
					WriteJSON(w, http.StatusInternalServerError, RespuestaError{
						Estado:    "error",
						Mensaje:   "error interno del servidor",
						TipoError: "interno",
						TraceID:   obs.TraceFrom(r.Context()),
						Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
					})
				}
			}()
			siguiente.ServeHTTP(w, r)
		})
	}
}

// ConAutenticacion exige la API key en los endpoints de ingesta.
//
// Se compara en tiempo constante: una comparacion normal de strings filtra
// informacion sobre la clave a traves del tiempo de respuesta.
func ConAutenticacion(apiKey string, log *slog.Logger) Middleware {
	return func(siguiente http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sin clave configurada el servicio queda abierto; ya se advierte
			// en el arranque. Facilita las pruebas locales sin obligar a
			// gestionar secretos.
			if apiKey == "" {
				siguiente.ServeHTTP(w, r)
				return
			}

			recibida := r.Header.Get(CabeceraAPIKey)
			if subtle.ConstantTimeCompare([]byte(recibida), []byte(apiKey)) != 1 {
				obs.LoggerFrom(r.Context(), log).Warn("peticion rechazada por credenciales invalidas",
					slog.String("ruta", r.URL.Path),
					slog.String("remoto", r.RemoteAddr))
				WriteJSON(w, http.StatusUnauthorized, RespuestaError{
					Estado:    "error",
					Mensaje:   "credenciales invalidas o ausentes",
					TipoError: "no_autorizado",
					TraceID:   obs.TraceFrom(r.Context()),
					Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
				})
				return
			}
			siguiente.ServeHTTP(w, r)
		})
	}
}

// ConLimiteDeCuerpo acota el tamano del cuerpo aceptado.
//
// Sin limite, una sola peticion puede agotar la memoria del proceso. El
// tamano por defecto se alinea con message.max.bytes de Kafka (1 MiB) para
// los eventos individuales.
func ConLimiteDeCuerpo(maxBytes int64) Middleware {
	return func(siguiente http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			siguiente.ServeHTTP(w, r)
		})
	}
}
