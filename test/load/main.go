// Command load genera carga contra el ingestor y mide su comportamiento.
//
// Cubre el requisito del enunciado de evaluar la reaccion del sistema ante
// eventos multiples simultaneos, incluyendo picos de carga (seccion 2.3). Los
// simuladores Python del proyecto original enviaban a ritmo constante con
// sleep entre lotes, asi que nunca ejercieron una rafaga.
//
// Solo usa la biblioteca estandar: no hace falta instalar k6, Artillery ni
// Node para ejecutarlo.
//
// Uso:
//
//	go run . -escenario rafaga
//	go run . -escenario sostenido -rps 20 -duracion 60s
//	go run . -escenario alertas
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	var (
		url       = flag.String("url", "http://localhost:8000", "URL base del ingestor")
		apiKey    = flag.String("api-key", "", "valor de la cabecera X-API-Key")
		escenario = flag.String("escenario", "sostenido", "sostenido | rafaga | alertas")
		rps       = flag.Int("rps", 10, "eventos por segundo en el escenario sostenido")
		duracion  = flag.Duration("duracion", 30*time.Second, "duracion del escenario sostenido")
		conc      = flag.Int("concurrencia", 50, "peticiones simultaneas maximas")
	)
	flag.Parse()

	gen := &generador{url: *url, apiKey: *apiKey, concurrencia: *conc}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var res *resultados
	switch *escenario {
	case "sostenido":
		fmt.Printf("Carga sostenida: %d eventos/s durante %s\n\n", *rps, *duracion)
		res = gen.sostenido(ctx, *rps, *duracion)
	case "rafaga":
		// La guia propone una fase de rafaga de 100 rps durante 10s para
		// comprobar que Kafka absorbe el pico sin perder eventos.
		fmt.Printf("Rafaga: calentamiento a 10 eventos/s, pico de 100 eventos/s durante 10s\n\n")
		res = gen.rafaga(ctx)
	case "alertas":
		fmt.Printf("Escenarios de correlacion: uno por cada tipo de alerta\n\n")
		res = gen.alertas(ctx)
	default:
		log.Fatalf("escenario desconocido: %q (usa sostenido, rafaga o alertas)", *escenario)
	}

	res.imprimir()

	// Salida distinta de cero si hubo errores, para poder encadenarlo en CI.
	if res.errores() > 0 {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Generador
// ---------------------------------------------------------------------------

type generador struct {
	url          string
	apiKey       string
	concurrencia int
	cliente      *http.Client
	once         sync.Once
}

func (g *generador) http() *http.Client {
	g.once.Do(func() {
		g.cliente = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        g.concurrencia * 2,
				MaxIdleConnsPerHost: g.concurrencia * 2,
				MaxConnsPerHost:     g.concurrencia * 2,
			},
		}
	})
	return g.cliente
}

// sostenido envia a ritmo constante durante un tiempo dado.
func (g *generador) sostenido(ctx context.Context, rps int, d time.Duration) *resultados {
	res := nuevosResultados()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	g.aRitmo(ctx, rps, res)
	return res
}

// rafaga calienta a ritmo bajo y luego sube de golpe a 100 eventos/s.
//
// El calentamiento importa: sin el, el primer pico mide tambien el coste de
// abrir conexiones y de que los pools se llenen, y no el comportamiento del
// sistema bajo carga.
func (g *generador) rafaga(ctx context.Context) *resultados {
	res := nuevosResultados()

	fmt.Println("  fase 1/3: calentamiento, 10 eventos/s durante 10s")
	c1, cancel1 := context.WithTimeout(ctx, 10*time.Second)
	g.aRitmo(c1, 10, res)
	cancel1()

	fmt.Println("  fase 2/3: pico, 100 eventos/s durante 10s")
	res.marcarInicioPico()
	c2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	g.aRitmo(c2, 100, res)
	cancel2()
	res.marcarFinPico()

	fmt.Println("  fase 3/3: recuperacion, 10 eventos/s durante 10s")
	c3, cancel3 := context.WithTimeout(ctx, 10*time.Second)
	g.aRitmo(c3, 10, res)
	cancel3()

	return res
}

