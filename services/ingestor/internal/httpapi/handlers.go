package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"ciudadinteligente/ingestor/internal/canonical"
	"ciudadinteligente/ingestor/internal/ingest"
	"ciudadinteligente/ingestor/internal/obs"
)

// RespuestaError es el cuerpo de error uniforme de la API.
type RespuestaError struct {
	Estado    string                 `json:"status"`
	Mensaje   string                 `json:"message"`
	TipoError string                 `json:"error_type"`
	Campos    []canonical.FieldError `json:"errors,omitempty"`
	EventID   string                 `json:"event_id,omitempty"`
	TraceID   string                 `json:"trace_id,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// RespuestaEvento es el cuerpo de exito de POST /events.
type RespuestaEvento struct {
	Estado       string `json:"status"`
	Mensaje      string `json:"message"`
	EventID      string `json:"event_id"`
	EventType    string `json:"event_type"`
	PartitionKey string `json:"partition_key"`
	TraceID      string `json:"trace_id"`
	Timestamp    string `json:"timestamp"`
}

// ResultadoBulk es el resultado de un evento dentro de un lote.
type ResultadoBulk struct {
	Indice  int                    `json:"index"`
	Estado  string                 `json:"status"`
	EventID string                 `json:"event_id,omitempty"`
	Mensaje string                 `json:"message,omitempty"`
	Campos  []canonical.FieldError `json:"errors,omitempty"`
}

// RespuestaBulk es el cuerpo de POST /events/bulk.
type RespuestaBulk struct {
	Estado     string          `json:"status"`
	Total      int             `json:"total"`
	Aceptados  int             `json:"accepted"`
	Rechazados int             `json:"rejected"`
	Resultados []ResultadoBulk `json:"results"`
	TraceID    string          `json:"trace_id"`
	Timestamp  string          `json:"timestamp"`
}

// Ingestor es el pipeline que la API expone por HTTP.
//
// Se declara como interfaz y no como *ingest.Service para poder probar el
// mapeo de errores a codigos de estado sin levantar Postgres ni Kafka.
type Ingestor interface {
	Ingest(ctx context.Context, raw []byte) (*canonical.Event, error)
}

// API agrupa los handlers del ingestor.
type API struct {
	servicio Ingestor
	log      *slog.Logger
	salud    func(context.Context) map[string]any
}

// NewAPI construye los handlers.
func NewAPI(s Ingestor, log *slog.Logger, salud func(context.Context) map[string]any) *API {
	return &API{servicio: s, log: log, salud: salud}
}

// IngestOne atiende POST /events.
func (a *API) IngestOne(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		a.error(w, ctx, http.StatusBadRequest, "no se pudo leer el cuerpo de la peticion", "cuerpo_ilegible", nil, "")
		return
	}

	ev, err := a.servicio.Ingest(ctx, raw)
	if err != nil {
		a.errorDePipeline(w, ctx, err, ev)
		return
	}

	WriteJSON(w, http.StatusAccepted, RespuestaEvento{
		Estado:       "success",
		Mensaje:      "evento validado y publicado",
		EventID:      ev.EventID,
		EventType:    ev.EventType,
		PartitionKey: ev.PartitionKey,
		TraceID:      ev.TraceID,
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// IngestBulk atiende POST /events/bulk.
//
// Cada elemento se procesa de forma independiente: un evento invalido no
// invalida el lote entero. La respuesta detalla el resultado por indice para
// que el productor sepa exactamente cual reenviar.
func (a *API) IngestBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		a.error(w, ctx, http.StatusBadRequest, "no se pudo leer el cuerpo de la peticion", "cuerpo_ilegible", nil, "")
		return
	}

	// Se deserializa a RawMessage para conservar los bytes originales de cada
	// elemento: el pipeline valida el JSON crudo, no una reserializacion.
	var elementos []json.RawMessage
	if err := json.Unmarshal(raw, &elementos); err != nil {
		a.error(w, ctx, http.StatusBadRequest,
			"el cuerpo debe ser un arreglo JSON de eventos canonicos", "json_invalido", nil, "")
		return
	}

	resultados := make([]ResultadoBulk, 0, len(elementos))
	aceptados := 0

	for i, elemento := range elementos {
		ev, err := a.servicio.Ingest(ctx, elemento)
		if err != nil {
			resultados = append(resultados, a.resultadoFallido(i, err, ev))
			continue
		}
		aceptados++
		resultados = append(resultados, ResultadoBulk{
			Indice:  i,
			Estado:  "accepted",
			EventID: ev.EventID,
		})
	}

	WriteJSON(w, http.StatusAccepted, RespuestaBulk{
		Estado:     "completed",
		Total:      len(elementos),
		Aceptados:  aceptados,
		Rechazados: len(elementos) - aceptados,
		Resultados: resultados,
		TraceID:    obs.TraceFrom(ctx),
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// Health atiende GET /events/health con el estado real de las dependencias.
func (a *API) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	estado := a.salud(ctx)

	// Un health que devuelve 200 con dependencias caidas no sirve para que el
	// orquestador decida sacar la instancia de rotacion.
	codigo := http.StatusOK
	if estado["status"] != "UP" {
		codigo = http.StatusServiceUnavailable
	}
	WriteJSON(w, codigo, estado)
}

// Schema atiende GET /events/schema.
func (a *API) Schema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(canonical.SchemaJSON())
}

// errorDePipeline traduce los errores del pipeline a codigos HTTP.
//
// Es la correccion del comportamiento del servicio Java, que respondia 400 a
// todo: el cliente no podia distinguir un evento mal formado (no reintentar,
// corregir) de una caida de Kafka (reintentar mas tarde).
func (a *API) errorDePipeline(w http.ResponseWriter, ctx context.Context, err error, ev *canonical.Event) {
	eventID := ""
	if ev != nil {
		eventID = ev.EventID
	}

	var det *ingest.ErrorDetallado
	campos := []canonical.FieldError(nil)
	mensaje := err.Error()
	if errors.As(err, &det) {
		campos = det.Campos
		mensaje = det.Mensaje
	}

	switch {
	case errors.Is(err, ingest.ErrContrato):
		a.error(w, ctx, http.StatusBadRequest, mensaje, "contrato_invalido", campos, eventID)
	case errors.Is(err, ingest.ErrDuplicado):
		a.error(w, ctx, http.StatusConflict, mensaje, "event_id_duplicado", nil, eventID)
	case errors.Is(err, ingest.ErrInfraestructura):
		a.error(w, ctx, http.StatusServiceUnavailable, mensaje, "dependencia_no_disponible", nil, eventID)
	default:
		obs.LoggerFrom(ctx, a.log).Error("error no clasificado en el pipeline",
			slog.String("error", err.Error()))
		a.error(w, ctx, http.StatusInternalServerError, "error interno del servidor", "interno", nil, eventID)
	}
}

func (a *API) resultadoFallido(indice int, err error, ev *canonical.Event) ResultadoBulk {
	res := ResultadoBulk{Indice: indice, Mensaje: err.Error()}
	if ev != nil {
		res.EventID = ev.EventID
	}

	var det *ingest.ErrorDetallado
	if errors.As(err, &det) {
		res.Campos = det.Campos
		res.Mensaje = det.Mensaje
	}

	switch {
	case errors.Is(err, ingest.ErrContrato):
		res.Estado = "rejected_contract"
	case errors.Is(err, ingest.ErrDuplicado):
		res.Estado = "rejected_duplicate"
	case errors.Is(err, ingest.ErrInfraestructura):
		res.Estado = "rejected_unavailable"
	default:
		res.Estado = "rejected_internal"
	}
	return res
}

func (a *API) error(w http.ResponseWriter, ctx context.Context, codigo int,
	mensaje, tipo string, campos []canonical.FieldError, eventID string) {
	WriteJSON(w, codigo, RespuestaError{
		Estado:    "error",
		Mensaje:   mensaje,
		TipoError: tipo,
		Campos:    campos,
		EventID:   eventID,
		TraceID:   obs.TraceFrom(ctx),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}
