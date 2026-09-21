package canonical

import (
	"time"

	"github.com/google/uuid"
)

// Estrategias de asignacion de partition_key.
const (
	// PartitionByZone deriva la clave de geo.zone. Es la estrategia correcta
	// segun Final-Project-Guide.md seccion 4 ("key = partition_key -> afinidad
	// por zona") y la que da sentido a la correlacion: todos los eventos de una
	// misma zona caen en la misma particion y se ordenan entre si.
	PartitionByZone = "zone"

	// PartitionByEventType reproduce el comportamiento del servicio Java de
	// referencia. Se conserva solo para poder correr ambos sistemas en paralelo
	// durante la migracion y comparar salidas evento por evento.
	//
	// Es una mala clave de particion: solo existen 5 valores posibles para 3
	// particiones, asi que el reparto queda desbalanceado y los eventos de una
	// misma zona se dispersan entre particiones, perdiendo el orden relativo.
	PartitionByEventType = "event_type"
)

// Enricher completa los campos que el contrato exige pero que el productor
// puede omitir.
//
// Corre ANTES de validar, no despues: el schema marca timestamp, trace_id
// derivados y partition_key como obligatorios, de modo que un evento minimo
// legitimo fallaria la validacion si se validara primero.
type Enricher struct {
	// PartitionKeyStrategy es PartitionByZone o PartitionByEventType.
	PartitionKeyStrategy string

	// Now y NewID se inyectan para poder escribir tests deterministas.
	Now   func() time.Time
	NewID func() string
}

// NewEnricher construye un Enricher con los valores por defecto de produccion.
func NewEnricher(strategy string) *Enricher {
	if strategy != PartitionByEventType {
		strategy = PartitionByZone
	}
	return &Enricher{
		PartitionKeyStrategy: strategy,
		Now:                  func() time.Time { return time.Now().UTC() },
		NewID:                func() string { return uuid.NewString() },
	}
}

// Result describe que campos tuvo que completar el enriquecedor.
// Se devuelve para poder registrarlo en logs y metricas: un productor que
// omite sistematicamente trace_id es un problema de instrumentacion que
// conviene ver, no esconder.
type Result struct {
	AddedTimestamp     bool
	AddedTraceID       bool
	AddedCorrelationID bool
	AddedPartitionKey  bool
}

// Any indica si el enriquecedor modifico el documento.
func (r Result) Any() bool {
	return r.AddedTimestamp || r.AddedTraceID || r.AddedCorrelationID || r.AddedPartitionKey
}

// Fields lista los campos completados, para logging estructurado.
func (r Result) Fields() []string {
	var f []string
	if r.AddedTimestamp {
		f = append(f, "timestamp")
	}
	if r.AddedTraceID {
		f = append(f, "trace_id")
	}
	if r.AddedCorrelationID {
		f = append(f, "correlation_id")
	}
	if r.AddedPartitionKey {
		f = append(f, "partition_key")
	}
	return f
}

// Enrich completa in situ los campos ausentes del documento.
//
// Respeta siempre lo que el productor envio: solo rellena lo que falta o
// viene vacio. Si el documento no es un objeto JSON lo deja intacto y deja
// que el validador reporte el error de tipo.
func (e *Enricher) Enrich(doc any) Result {
	obj, ok := doc.(map[string]any)
	if !ok {
		return Result{}
	}

	var res Result

	if isBlank(obj["timestamp"]) {
		obj["timestamp"] = e.Now().Format(time.RFC3339Nano)
		res.AddedTimestamp = true
	}
	if isBlank(obj["trace_id"]) {
		obj["trace_id"] = e.NewID()
		res.AddedTraceID = true
	}
	if isBlank(obj["correlation_id"]) {
		obj["correlation_id"] = e.NewID()
		res.AddedCorrelationID = true
	}
	if isBlank(obj["partition_key"]) {
		if key := e.partitionKey(obj); key != "" {
			obj["partition_key"] = key
			res.AddedPartitionKey = true
		}
	}

	return res
}

// partitionKey resuelve la clave de particion segun la estrategia configurada.
func (e *Enricher) partitionKey(obj map[string]any) string {
	if e.PartitionKeyStrategy == PartitionByEventType {
		if t, ok := obj["event_type"].(string); ok {
			return t
		}
		return ""
	}

	// Estrategia por zona, con event_type como respaldo: si geo.zone no vino,
	// el validador rechazara el evento de todos modos por geo faltante, pero
	// preferimos no dejar partition_key vacio para que el error que vea el
	// cliente sea el real (geo ausente) y no un segundo error derivado.
	if geo, ok := obj["geo"].(map[string]any); ok {
		if zone, ok := geo["zone"].(string); ok && zone != "" {
			return zone
		}
	}
	if t, ok := obj["event_type"].(string); ok {
		return t
	}
	return ""
}

// isBlank considera ausente tanto la clave faltante como el string vacio.
func isBlank(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}