// alertas envia las secuencias que disparan cada tipo de alerta.
func (g *generador) alertas(ctx context.Context) *resultados {
	res := nuevosResultados()

	escenarios := []struct {
		nombre  string
		zona    string
		eventos []map[string]any
	}{
		{"possible_robbery", "zona_1", secuenciaRobo("zona_1")},
		{"accident", "zona_10", secuenciaAccidente("zona_10")},
		{"fire", "zona_12", secuenciaIncendio("zona_12")},
		{"traffic_speed_violation", "zona_18", secuenciaVelocidad("zona_18")},
	}

	for _, e := range escenarios {
		fmt.Printf("  %-24s -> %s\n", e.nombre, e.zona)
		for _, ev := range e.eventos {
			if ctx.Err() != nil {
				return res
			}
			g.enviar(ctx, ev, res)
			time.Sleep(150 * time.Millisecond)
		}
	}

	fmt.Println("\n  Consultar las alertas generadas:")
	for _, e := range escenarios {
		fmt.Printf("    curl -s \"http://localhost:8080/alerts/active?zone=%s\"\n", e.zona)
	}
	fmt.Println()

	return res
}

// aRitmo mantiene un ritmo de envio acotando la concurrencia.
func (g *generador) aRitmo(ctx context.Context, rps int, res *resultados) {
	intervalo := time.Second / time.Duration(rps)
	tic := time.NewTicker(intervalo)
	defer tic.Stop()

	guardia := make(chan struct{}, g.concurrencia)
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-tic.C:
			select {
			case guardia <- struct{}{}:
			default:
				// Todas las ranuras ocupadas: el sistema no da abasto al
				// ritmo pedido. Se contabiliza en lugar de encolar sin
				// limite, que falsearia la medicion de latencia.
				res.saturado.Add(1)
				continue
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-guardia }()
				g.enviar(ctx, eventoAleatorio(), res)
			}()
		}
	}
}

// enviar hace una peticion y registra su resultado.
func (g *generador) enviar(ctx context.Context, evento map[string]any, res *resultados) {
	cuerpo, err := json.Marshal(evento)
	if err != nil {
		res.registrarError("serializacion")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url+"/events", bytes.NewReader(cuerpo))
	if err != nil {
		res.registrarError("peticion")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		req.Header.Set("X-API-Key", g.apiKey)
	}

	inicio := time.Now()
	resp, err := g.http().Do(req)
	latencia := time.Since(inicio)

	if err != nil {
		if ctx.Err() != nil {
			return // apagado en curso, no es un fallo del sistema
		}
		res.registrarError("red")
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	res.registrar(resp.StatusCode, latencia)
}

// ---------------------------------------------------------------------------
// Resultados
// ---------------------------------------------------------------------------

type resultados struct {
	mu        sync.Mutex
	latencias []time.Duration
	porCodigo map[int]int
	porError  map[string]int
	saturado  atomic.Int64
	inicio    time.Time
	picoIni   time.Time
	picoFin   time.Time
}

func nuevosResultados() *resultados {
	return &resultados{
		porCodigo: map[int]int{},
		porError:  map[string]int{},
		inicio:    time.Now(),
	}
}

func (r *resultados) registrar(codigo int, latencia time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.porCodigo[codigo]++
	r.latencias = append(r.latencias, latencia)
}

func (r *resultados) registrarError(tipo string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.porError[tipo]++
}

func (r *resultados) marcarInicioPico() { r.picoIni = time.Now() }
func (r *resultados) marcarFinPico()    { r.picoFin = time.Now() }

// errores cuenta los fallos que indican un problema real del sistema.
//
// El 409 no cuenta: significa que el evento ya habia sido ingerido, que es el
// comportamiento correcto de la deduplicacion, no un fallo.
func (r *resultados) errores() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0
	for _, c := range r.porError {
		n += c
	}
	for codigo, c := range r.porCodigo {
		if codigo >= 500 || codigo == http.StatusTooManyRequests {
			n += c
		}
	}
	return n
}

