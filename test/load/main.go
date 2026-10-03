// Command load genera carga contra el ingestor y mide su comportamiento.
//
// Cubre el requisito del enunciado de evaluar la reaccion del sistema ante
// eventos multiples simultaneos, incluyendo picos de carga (seccion 2.3). Por
// eso no se limita a un ritmo constante: un generador que espera entre lotes
// comprueba que el sistema funciona, pero nunca ejerce una rafaga.
//
// Solo usa la biblioteca estandar: no hace falta instalar k6, Artillery ni
// Node para ejecutarlo.
//
// Uso:
//
//	go run . -escenario rafaga
//	go run . -escenario sostenido -rps 20 -duracion 60s
//	go run . -escenario alertas
//	go run . -escenario bulk
//	go run . -escenario enunciado
//	go run . -escenario tormenta -alertas 100
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	var (
		url       = flag.String("url", "http://localhost:8000", "URL base del ingestor")
		apiKey    = flag.String("api-key", "", "valor de la cabecera X-API-Key")
		escenario = flag.String("escenario", "sostenido", "sostenido | rafaga | alertas | bulk | enunciado | tormenta")
		rps       = flag.Int("rps", 10, "eventos por segundo en el escenario sostenido")
		duracion  = flag.Duration("duracion", 30*time.Second, "duracion del escenario sostenido")
		conc      = flag.Int("concurrencia", 50, "peticiones simultaneas maximas")
		lote      = flag.Int("lote", 20, "eventos por lote en el escenario bulk")
		cuantas   = flag.Int("alertas", 100, "alertas simultaneas en el escenario tormenta")
		// El correlator se publica en el host en 8001, no en 8080: dentro de
		// Docker sigue en 8080, pero ese puerto choca a menudo con otro
		// programa en las maquinas de desarrollo. Ver CORRELATOR_HOST_PORT.
		correlador = flag.String("correlator", "http://localhost:8001", "URL base del correlator, para consultarlo y leer sus metricas")
		espera     = flag.Duration("espera", 3*time.Minute, "cuanto esperar a que cierre la ventana en el escenario enunciado")
	)
	flag.Parse()

	urlCorrelator = strings.TrimSuffix(*correlador, "/")
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
	case "bulk":
		fmt.Printf("Endpoint de lotes: se comprueba que un evento invalido no invalide el lote\n\n")
		res = gen.bulk(ctx, *lote)
	case "enunciado":
		fmt.Printf("Tabla 2.4 del enunciado: los dos renglones, uno positivo y uno negativo\n\n")
		res = gen.enunciado(ctx, *correlador, *espera)
	case "tormenta":
		fmt.Printf("Tormenta: %d incidentes simultaneos sobre las zonas de la ciudad\n\n", *cuantas)
		res = gen.tormenta(ctx, *cuantas, urlCorrelator)
	default:
		log.Fatalf("escenario desconocido: %q (usa sostenido, rafaga, alertas, bulk, enunciado o tormenta)", *escenario)
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

// urlCorrelator es como se alcanza el correlator DESDE EL HOST. Lo fija main a
// partir de la bandera -correlator.
//
// Es una variable de paquete y no un campo porque las ordenes que se imprimen
// al terminar cada escenario tambien la necesitan, y repetir el puerto a mano
// en cada fmt.Println es como acaba divergiendo del despliegue: las
// instrucciones siguen imprimiendose, pero apuntan a donde ya no hay nadie.
var urlCorrelator = "http://localhost:8001"

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
		fmt.Printf("    curl -s \"%s/alerts/active?zone=%s\"\n", urlCorrelator, e.zona)
	}
	fmt.Println()

	return res
}

