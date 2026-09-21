package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ciudadinteligente/ingestor/internal/canonical"
	"ciudadinteligente/ingestor/internal/ingest"
	"ciudadinteligente/ingestor/internal/obs"
)

// ingestorFalso permite probar el mapeo de errores sin Postgres ni Kafka.
type ingestorFalso struct {
	evento *canonical.Event
	err    error
	llamas int
}

func (f *ingestorFalso) Ingest(ctx context.Context, raw []byte) (*canonical.Event, error) {
	f.llamas++
	return f.evento, f.err
}

func logSilencioso() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func eventoDemo() *canonical.Event {
	return &canonical.Event{
		EventID:      "a1b2c3d4-0001-4001-8001-000000000001",
		EventType:    canonical.TypePanicButton,
		PartitionKey: "zone_4",
		TraceID:      "c1d2e3f4-0001-4001-8001-200000000001",
		Geo:          canonical.Geo{Zone: "zone_4"},
	}
}

func peticion(t *testing.T, api *API, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(cuerpo))
	r = r.WithContext(obs.WithTrace(r.Context(), "traza-de-prueba"))
	w := httptest.NewRecorder()
	api.IngestOne(w, r)
	return w
}

// El servicio Java respondia 400 a todo porque el controlador capturaba
// Exception y devolvia badRequest. Cada clase de fallo debe tener su codigo:
// el cliente necesita saber si corregir el evento o reintentar mas tarde.
func TestMapeoDeErroresACodigosDeEstado(t *testing.T) {
	casos := []struct {
		nombre    string
		err       error
		esperado  int
		tipoError string
	}{
		{
			nombre:    "evento valido",
			err:       nil,
			esperado:  http.StatusAccepted,
			tipoError: "",
		},
		{
			nombre:    "no cumple el contrato",
			err:       &ingest.ErrorDetallado{Err: ingest.ErrContrato, Mensaje: "falta geo.zone"},
			esperado:  http.StatusBadRequest,
			tipoError: "contrato_invalido",
		},
		{
			nombre:    "event_id duplicado",
			err:       &ingest.ErrorDetallado{Err: ingest.ErrDuplicado, Mensaje: "ya fue ingerido"},
			esperado:  http.StatusConflict,
			tipoError: "event_id_duplicado",
		},
		{
			nombre:    "kafka caido",
			err:       &ingest.ErrorDetallado{Err: ingest.ErrInfraestructura, Mensaje: "no se pudo publicar"},
			esperado:  http.StatusServiceUnavailable,
			tipoError: "dependencia_no_disponible",
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			falso := &ingestorFalso{evento: eventoDemo(), err: c.err}
			api := NewAPI(falso, logSilencioso(), nil)

			w := peticion(t, api, `{"event_id":"x"}`)

			if w.Code != c.esperado {
				t.Fatalf("esperaba %d, se obtuvo %d (cuerpo: %s)", c.esperado, w.Code, w.Body.String())
			}
			if c.tipoError == "" {
				return
			}

			var resp RespuestaError
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("respuesta de error ilegible: %v", err)
			}
			if resp.TipoError != c.tipoError {
				t.Errorf("error_type: esperaba %q, se obtuvo %q", c.tipoError, resp.TipoError)
			}
			if resp.TraceID != "traza-de-prueba" {
				t.Errorf("la respuesta de error debe llevar el trace_id, se obtuvo %q", resp.TraceID)
			}
		})
	}
}

// Los campos que fallaron deben llegar al cliente: la guia exige responder 400
// con la lista de campos, no con un mensaje generico.
func TestErrorDeContratoDevuelveLosCampos(t *testing.T) {
	falso := &ingestorFalso{err: &ingest.ErrorDetallado{
		Err:     ingest.ErrContrato,
		Mensaje: "no cumple el contrato",
		Campos: []canonical.FieldError{
			{Field: "geo.zone", Reason: "es obligatorio"},
			{Field: "severity", Reason: "valor fuera del enum"},
		},
	}}
	api := NewAPI(falso, logSilencioso(), nil)

	w := peticion(t, api, `{}`)

	var resp RespuestaError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	if len(resp.Campos) != 2 {
		t.Fatalf("esperaba 2 campos con error, se obtuvieron %d", len(resp.Campos))
	}
	if resp.Campos[0].Field != "geo.zone" {
		t.Errorf("primer campo: esperaba geo.zone, se obtuvo %q", resp.Campos[0].Field)
	}
}

// Un elemento invalido no debe invalidar el lote completo.
func TestBulkProcesaCadaElementoPorSeparado(t *testing.T) {
	// Falla el segundo de tres.
	var n int
	api := NewAPI(ingestorFunc(func(ctx context.Context, raw []byte) (*canonical.Event, error) {
		n++
		if n == 2 {
			return nil, &ingest.ErrorDetallado{Err: ingest.ErrDuplicado, Mensaje: "duplicado"}
		}
		return eventoDemo(), nil
	}), logSilencioso(), nil)

	cuerpo := `[{"a":1},{"b":2},{"c":3}]`
	r := httptest.NewRequest(http.MethodPost, "/events/bulk", strings.NewReader(cuerpo))
	r = r.WithContext(obs.WithTrace(r.Context(), "traza-bulk"))
	w := httptest.NewRecorder()
	api.IngestBulk(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("esperaba 202, se obtuvo %d", w.Code)
	}

	var resp RespuestaBulk
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}
	if resp.Total != 3 || resp.Aceptados != 2 || resp.Rechazados != 1 {
		t.Errorf("resumen incorrecto: total=%d aceptados=%d rechazados=%d",
			resp.Total, resp.Aceptados, resp.Rechazados)
	}
	if resp.Resultados[1].Estado != "rejected_duplicate" {
		t.Errorf("el elemento 1 deberia estar marcado como duplicado, se obtuvo %q",
			resp.Resultados[1].Estado)
	}
	if resp.Resultados[1].Indice != 1 {
		t.Errorf("el indice debe identificar el elemento del lote, se obtuvo %d", resp.Resultados[1].Indice)
	}
}