func (r *resultados) imprimir() {
	r.mu.Lock()
	defer r.mu.Unlock()

	total := 0
	for _, c := range r.porCodigo {
		total += c
	}
	transcurrido := time.Since(r.inicio)

	fmt.Println("\n─────────────────────────────────────────────")
	fmt.Printf("Peticiones      %d en %s\n", total, transcurrido.Round(time.Millisecond))
	if transcurrido > 0 {
		fmt.Printf("Ritmo efectivo  %.1f eventos/s\n", float64(total)/transcurrido.Seconds())
	}

	if !r.picoIni.IsZero() && !r.picoFin.IsZero() {
		fmt.Printf("Duracion pico   %s\n", r.picoFin.Sub(r.picoIni).Round(time.Millisecond))
	}

	fmt.Println("\nCodigos de respuesta")
	codigos := make([]int, 0, len(r.porCodigo))
	for c := range r.porCodigo {
		codigos = append(codigos, c)
	}
	sort.Ints(codigos)
	for _, c := range codigos {
		fmt.Printf("  %d  %6d  %s\n", c, r.porCodigo[c], significado(c))
	}

	if len(r.porError) > 0 {
		fmt.Println("\nErrores de transporte")
		for tipo, n := range r.porError {
			fmt.Printf("  %-14s %d\n", tipo, n)
		}
	}

	if s := r.saturado.Load(); s > 0 {
		fmt.Printf("\nSaturacion      %d envios omitidos por concurrencia agotada\n", s)
		fmt.Println("                el sistema no sostuvo el ritmo solicitado")
	}

	if len(r.latencias) > 0 {
		orden := make([]time.Duration, len(r.latencias))
		copy(orden, r.latencias)
		sort.Slice(orden, func(i, j int) bool { return orden[i] < orden[j] })

		fmt.Println("\nLatencia de ingesta")
		fmt.Printf("  p50   %s\n", percentil(orden, 50).Round(time.Microsecond))
		fmt.Printf("  p95   %s\n", percentil(orden, 95).Round(time.Microsecond))
		fmt.Printf("  p99   %s\n", percentil(orden, 99).Round(time.Microsecond))
		fmt.Printf("  max   %s\n", orden[len(orden)-1].Round(time.Microsecond))
	}

	fmt.Println("\n─────────────────────────────────────────────")
	fmt.Println("La latencia de ingesta NO es el SLO del sistema.")
	fmt.Println("El SLO es evento -> alerta, y se mide en el correlator:")
	fmt.Println("  curl -s http://localhost:8080/metrics | grep correlation_latency")
	fmt.Println("\nVerificar que no se perdio nada:")
	fmt.Println("  Kafka-UI  http://localhost:8081  (events.standardized y events.dlq)")
}

func percentil(ordenadas []time.Duration, p int) time.Duration {
	if len(ordenadas) == 0 {
		return 0
	}
	i := (len(ordenadas) * p) / 100
	if i >= len(ordenadas) {
		i = len(ordenadas) - 1
	}
	return ordenadas[i]
}