// enunciado reproduce literalmente la tabla 2.4 del enunciado del proyecto.
//
// Esa tabla tiene dos renglones y el segundo es el que casi nunca se prueba:
//
//	Zona 4,  22:05 | Placa XYZ123 | 85 km/h | alerta de robo en curso
//	Zona 10, 17:22 | --           | --      | falso positivo, sin complementarios
//
// El primero comprueba que el motor de correlacion dispara. El segundo
// comprueba algo mas dificil: que NO dispare, y que ademas deje constancia de
// que hubo una senal suelta que nunca encontro companera. Sin ese registro,
// una zona tranquila y una zona llena de senales incompletas son
// indistinguibles, y la tasa de falsos positivos no se puede medir.
func (g *generador) enunciado(ctx context.Context, correlator string, espera time.Duration) *resultados {
	res := nuevosResultados()
	fallos := 0

	antes, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		fmt.Printf("  no se pudieron leer las metricas del correlator: %v\n", err)
		res.registrarError("metricas")
		return res
	}

	// Una ventana con eventos previos puede completar la coincidencia que el
	// renglon 2 necesita dejar a medias. Se avisa en lugar de dar un
	// resultado enganoso.
	if v := valorDe(antes, "correlator_window_events", etq("zone", zonaNegativa)); v > 0 {
		fmt.Printf("  aviso: %s ya tiene %.0f eventos en su ventana de correlacion.\n", zonaNegativa, v)
		fmt.Printf("         Conviene esperar a que expire (2 min) para que el renglon 2 sea limpio.\n\n")
	}

	// ----------------------------------------------------------- renglon 1
	fmt.Printf("  Renglon 1 - %s, 22:05: panico + placa %s a %d km/h\n",
		zonaPositiva, placaEnunciado, velocidadEnunciado)
	fmt.Println("             esperado: alerta possible_robbery")

	for _, ev := range secuenciaEnunciadoPositiva() {
		g.enviar(ctx, ev, res)
		time.Sleep(200 * time.Millisecond)
	}
	time.Sleep(3 * time.Second)

	despues, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		fmt.Printf("             no se pudieron leer las metricas: %v\n", err)
		res.registrarError("metricas")
		return res
	}

	emitidas := delta(antes, despues, "correlator_alerts_generated_total",
		etq("type", "possible_robbery", "zone", zonaPositiva))
	suprimidas := delta(antes, despues, "correlator_alerts_suppressed_total",
		etq("type", "possible_robbery"))

	switch {
	case emitidas >= 1:
		fmt.Printf("             -> alerta emitida  [OK]\n")
	case suprimidas >= 1:
		fmt.Printf("             -> suprimida: ya habia una equivalente vigente en la zona.\n")
		fmt.Printf("                Es comportamiento correcto, pero no verifica el renglon.\n")
		fmt.Printf("                Reintentar cuando expire ALERT_SUPPRESS_TTL.\n")
	default:
		fmt.Printf("             -> NO se emitio ninguna alerta  [FALLA]\n")
		fallos++
	}

	// ----------------------------------------------------------- renglon 2
	fmt.Printf("\n  Renglon 2 - %s, 17:22: boton de panico sin eventos complementarios\n", zonaNegativa)
	fmt.Println("             esperado: ninguna alerta, y un candidato a falso positivo")

	base, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		res.registrarError("metricas")
		return res
	}

	for _, ev := range secuenciaEnunciadoNegativa() {
		g.enviar(ctx, ev, res)
	}
	time.Sleep(3 * time.Second)

	tras, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		res.registrarError("metricas")
		return res
	}

	if n := delta(base, tras, "correlator_alerts_generated_total",
		etq("type", "possible_robbery", "zone", zonaNegativa)); n > 0 {
		fmt.Printf("             -> se emitieron %.0f alertas; deberia no emitirse ninguna  [FALLA]\n", n)
		fmt.Printf("                Probablemente la ventana de %s no estaba limpia.\n", zonaNegativa)
		fallos++
	} else {
		fmt.Println("             -> ninguna alerta emitida  [OK]")
	}

	if espera <= 0 {
		fmt.Println("             -> falso positivo pendiente de confirmar (se omitio la espera)")
		return g.cerrarEnunciado(res, fallos)
	}

	// El falso positivo no se confirma al instante: hace falta que la ventana
	// de la regla se cierre (2 min) y que pase el barrido periodico (30 s).
	fmt.Printf("             -> esperando a que cierre la ventana de correlacion (hasta %s)", espera)

	limite := time.Now().Add(espera)
	confirmado := false
	for time.Now().Before(limite) {
		select {
		case <-ctx.Done():
			fmt.Println()
			return g.cerrarEnunciado(res, fallos)
		case <-time.After(15 * time.Second):
		}
		fmt.Print(".")

		ahora, err := g.leerMuestras(ctx, correlator)
		if err != nil {
			continue
		}
		if delta(base, ahora, "correlator_false_positive_candidates_total",
			etq("rule", "possible_robbery", "zone", zonaNegativa)) >= 1 {
			confirmado = true
			break
		}
	}
	fmt.Println()

	if confirmado {
		fmt.Println("             -> falso positivo confirmado y contabilizado  [OK]")
	} else {
		fmt.Println("             -> la ventana no se cerro dentro del tiempo de espera  [FALLA]")
		fmt.Println("                Subir -espera, o revisar el barrido del correlator.")
		fallos++
	}

	return g.cerrarEnunciado(res, fallos)
}

