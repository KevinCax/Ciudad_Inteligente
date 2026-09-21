package canonical

import (
	"strings"
	"testing"
	"time"
)

const eventoValido = `{
  "event_version": "1.0",
  "event_type": "panic.button",
  "event_id": "a1b2c3d4-0001-4001-8001-000000000001",
  "producer": "test-suite",
  "source": "simulated",
  "correlation_id": "b1c2d3e4-0001-4001-8001-100000000001",
  "trace_id": "c1d2e3f4-0001-4001-8001-200000000001",
  "timestamp": "2026-09-20T15:00:00Z",
  "partition_key": "zone_4",
  "geo": {"zone": "zone_4", "lat": 14.628, "lon": -90.522},
  "severity": "critical",
  "payload": {"tipo_de_alerta": "panico", "identificador_dispositivo": "BTN-001"}
}`

func validarCrudo(t *testing.T, raw string) error {
	t.Helper()
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("no se pudo construir el validador: %v", err)
	}
	doc, err := ParseDocument([]byte(raw))
	if err != nil {
		return err
	}
	return v.Validate(doc)
}

func TestEventoValidoPasa(t *testing.T) {
	if err := validarCrudo(t, eventoValido); err != nil {
		t.Fatalf("un evento valido fue rechazado: %v", err)
	}
}

// Esta es la diferencia de fondo con el servicio Java: alli se validaba el
// objeto ya deserializado, de modo que Jackson descartaba los campos
// desconocidos antes de validar y additionalProperties:false nunca detectaba
// nada. Aqui se valida el JSON crudo, asi que si aplica.
func TestCampoDesconocidoEsRechazado(t *testing.T) {
	conExtra := strings.Replace(eventoValido,
		`"severity": "critical",`,
		`"severity": "critical", "campo_no_declarado": "x",`, 1)

	err := validarCrudo(t, conExtra)
	if err == nil {
		t.Fatal("se acepto un evento con un campo fuera del contrato")
	}
	if !strings.Contains(err.Error(), "campo_no_declarado") {
		t.Errorf("el error deberia nombrar el campo infractor, se obtuvo: %v", err)
	}
}

func TestCamposObligatoriosFaltantes(t *testing.T) {
	casos := map[string]string{
		"sin geo":      `"geo": {"zone": "zone_4", "lat": 14.628, "lon": -90.522},`,
		"sin severity": `"severity": "critical",`,
		"sin payload":  `"payload": {"tipo_de_alerta": "panico", "identificador_dispositivo": "BTN-001"}`,
	}
	for nombre, fragmento := range casos {
		t.Run(nombre, func(t *testing.T) {
			sinCampo := strings.Replace(eventoValido, fragmento, "", 1)
			// Limpiar la coma colgante que deja quitar el ultimo campo.
			sinCampo = strings.Replace(sinCampo, ",\n}", "\n}", 1)
			if err := validarCrudo(t, sinCampo); err == nil {
				t.Errorf("se acepto un evento %s", nombre)
			}
		})
	}
}

func TestValoresFueraDeEnum(t *testing.T) {
	casos := map[string][2]string{
		"event_type":    {`"event_type": "panic.button"`, `"event_type": "sensor.inventado"`},
		"severity":      {`"severity": "critical"`, `"severity": "urgentisimo"`},
		"source":        {`"source": "simulated"`, `"source": "produccion"`},
		"event_version": {`"event_version": "1.0"`, `"event_version": "2.0"`},
	}
	for campo, par := range casos {
		t.Run(campo, func(t *testing.T) {
			malo := strings.Replace(eventoValido, par[0], par[1], 1)
			if err := validarCrudo(t, malo); err == nil {
				t.Errorf("se acepto %s fuera del enum del contrato", campo)
			}
		})
	}
}