func TestBulkRechazaCuerpoQueNoEsArreglo(t *testing.T) {
	api := NewAPI(&ingestorFalso{}, logSilencioso(), nil)
	r := httptest.NewRequest(http.MethodPost, "/events/bulk", strings.NewReader(`{"no":"es un arreglo"}`))
	w := httptest.NewRecorder()
	api.IngestBulk(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("esperaba 400, se obtuvo %d", w.Code)
	}
}

// El health debe responder 503 cuando una dependencia esta caida: si devuelve
// 200 con Postgres inaccesible, el orquestador nunca saca la instancia de
// rotacion. Es el fallo del health del servicio Java.
func TestHealthReflejaElEstadoDeLasDependencias(t *testing.T) {
	casos := map[string]int{
		"UP":       http.StatusOK,
		"DEGRADED": http.StatusServiceUnavailable,
		"DOWN":     http.StatusServiceUnavailable,
	}

	for estado, esperado := range casos {
		t.Run(estado, func(t *testing.T) {
			api := NewAPI(&ingestorFalso{}, logSilencioso(),
				func(ctx context.Context) map[string]any {
					return map[string]any{"status": estado}
				})

			r := httptest.NewRequest(http.MethodGet, "/events/health", nil)
			w := httptest.NewRecorder()
			api.Health(w, r)

			if w.Code != esperado {
				t.Errorf("estado %s: esperaba %d, se obtuvo %d", estado, esperado, w.Code)
			}
		})
	}
}

func TestSchemaDevuelveElContrato(t *testing.T) {
	api := NewAPI(&ingestorFalso{}, logSilencioso(), nil)
	r := httptest.NewRequest(http.MethodGet, "/events/schema", nil)
	w := httptest.NewRecorder()
	api.Schema(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("esperaba 200, se obtuvo %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "CanonicalEventV1") {
		t.Error("el schema devuelto no es el contrato canonico")
	}
}

// ---- middleware ----

func TestAutenticacionRechazaClaveInvalida(t *testing.T) {
	protegido := ConAutenticacion("clave-correcta", logSilencioso())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	casos := map[string]int{
		"clave-correcta":   http.StatusOK,
		"clave-incorrecta": http.StatusUnauthorized,
		"":                 http.StatusUnauthorized,
	}

	for clave, esperado := range casos {
		r := httptest.NewRequest(http.MethodPost, "/events", nil)
		if clave != "" {
			r.Header.Set(CabeceraAPIKey, clave)
		}
		w := httptest.NewRecorder()
		protegido.ServeHTTP(w, r)

		if w.Code != esperado {
			t.Errorf("clave %q: esperaba %d, se obtuvo %d", clave, esperado, w.Code)
		}
	}
}

func TestSinClaveConfiguradaNoSeExigeAutenticacion(t *testing.T) {
	abierto := ConAutenticacion("", logSilencioso())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	r := httptest.NewRequest(http.MethodPost, "/events", nil)
	w := httptest.NewRecorder()
	abierto.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("sin API_KEY el endpoint debe quedar abierto, se obtuvo %d", w.Code)
	}
}

// El trace_id debe generarse si no viene, y respetarse si el productor lo
// envia: es lo que permite correlacionar de extremo a extremo.
func TestTraceSeGeneraYSeRespeta(t *testing.T) {
	var visto string
	h := ConTrace()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = obs.TraceFrom(r.Context())
	}))

	t.Run("se genera si falta", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/events", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if visto == "" {
			t.Error("no se genero trace_id")
		}
		if w.Header().Get(CabeceraTrace) != visto {
			t.Error("el trace_id no se devolvio en la cabecera de respuesta")
		}
	})

	t.Run("se respeta el del productor", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/events", nil)
		r.Header.Set(CabeceraTrace, "traza-del-productor")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if visto != "traza-del-productor" {
			t.Errorf("se sobrescribio el trace_id del productor: %q", visto)
		}
	})
}

// Un panic no debe tumbar el proceso ni dejar al cliente sin respuesta.
func TestRecuperacionAntePanic(t *testing.T) {
	h := ConRecuperacion(logSilencioso())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("fallo inesperado")
		}))

	r := httptest.NewRequest(http.MethodPost, "/events", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, r) // no debe propagar el panic

	if w.Code != http.StatusInternalServerError {
		t.Errorf("esperaba 500, se obtuvo %d", w.Code)
	}
}

// ingestorFunc adapta una funcion a la interfaz Ingestor.
type ingestorFunc func(ctx context.Context, raw []byte) (*canonical.Event, error)

func (f ingestorFunc) Ingest(ctx context.Context, raw []byte) (*canonical.Event, error) {
	return f(ctx, raw)
}