func (g *generador) cerrarEnunciado(res *resultados, fallos int) *resultados {
	fmt.Println()
	if fallos == 0 {
		fmt.Println("  los dos renglones de la tabla del enunciado se comportan como dice")
	} else {
		fmt.Printf("  %d comprobaciones fallaron\n", fallos)
		res.registrarError("correlacion_enunciado")
	}
	fmt.Println()
	return res
}

// Valores tomados literalmente de la tabla 2.4 del enunciado.
const (
	zonaPositiva       = "zona_4"
	zonaNegativa       = "zona_10"
	placaEnunciado     = "XYZ123"
	velocidadEnunciado = 85
)

func secuenciaEnunciadoPositiva() []map[string]any {
	z := coordenadas(zonaPositiva)
	return []map[string]any{
		evento("panic.button", zonaPositiva, z.lat, z.lon, map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": "BTN-Z4-001",
			"user_context":              "movil",
		}),
		evento("sensor.lpr", zonaPositiva, z.lat, z.lon, map[string]any{
			"placa_vehicular":    placaEnunciado,
			"velocidad_estimada": velocidadEnunciado,
			"modelo_vehiculo":    "sedan",
			"color_vehiculo":     "gris",
		}),
	}
}

// secuenciaEnunciadoNegativa es un solo evento a proposito: es justamente la
// ausencia de complementarios lo que el renglon 2 comprueba.
func secuenciaEnunciadoNegativa() []map[string]any {
	z := coordenadas(zonaNegativa)
	return []map[string]any{
		evento("panic.button", zonaNegativa, z.lat, z.lon, map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": "BTN-Z10-001",
			"user_context":              "movil",
		}),
	}
}

// ---------------------------------------------------------------------------
// Lectura de metricas del correlator
//
// Se leen del endpoint /metrics y no de la API de alertas porque /metrics no
// exige credenciales y porque los contadores permiten medir por diferencia,
// que es lo unico fiable cuando el sistema lleva rato en marcha.
// ---------------------------------------------------------------------------

type muestra struct {
	nombre    string
	etiquetas map[string]string
	valor     float64
}

func (g *generador) leerMuestras(ctx context.Context, base string) ([]muestra, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("%s/metrics devolvio %d", base, resp.StatusCode)
	}

	datos, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parsearExposicion(string(datos)), nil
}

func parsearExposicion(texto string) []muestra {
	var ms []muestra
	for _, linea := range strings.Split(texto, "\n") {
		linea = strings.TrimSpace(linea)
		if linea == "" || strings.HasPrefix(linea, "#") {
			continue
		}
		corte := strings.LastIndex(linea, " ")
		if corte < 0 {
			continue
		}
		valor, err := strconv.ParseFloat(linea[corte+1:], 64)
		if err != nil {
			continue
		}

		m := muestra{etiquetas: map[string]string{}}
		izq := linea[:corte]
		if i := strings.Index(izq, "{"); i >= 0 {
			m.nombre = izq[:i]
			for _, par := range strings.Split(strings.TrimSuffix(izq[i+1:], "}"), ",") {
				if j := strings.Index(par, "="); j > 0 {
					m.etiquetas[strings.TrimSpace(par[:j])] = strings.Trim(par[j+1:], `"`)
				}
			}
		} else {
			m.nombre = izq
		}
		m.valor = valor
		ms = append(ms, m)
	}
	return ms
}

func etq(pares ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(pares); i += 2 {
		m[pares[i]] = pares[i+1]
	}
	return m
}

func valorDe(ms []muestra, nombre string, etiquetas map[string]string) float64 {
	total := 0.0
	for _, m := range ms {
		if m.nombre != nombre {
			continue
		}
		coincide := true
		for k, v := range etiquetas {
			if m.etiquetas[k] != v {
				coincide = false
				break
			}
		}
		if coincide {
			total += m.valor
		}
	}
	return total
}

// delta mide por diferencia. Un contador acumulado no dice nada por si solo
// cuando el sistema lleva horas en marcha: lo que importa es cuanto subio.
func delta(antes, despues []muestra, nombre string, etiquetas map[string]string) float64 {
	return valorDe(despues, nombre, etiquetas) - valorDe(antes, nombre, etiquetas)
}