func significado(codigo int) string {
	switch codigo {
	case http.StatusAccepted:
		return "aceptado y publicado"
	case http.StatusBadRequest:
		return "no cumple el contrato"
	case http.StatusUnauthorized:
		return "falta la API key"
	case http.StatusConflict:
		return "event_id duplicado (deduplicacion correcta)"
	case http.StatusServiceUnavailable:
		return "dependencia caida"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Generacion de eventos
// ---------------------------------------------------------------------------

// Zonas reales de la Ciudad de Guatemala, con coordenadas aproximadas.
var zonas = []struct {
	nombre   string
	lat, lon float64
}{
	{"zona_1", 14.6407, -90.5133},
	{"zona_4", 14.6280, -90.5220},
	{"zona_10", 14.5995, -90.5130},
	{"zona_12", 14.5890, -90.5450},
	{"zona_18", 14.6720, -90.4710},
}

var tipos = []string{"panic.button", "sensor.lpr", "sensor.speed", "sensor.acoustic", "citizen.report"}

func eventoAleatorio() map[string]any {
	z := zonas[int(aleatorio(int64(len(zonas))))]
	tipo := tipos[int(aleatorio(int64(len(tipos))))]
	return evento(tipo, z.nombre, z.lat, z.lon, payloadDe(tipo))
}

// evento construye un evento canonico minimo.
//
// Se omiten timestamp, trace_id, correlation_id y partition_key a proposito:
// asi la carga ejercita tambien el enriquecedor del ingestor.
func evento(tipo, zona string, lat, lon float64, payload map[string]any) map[string]any {
	return map[string]any{
		"event_version": "1.0",
		"event_type":    tipo,
		"event_id":      uuidV4(),
		"producer":      "load-test",
		"source":        "simulated",
		"geo":           map[string]any{"zone": zona, "lat": lat, "lon": lon},
		"severity":      severidad(tipo),
		"payload":       payload,
	}
}

func severidad(tipo string) string {
	switch tipo {
	case "panic.button", "sensor.acoustic":
		return "critical"
	case "sensor.lpr", "sensor.speed":
		return "warning"
	default:
		return "info"
	}
}

func payloadDe(tipo string) map[string]any {
	switch tipo {
	case "panic.button":
		return map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": fmt.Sprintf("BTN-%03d", aleatorio(200)),
			"user_context":              "movil",
		}
	case "sensor.lpr":
		return map[string]any{
			"placa_vehicular":    placa(),
			"velocidad_estimada": 40 + aleatorio(70),
			"modelo_vehiculo":    "sedan",
			"color_vehiculo":     "blanco",
			"ubicacion_sensor":   fmt.Sprintf("cam_%02d", aleatorio(30)),
		}
	case "sensor.speed":
		return map[string]any{
			"velocidad_detectada": 40 + aleatorio(70),
			"sensor_id":           fmt.Sprintf("SPD-%03d", aleatorio(50)),
			"direccion":           "NORTE",
		}
	case "sensor.acoustic":
		sonidos := []string{"disparo", "explosion", "vidrio_roto"}
		return map[string]any{
			"tipo_sonido_detectado":       sonidos[int(aleatorio(int64(len(sonidos))))],
			"nivel_decibeles":             80 + aleatorio(50),
			"probabilidad_evento_critico": 0.75,
		}
	default:
		eventos := []string{"accidente", "incendio", "altercado"}
		return map[string]any{
			"tipo_evento":          eventos[int(aleatorio(int64(len(eventos))))],
			"mensaje_descriptivo":  "reporte generado por la prueba de carga",
			"ubicacion_aproximada": "via publica",
			"origen":               "app",
		}
	}
}

func secuenciaRobo(zona string) []map[string]any {
	z := coordenadas(zona)
	return []map[string]any{
		evento("panic.button", zona, z.lat, z.lon, map[string]any{
			"tipo_de_alerta": "panico", "identificador_dispositivo": "BTN-001", "user_context": "movil",
		}),
		evento("sensor.lpr", zona, z.lat, z.lon, map[string]any{
			"placa_vehicular": placa(), "velocidad_estimada": 95,
			"modelo_vehiculo": "sedan", "color_vehiculo": "negro",
		}),
	}
}

func secuenciaAccidente(zona string) []map[string]any {
	z := coordenadas(zona)
	return []map[string]any{
		evento("citizen.report", zona, z.lat, z.lon, map[string]any{
			"tipo_evento": "accidente", "mensaje_descriptivo": "vehiculo volcado", "origen": "app",
		}),
		evento("sensor.acoustic", zona, z.lat, z.lon, map[string]any{
			"tipo_sonido_detectado": "vidrio_roto", "nivel_decibeles": 110,
			"probabilidad_evento_critico": 0.9,
		}),
		// Las dos lecturas siguientes forman la caida de velocidad. En el
		// sistema Java la segunda se descartaba por el filtro de velocidad
		// previo, de modo que la caida no se detectaba nunca.
		evento("sensor.lpr", zona, z.lat, z.lon, map[string]any{
			"placa_vehicular": "P555AAA", "velocidad_estimada": 90,
		}),
		evento("sensor.lpr", zona, z.lat, z.lon, map[string]any{
			"placa_vehicular": "P555AAA", "velocidad_estimada": 25,
		}),
	}
}

func secuenciaIncendio(zona string) []map[string]any {
	z := coordenadas(zona)
	return []map[string]any{
		evento("citizen.report", zona, z.lat, z.lon, map[string]any{
			"tipo_evento": "incendio", "mensaje_descriptivo": "fuego en bodega", "origen": "usuario",
		}),
		evento("sensor.acoustic", zona, z.lat, z.lon, map[string]any{
			"tipo_sonido_detectado": "explosion", "nivel_decibeles": 125,
			"probabilidad_evento_critico": 0.95,
		}),
	}
}

func secuenciaVelocidad(zona string) []map[string]any {
	z := coordenadas(zona)
	var out []map[string]any
	for i, v := range []int{88, 96, 104} {
		out = append(out, evento("sensor.lpr", zona, z.lat, z.lon, map[string]any{
			"placa_vehicular":    fmt.Sprintf("P%03dXYZ", i+1),
			"velocidad_estimada": v,
			"ubicacion_sensor":   "autopista_norte_01",
		}))
	}
	return out
}

func coordenadas(zona string) struct {
	nombre   string
	lat, lon float64
} {
	for _, z := range zonas {
		if z.nombre == zona {
			return z
		}
	}
	return zonas[0]
}

func placa() string {
	letras := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return fmt.Sprintf("P%03d%c%c%c",
		aleatorio(1000),
		letras[aleatorio(26)], letras[aleatorio(26)], letras[aleatorio(26)])
}

// aleatorio devuelve un entero en [0,n) usando crypto/rand, que no necesita
// semilla ni comparte estado entre goroutines.
func aleatorio(n int64) int64 {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	v := int64(0)
	for _, x := range b[:7] {
		v = v<<8 | int64(x)
	}
	return v % n
}

func uuidV4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("no hay fuente de aleatoriedad disponible: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