// Un evento minimo legitimo: sin timestamp, trace_id, correlation_id ni
// partition_key. Debe fallar tal cual y pasar despues de enriquecerlo. Esto
// fija el orden correcto del pipeline: enriquecer y luego validar.
func TestEventoMinimoRequiereEnriquecimiento(t *testing.T) {
	minimo := `{
      "event_version": "1.0",
      "event_type": "sensor.lpr",
      "event_id": "a1b2c3d4-0002-4002-8002-000000000002",
      "producer": "test-suite",
      "source": "simulated",
      "geo": {"zone": "zone_10"},
      "severity": "warning",
      "payload": {"placa_vehicular": "XYZ123", "velocidad_estimada": 95}
    }`

	if err := validarCrudo(t, minimo); err == nil {
		t.Fatal("el evento minimo deberia fallar ANTES de enriquecerse")
	}

	v, _ := NewValidator()
	doc, err := ParseDocument([]byte(minimo))
	if err != nil {
		t.Fatalf("JSON invalido: %v", err)
	}

	e := NewEnricher(PartitionByZone)
	res := e.Enrich(doc)

	if !res.AddedTimestamp || !res.AddedTraceID || !res.AddedCorrelationID || !res.AddedPartitionKey {
		t.Errorf("el enriquecedor no completo todo lo que faltaba: %+v", res.Fields())
	}
	if err := v.Validate(doc); err != nil {
		t.Fatalf("el evento enriquecido sigue sin cumplir el contrato: %v", err)
	}

	ev, err := DocumentToEvent(doc)
	if err != nil {
		t.Fatalf("no se pudo mapear a Event: %v", err)
	}
	if ev.PartitionKey != "zone_10" {
		t.Errorf("partition_key por zona: esperaba zone_10, se obtuvo %q", ev.PartitionKey)
	}
}

func TestEstrategiaDeParticionado(t *testing.T) {
	base := `{"event_type":"sensor.lpr","geo":{"zone":"zone_10"}}`

	t.Run("por zona", func(t *testing.T) {
		doc, _ := ParseDocument([]byte(base))
		NewEnricher(PartitionByZone).Enrich(doc)
		if got := doc.(map[string]any)["partition_key"]; got != "zone_10" {
			t.Errorf("esperaba zone_10, se obtuvo %v", got)
		}
	})

	t.Run("por event_type (compatibilidad con el Java)", func(t *testing.T) {
		doc, _ := ParseDocument([]byte(base))
		NewEnricher(PartitionByEventType).Enrich(doc)
		if got := doc.(map[string]any)["partition_key"]; got != "sensor.lpr" {
			t.Errorf("esperaba sensor.lpr, se obtuvo %v", got)
		}
	})
}

// El enriquecedor nunca debe pisar lo que el productor envio.
func TestEnriquecedorRespetaValoresDelProductor(t *testing.T) {
	doc, err := ParseDocument([]byte(eventoValido))
	if err != nil {
		t.Fatalf("JSON invalido: %v", err)
	}
	res := NewEnricher(PartitionByZone).Enrich(doc)
	if res.Any() {
		t.Errorf("modifico un evento completo: %v", res.Fields())
	}
	obj := doc.(map[string]any)
	if obj["trace_id"] != "c1d2e3f4-0001-4001-8001-200000000001" {
		t.Error("se sobrescribio el trace_id del productor")
	}
	if obj["partition_key"] != "zone_4" {
		t.Error("se sobrescribio el partition_key del productor")
	}
}

func TestTimestampUTC(t *testing.T) {
	doc, _ := ParseDocument([]byte(eventoValido))
	ev, err := DocumentToEvent(doc)
	if err != nil {
		t.Fatalf("no se pudo mapear: %v", err)
	}
	ts, err := ev.TimestampUTC()
	if err != nil {
		t.Fatalf("no se pudo parsear el timestamp: %v", err)
	}
	if !ts.Equal(time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("timestamp mal parseado: %v", ts)
	}
}

// Los simuladores no son consistentes: unos envian la velocidad como numero
// y otros como string. El acceso al payload debe tolerar ambos.
func TestPayloadFloatToleraNumeroYString(t *testing.T) {
	ev := &Event{Payload: map[string]any{
		"como_numero": float64(95),
		"como_string": "95",
		"no_numerico": "rapido",
	}}

	if v, ok := ev.PayloadFloat("como_numero"); !ok || v != 95 {
		t.Errorf("numero: ok=%v v=%v", ok, v)
	}
	if v, ok := ev.PayloadFloat("como_string"); !ok || v != 95 {
		t.Errorf("string numerico: ok=%v v=%v", ok, v)
	}
	if _, ok := ev.PayloadFloat("no_numerico"); ok {
		t.Error("se acepto un string no numerico como float")
	}
	if _, ok := ev.PayloadFloat("ausente"); ok {
		t.Error("se acepto una clave ausente")
	}
}