// tormenta dispara N incidentes a la vez, cada uno en su propia zona.
//
// Responde a "¿que pasa si estallan cien incidentes simultaneos?".
//
// La respuesta honesta no es "cien alertas". El correlator deduplica por zona
// y tipo: dos incidentes del mismo tipo en la misma zona son, para el, el
// mismo incidente, y el segundo se suprime. Con las zonas reales de la ciudad
// el techo de alertas distintas es zonas x tipos, y por encima de ahi todo lo
// demas se suprime.
//
// Inventar una zona por incidente daria el numero redondo que uno espera, y
// seria mentira: ensuciaria las metricas y el archivo con zonas inexistentes,
// y presentaria como capacidad lo que en realidad es ausencia de
// deduplicacion. Por eso el escenario usa las zonas que la ciudad tiene y
// reporta las dos cifras: cuantas alertas distintas salieron y cuantas se
// suprimieron.
func (g *generador) tormenta(ctx context.Context, cuantas int, correlator string) *resultados {
	res := nuevosResultados()
	if cuantas < 1 {
		cuantas = 1
	}

	antes, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		fmt.Printf("  no se pudieron leer las metricas del correlator: %v\n", err)
		res.registrarError("metricas")
		return res
	}

	tipos := []struct {
		nombre    string
		secuencia func(string) []map[string]any
	}{
		{"possible_robbery", secuenciaRobo},
		{"accident", secuenciaAccidente},
		{"fire", secuenciaIncendio},
		{"traffic_speed_violation", secuenciaVelocidad},
	}

	// Cada incidente ocupa una combinacion (zona, tipo) distinta, y ese
	// producto es el techo de alertas DISTINTAS que el sistema puede emitir:
	// para el correlator, dos incidentes del mismo tipo en la misma zona son
	// el mismo incidente, y el segundo se suprime.
	combinaciones := len(zonas) * len(tipos)
	esperadas := cuantas
	if esperadas > combinaciones {
		esperadas = combinaciones
	}

	fmt.Printf("  %d zonas x %d tipos = %d alertas distintas posibles\n",
		len(zonas), len(tipos), combinaciones)
	if cuantas > combinaciones {
		fmt.Printf("  se lanzan %d incidentes, asi que al menos %d repetiran\n",
			cuantas, cuantas-combinaciones)
		fmt.Println("  combinacion y quedaran suprimidos. Es el comportamiento correcto.")
	}

	reparto := map[string]int{}
	for i := 0; i < esperadas; i++ {
		reparto[tipos[i%len(tipos)].nombre]++
	}
	fmt.Printf("\n  lanzando %d secuencias en paralelo...\n", cuantas)

	inicio := time.Now()
	guardia := make(chan struct{}, g.concurrencia)
	var wg sync.WaitGroup

	for i := 0; i < cuantas; i++ {
		if ctx.Err() != nil {
			break
		}
		// Se recorre el producto (zona, tipo) para agotar las combinaciones
		// distintas antes de repetir ninguna.
		zona := zonas[(i/len(tipos))%len(zonas)].nombre
		eventos := tipos[i%len(tipos)].secuencia(zona)

		guardia <- struct{}{}
		wg.Add(1)
		go func(evs []map[string]any) {
			defer wg.Done()
			defer func() { <-guardia }()
			// Dentro de una zona los eventos van EN ORDEN y separados en el
			// tiempo: la caida de velocidad del accidente es una transicion
			// entre lecturas consecutivas, y sin orden no existe.
			for _, ev := range evs {
				g.enviar(ctx, ev, res)
				time.Sleep(60 * time.Millisecond)
			}
		}(eventos)
	}
	wg.Wait()
	envio := time.Since(inicio)

	fmt.Printf("  enviadas en %s; esperando a que el correlator termine...\n", envio.Round(time.Millisecond))
	select {
	case <-ctx.Done():
		return res
	case <-time.After(10 * time.Second):
	}

	despues, err := g.leerMuestras(ctx, correlator)
	if err != nil {
		res.registrarError("metricas")
		return res
	}

	emitidas := delta(antes, despues, "correlator_alerts_generated_total", nil)
	suprimidas := delta(antes, despues, "correlator_alerts_suppressed_total", nil)

	fmt.Println("")
	fmt.Println("  Resultado")
	fmt.Printf("    incidentes lanzados     %d\n", cuantas)
	fmt.Printf("    alertas distintas       %.0f  (de %d combinaciones posibles)\n",
		emitidas, combinaciones)
	fmt.Printf("    suprimidas              %.0f\n", suprimidas)
	fmt.Println("")
	for _, t := range tipos {
		n := delta(antes, despues, "correlator_alerts_generated_total", etq("type", t.nombre))
		if reparto[t.nombre] > 0 {
			fmt.Printf("    %-24s %3.0f de %d\n", t.nombre, n, reparto[t.nombre])
		}
	}

	fmt.Println("")
	switch {
	case emitidas >= float64(esperadas):
		fmt.Printf("  Se emitio una alerta por cada combinacion zona-tipo afectada: %.0f.\n", emitidas)
		fmt.Println("  Ningun incidente se perdio.")
	case emitidas == 0:
		fmt.Println("  No se emitio ninguna: TODAS quedaron suprimidas. Las zonas ya tenian")
		fmt.Println("  una alerta vigente. Esperar a que expire ALERT_SUPPRESS_TTL (5 min")
		fmt.Println("  por defecto) y repetir.")
	default:
		fmt.Printf("  Se emitieron %.0f de las %d esperadas. Si se repitio el escenario\n",
			emitidas, esperadas)
		fmt.Println("  hace menos de 5 minutos, el resto sigue suprimido del intento anterior.")
	}

	// Las dos cifras miden cosas distintas y conviene no confundirlas: un
	// numero alto de supresiones aqui NO significa alertas perdidas.
	if suprimidas > 0 {
		fmt.Println("")
		fmt.Println("  Las supresiones NO son incidentes perdidos. Son de dos clases:")
		fmt.Println("    - re-disparos dentro del mismo incidente: una secuencia de cuatro")
		fmt.Println("      eventos cumple su regla al segundo, y los dos siguientes vuelven")
		fmt.Println("      a cumplirla;")
		fmt.Println("    - incidentes que caen en una zona que ya tenia una alerta de ese")
		fmt.Println("      tipo vigente.")
		fmt.Println("  En ambos casos es lo que se busca: que un incidente en curso genere")
		fmt.Println("  un aviso y no una cadena de avisos identicos.")
	}
	fmt.Println("")

	return res
}

// bulk ejercita POST /events/bulk.
//
// No mide volumen, comprueba el contrato. La promesa del endpoint es que un
// evento invalido no invalide el lote entero y que la respuesta diga
// exactamente cual fallo, en que indice y por que. Un generador que solo
// enviara lotes sin mirar la respuesta dejaria esa promesa sin verificar
// contra el sistema levantado.
func (g *generador) bulk(ctx context.Context, tam int) *resultados {
	res := nuevosResultados()

	// Se repite a proposito dentro de un mismo lote: el segundo debe
	// rechazarse por duplicado sin arrastrar a los demas.
	repetido := eventoAleatorio()

	casos := []struct {
		nombre  string
		eventos []map[string]any
		espera  []string
	}{
		{
			nombre:  fmt.Sprintf("lote limpio de %d eventos", tam),
			eventos: loteValido(tam),
			espera:  repetir("accepted", tam),
		},
		{
			nombre: "lote mixto: validos e invalidos entremezclados",
			eventos: []map[string]any{
				eventoAleatorio(),
				sinCampo(eventoAleatorio(), "severity"),
				eventoAleatorio(),
				conCampoExtra(eventoAleatorio()),
				conTipoInvalido(eventoAleatorio()),
				eventoAleatorio(),
			},
			espera: []string{
				"accepted", "rejected_contract", "accepted",
				"rejected_contract", "rejected_contract", "accepted",
			},
		},
		{
			nombre:  "duplicado dentro del mismo lote",
			eventos: []map[string]any{repetido, eventoAleatorio(), repetido},
			espera:  []string{"accepted", "accepted", "rejected_duplicate"},
		},
	}

	fallos := 0
	for _, c := range casos {
		fmt.Printf("  %s\n", c.nombre)
		r, err := g.enviarLote(ctx, c.eventos, res)
		if err != nil {
			fmt.Printf("      no se pudo enviar: %v\n", err)
			fallos++
			continue
		}
		fallos += comprobarLote(r, c.espera)
	}

	// Un cuerpo mal formado no es un lote con eventos invalidos: es una
	// peticion invalida, y debe responder 400 y no 202.
	fmt.Println("  cuerpo que no es un arreglo JSON")
	if codigo := g.enviarCrudo(ctx, []byte(`{"event_type":"panic.button"}`), res); codigo != http.StatusBadRequest {
		fmt.Printf("      esperaba 400, se obtuvo %d\n", codigo)
		fallos++
	} else {
		fmt.Println("      400, como corresponde")
	}

	fmt.Println()
	if fallos == 0 {
		fmt.Println("  el endpoint se comporta como promete el contrato")
	} else {
		fmt.Printf("  %d comprobaciones fallaron\n", fallos)
		res.registrarError("contrato_bulk")
	}
	fmt.Println()

	return res
}

// respuestaBulk es la parte del cuerpo de /events/bulk que interesa aqui.
type respuestaBulk struct {
	Total      int `json:"total"`
	Aceptados  int `json:"accepted"`
	Rechazados int `json:"rejected"`
	Resultados []struct {
		Indice  int    `json:"index"`
		Estado  string `json:"status"`
		EventID string `json:"event_id"`
		Mensaje string `json:"message"`
	} `json:"results"`
}

// comprobarLote contrasta el resultado de cada indice con lo esperado y
// devuelve cuantos no coincidieron.
func comprobarLote(r *respuestaBulk, espera []string) int {
	if len(r.Resultados) != len(espera) {
		fmt.Printf("      el lote devolvio %d resultados, se esperaban %d\n",
			len(r.Resultados), len(espera))
		return len(espera)
	}

	fallos := 0
	for i, quiero := range espera {
		if tengo := r.Resultados[i].Estado; tengo != quiero {
			fmt.Printf("      indice %d: esperaba %q, se obtuvo %q (%s)\n",
				i, quiero, tengo, r.Resultados[i].Mensaje)
			fallos++
		}
	}
	if fallos == 0 {
		fmt.Printf("      %d aceptados, %d rechazados, cada uno en su indice\n",
			r.Aceptados, r.Rechazados)
	}
	return fallos
}

// enviarLote publica el lote y devuelve su respuesta ya interpretada.
func (g *generador) enviarLote(ctx context.Context, eventos []map[string]any, res *resultados) (*respuestaBulk, error) {
	cuerpo, err := json.Marshal(eventos)
	if err != nil {
		res.registrarError("serializacion")
		return nil, err
	}

	datos, codigo, err := g.postLote(ctx, cuerpo, res)
	if err != nil {
		return nil, err
	}
	// Un lote con eventos invalidos sigue siendo un lote aceptado: el 202 es
	// parte de la promesa, y un 400 aqui significaria que un elemento tumbo
	// el lote entero.
	if codigo != http.StatusAccepted {
		return nil, fmt.Errorf("el lote devolvio %d, se esperaba 202", codigo)
	}

	var r respuestaBulk
	if err := json.Unmarshal(datos, &r); err != nil {
		return nil, fmt.Errorf("respuesta ilegible: %w", err)
	}
	return &r, nil
}

func (g *generador) enviarCrudo(ctx context.Context, cuerpo []byte, res *resultados) int {
	_, codigo, _ := g.postLote(ctx, cuerpo, res)
	return codigo
}

func (g *generador) postLote(ctx context.Context, cuerpo []byte, res *resultados) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url+"/events/bulk", bytes.NewReader(cuerpo))
	if err != nil {
		res.registrarError("peticion")
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		req.Header.Set("X-API-Key", g.apiKey)
	}

	inicio := time.Now()
	resp, err := g.http().Do(req)
	latencia := time.Since(inicio)
	if err != nil {
		res.registrarError("red")
		return nil, 0, err
	}
	defer resp.Body.Close()

	datos, err := io.ReadAll(resp.Body)
	if err != nil {
		res.registrarError("lectura")
		return nil, resp.StatusCode, err
	}

	res.registrar(resp.StatusCode, latencia)
	return datos, resp.StatusCode, nil
}

func loteValido(n int) []map[string]any {
	l := make([]map[string]any, n)
	for i := range l {
		l[i] = eventoAleatorio()
	}
	return l
}

func repetir(v string, n int) []string {
	s := make([]string, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// sinCampo quita un campo que el contrato declara obligatorio.
func sinCampo(ev map[string]any, campo string) map[string]any {
	delete(ev, campo)
	return ev
}

// conCampoExtra anade un campo que el contrato no declara. El schema cierra el
// objeto con additionalProperties:false, de modo que debe rechazarse en lugar
// de ignorarse en silencio, que es lo que hace la mayoria de deserializadores.
func conCampoExtra(ev map[string]any) map[string]any {
	ev["campo_que_no_existe"] = "valor"
	return ev
}

// conTipoInvalido pone un tipo de evento fuera del enumerado del contrato.
func conTipoInvalido(ev map[string]any) map[string]any {
	ev["event_type"] = "sensor.inexistente"
	return ev
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
	fmt.Printf("  curl -s %s/metrics | grep correlation_latency\n", urlCorrelator)
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
// zonas son las de la Ciudad de Guatemala. La lista es fija a proposito: una
// ciudad tiene las zonas que tiene, y un generador que invente nombres nuevos
// en cada ejecucion ensucia las metricas, los tableros y el archivo historico
// con zonas que no existen.
//
// Las coordenadas son APROXIMADAS, suficientes para situar los eventos en el
// mapa de calor. El equipo debe sustituirlas por las oficiales antes de
// presentar el informe, junto con el resto de datos a verificar del Anexo C.
var zonas = []struct {
	nombre   string
	lat, lon float64
}{
	{"zona_1", 14.6407, -90.5133},
	{"zona_2", 14.6530, -90.5160},
	{"zona_3", 14.6480, -90.5290},
	{"zona_4", 14.6280, -90.5220},
	{"zona_5", 14.6330, -90.5050},
	{"zona_6", 14.6560, -90.4950},
	{"zona_7", 14.6200, -90.5560},
	{"zona_8", 14.6180, -90.5340},
	{"zona_9", 14.6050, -90.5210},
	{"zona_10", 14.5995, -90.5130},
	{"zona_11", 14.6030, -90.5440},
	{"zona_12", 14.5890, -90.5450},
	{"zona_13", 14.5830, -90.5260},
	{"zona_14", 14.5810, -90.5090},
	{"zona_15", 14.5900, -90.4860},
	{"zona_16", 14.6120, -90.4620},
	{"zona_17", 14.6450, -90.4600},
	{"zona_18", 14.6720, -90.4710},
	{"zona_19", 14.6630, -90.4850},
	{"zona_21", 14.5700, -90.5330},
	{"zona_24", 14.5620, -90.5120},
	{"zona_25", 14.5760, -90.4960},
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
// dentroDeLaZona dispersa un punto alrededor del centroide recibido.
//
// Un sensor esta en una esquina concreta, no en el centro geometrico de la
// zona. Sin esta dispersion todos los eventos de una zona comparten
// coordenada exacta: en el mapa caen en el mismo pixel y un panel no puede
// distinguir una zona con un incidente de otra con treinta, porque los puntos
// se tapan entre si. El radio es el orden de magnitud de una zona capitalina.
//
// La correccion por coseno de la latitud evita que la dispersion quede
// achatada: un grado de longitud mide menos que uno de latitud, y mas cuanto
// mas lejos del ecuador.
func dentroDeLaZona(lat, lon float64) (float64, float64) {
	const radio = 0.008 // grados, unos 890 m

	dLat := radio * (2*fraccionAleatoria() - 1)
	dLon := radio * (2*fraccionAleatoria() - 1) / math.Cos(lat*math.Pi/180)

	return redondear(lat + dLat), redondear(lon + dLon)
}

// redondear deja seis decimales, que son unos 11 cm: mas precision seria
// fingir una exactitud que un sensor urbano no tiene.
func redondear(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// fraccionAleatoria devuelve un valor en [0,1). Usa crypto/rand porque es el
// generador que el programa ya tiene importado para los UUID; aqui no hace
// falta calidad criptografica, pero tampoco cuesta nada.
func fraccionAleatoria() float64 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint32(b[:])) / (1 << 32)
}

func evento(tipo, zona string, lat, lon float64, payload map[string]any) map[string]any {
	plat, plon := dentroDeLaZona(lat, lon)
	return map[string]any{
		"event_version": "1.0",
		"event_type":    tipo,
		"event_id":      uuidV4(),
		"producer":      "load-test",
		"source":        "simulated",
		"geo":           map[string]any{"zone": zona, "lat": plat, "lon": plon},
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
		// Las dos lecturas siguientes forman la caida de velocidad. Si la
		// regla filtrara antes por velocidad alta, la segunda desapareceria
		// y la caida no podria detectarse nunca.
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
