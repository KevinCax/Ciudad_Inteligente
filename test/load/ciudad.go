package main

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"os"
	"sync"
	"time"
)

// Simulacion de la ciudad en tiempo real.
//
// Hasta ahora cada escenario lanzaba un puñado de eventos y terminaba. Esto es
// otra cosa: un mundo que sigue funcionando. Hay vehiculos que salen de un punto
// y llegan a otro por las calles de verdad, y las camaras de la red fija los
// leen cuando pasan por delante, a la hora a la que pasan. Hay hora pico y hora
// valle. Y de vez en cuando, sin que nadie lo ordene, ocurre algo: un robo con
// huida, un accidente, un incendio, un boton pulsado por error.
//
// Corre en tiempo real y a ritmo 1:1, a proposito. Acelerar el reloj no es
// posible sin falsear el resultado: la supresion de alertas repetidas se mide en
// tiempo de pared, de modo que dos robos separados por veinte minutos
// SIMULADOS pero por doce segundos REALES se tomarian por el mismo incidente.
// Un sistema en produccion tampoco admite que se le acelere el tiempo.
//
// LIMITES, para no confundirlo con la realidad: es una simulacion. Los eventos
// llevan source "simulated", unico valor que admite el contrato. La red de
// sensores es inventada, aunque este sobre calles reales. Y la frecuencia de
// incidentes es plausible, no medida: no hay estadisticas de la Municipalidad
// de las que partir.

// zonaGT es la hora de Guatemala. No tiene horario de verano.
var zonaGT = time.FixedZone("GT", -6*3600)

// curvaHoraria es la actividad relativa de cada hora del dia, en hora local.
//
// Un valor de 1.0 es el trafico de media jornada. La forma —dos picos, de
// 7 a 8 de la manana y de 17 a 18 de la tarde, y un valle profundo de madrugada—
// es la de cualquier ciudad con trabajo de oficina; las cifras son plausibles,
// no medidas.
var curvaHoraria = [24]float64{
	0.25, 0.18, 0.12, 0.10, 0.18, 0.55, // 00 - 05
	1.20, 1.90, 1.70, 1.20, 1.00, 1.00, // 06 - 11
	1.20, 1.20, 1.00, 1.10, 1.40, 1.90, // 12 - 17
	1.80, 1.20, 0.80, 0.55, 0.40, 0.30, // 18 - 23
}

// desfaseCurva adelanta o atrasa la curva de actividad (flag -hora).
//
// Solo mueve la CURVA, no el reloj: los eventos siguen llevando la hora real
// de generacion. Sirve para ver la hora pico a las once de la noche sin tener
// que falsear ninguna marca de tiempo, que es lo que el sistema comprueba.
var desfaseCurva time.Duration

// factorHora interpola la curva entre horas, para que no haya saltos a las en punto.
func factorHora(t time.Time) float64 {
	l := t.Add(desfaseCurva).In(zonaGT)
	h := float64(l.Hour()) + float64(l.Minute())/60 + float64(l.Second())/3600
	i := int(h)
	f := h - float64(i)
	return curvaHoraria[i%24]*(1-f) + curvaHoraria[(i+1)%24]*f
}

// congestion es el multiplicador de velocidad segun la actividad: en hora pico
// se circula a dos tercios de lo normal, y de madrugada algo mas rapido.
func congestion(factor float64) float64 {
	return math.Min(1.15, math.Max(0.55, 1.15-0.27*factor))
}

// vehiculo es un coche del parque automotor simulado.
type vehiculo struct {
	placa, modelo, color string

	// agresividad multiplica la velocidad a la que circula respecto a lo normal.
	agresividad float64
}

// evProgramado es un evento que ocurrira en un instante futuro.
type evProgramado struct {
	at   time.Time
	tipo string
	ev   map[string]any
}

// colaEv es una cola de prioridad por instante.
type colaEv []evProgramado

func (q colaEv) Len() int           { return len(q) }
func (q colaEv) Less(i, j int) bool { return q[i].at.Before(q[j].at) }
func (q colaEv) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *colaEv) Push(x any)        { *q = append(*q, x.(evProgramado)) }
func (q *colaEv) Pop() any {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[:n-1]
	return x
}

// evTag es lo que la simulacion recuerda de un evento que genero.
type evTag struct {
	ID         string  `json:"id"`
	Tipo       string  `json:"tipo"`
	Zona       string  `json:"zona"`
	Detalle    string  `json:"detalle,omitempty"` // sonido, tipo de reporte o tipo de alerta
	Vel        float64 `json:"vel,omitempty"`
	Db         float64 `json:"db,omitempty"`
	Infraccion bool    `json:"infraccion,omitempty"`
}

// verdad es lo que la simulacion SABE de un incidente que genero.
//
// Es la diferencia entre probar y medir. Con datos reales nadie sabe cuantos
// robos hubo, de modo que no se puede decir cuantos detecto el sistema. Aqui si:
// se anota que se genero, cuando y con que eventos, y al terminar un evaluador
// lo contrasta con lo que el sistema decidio por su cuenta.
//
// El sistema NO ve esta informacion. Se escribe en un archivo aparte, no viaja en
// ningun evento: un detector que supiera la respuesta no demostraria nada.
type verdad struct {
	ID     string    `json:"id"`
	Clase  string    `json:"clase"`
	Zona   string    `json:"zona"`
	Inicio time.Time `json:"inicio"`
	Placa  string    `json:"placa,omitempty"`

	// Solo en la simulacion controlada: cada caso dice que alertas deberia
	// producir, y si lo que se espera es un limite conocido del sistema.
	Caso     string   `json:"caso,omitempty"`
	Esperado []string `json:"esperado"`
	Limite   string   `json:"limite,omitempty"`
	Nota     string   `json:"nota,omitempty"`

	Eventos []evTag `json:"eventos"`
}

// incidente es un proceso que ocurre al azar, con una frecuencia media.
type incidente struct {
	nombre  string
	porHora float64
	hacer   func(time.Time)
	prox    time.Time
}

type ciudad struct {
	g   *generador
	res *resultados
	rng *mrand.Rand

	lpr, rad, acu, btn []sensor
	flota              []vehiculo
	pesos              []float64 // pesos acumulados por zona

	cola colaEv

	programados map[string]int
	enviados    map[string]int
	incidentes  map[string]int
	perdidos    int

	// etiq es el incidente que se esta generando en este instante, si hay uno.
	// Todo evento que se programe mientras tanto queda anotado como suyo.
	etiq       *verdad
	verdades   []verdad
	rutaVerdad string

	// Trafico de fondo, en vehiculos por minuto a actividad 1.0.
	porMinuto float64

	// Solo la simulacion controlada: zonas ya ocupadas por algun caso, para que
	// un pulsador de un caso no se cruce con un coche rapido de otro.
	usadas         map[string]bool
	usadasAcu      map[string]bool
	ultimoEtiq     time.Time
	sitioAccidente *sensor
}

func nuevaCiudad(g *generador, res *resultados, cat *catalogo, semilla uint64) *ciudad {
	c := &ciudad{
		g: g, res: res,
		rng:         mrand.New(mrand.NewPCG(semilla^0xc1ed, semilla+7)),
		lpr:         cat.deTipo("lpr"),
		rad:         cat.deTipo("velocidad"),
		acu:         cat.deTipo("acustico"),
		btn:         cat.deTipo("panico"),
		programados: map[string]int{},
		enviados:    map[string]int{},
		incidentes:  map[string]int{},
		porMinuto:   42,
		usadas:      map[string]bool{},
		usadasAcu:   map[string]bool{},
	}

	// Las zonas del centro generan y reciben mas viajes que las periferias.
	centrales := map[string]bool{
		"zona_1": true, "zona_4": true, "zona_5": true, "zona_9": true,
		"zona_10": true, "zona_11": true, "zona_12": true, "zona_13": true,
	}
	acum := 0.0
	for _, z := range zonas {
		if centrales[z.nombre] {
			acum += 3
		} else {
			acum++
		}
		c.pesos = append(c.pesos, acum)
	}

	c.generarFlota(4000)
	return c
}

// generarFlota crea el parque de vehiculos. Los mismos coches repiten viaje, como
// los de quien va a trabajar cada dia, y por eso una misma placa aparece varias
// veces en la red.
func (c *ciudad) generarFlota(n int) {
	const letras = "BCDFGHJKLMNPRSTVWXYZ" // sin vocales, como en las placas reales
	modelos := []string{"sedan", "pickup", "SUV", "hatchback", "microbus", "camion liviano"}
	colores := []string{"blanco", "negro", "gris", "rojo", "azul", "plata", "verde"}

	vistos := map[string]bool{}
	for len(c.flota) < n {
		prefijo, modelo := "P", modelos[c.rng.IntN(len(modelos))]
		switch u := c.rng.Float64(); {
		case u > 0.90:
			prefijo, modelo = "M", "moto"
		case u > 0.82:
			prefijo = "C"
		}

		placa := fmt.Sprintf("%s%03d%c%c%c", prefijo, c.rng.IntN(1000),
			letras[c.rng.IntN(len(letras))], letras[c.rng.IntN(len(letras))], letras[c.rng.IntN(len(letras))])
		if vistos[placa] {
			continue
		}
		vistos[placa] = true

		// La mayoria conduce a lo normal; unos pocos, bastante mas rapido.
		agr := math.Min(1.2, math.Max(0.75, 1+c.rng.NormFloat64()*0.10))
		if c.rng.Float64() < 0.04 {
			agr = 1.3 + c.rng.Float64()*0.15
		}

		c.flota = append(c.flota, vehiculo{
			placa: placa, modelo: modelo, color: colores[c.rng.IntN(len(colores))],
			agresividad: agr,
		})
	}
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

func (c *ciudad) u() float64 { return c.rng.Float64() }

func (c *ciudad) entre(a, b float64) float64 { return a + (b-a)*c.u() }

// exp devuelve el tiempo hasta el siguiente suceso de un proceso de Poisson
// con la frecuencia dada, en sucesos por minuto.
func (c *ciudad) exp(porMinuto float64) time.Duration {
	if porMinuto <= 0 {
		return time.Hour
	}
	return time.Duration(-math.Log(1-c.u()) / porMinuto * float64(time.Minute))
}

func (c *ciudad) zonaPonderada() string {
	x := c.u() * c.pesos[len(c.pesos)-1]
	for i, p := range c.pesos {
		if x <= p {
			return zonas[i].nombre
		}
	}
	return zonas[len(zonas)-1].nombre
}

// puntoEn devuelve un punto al azar a poco mas de un kilometro del centro de la
// zona. El motor de rutas lo lleva a la calle mas cercana.
func (c *ciudad) puntoEn(zona string) [2]float64 {
	z := coordenadasDe(zona)
	return [2]float64{z[0] + c.entre(-0.012, 0.012), z[1] + c.entre(-0.012, 0.012)}
}

// cerca desplaza un punto hasta unos metros, en cualquier direccion.
func (c *ciudad) cerca(lat, lon, metros float64) (float64, float64) {
	return lat + c.entre(-1, 1)*metros/110574,
		lon + c.entre(-1, 1)*metros/(111320*math.Cos(lat*math.Pi/180))
}

func (c *ciudad) vehiculoAlAzar() vehiculo { return c.flota[c.rng.IntN(len(c.flota))] }

// camaraCerca devuelve la camara LPR mas proxima dentro de un radio.
func (c *ciudad) camaraCerca(lat, lon, km float64) (sensor, bool) {
	var mejor sensor
	mejorKm, hay := km, false
	for _, s := range c.lpr {
		if d := distanciaRectaKm([2]float64{lat, lon}, [2]float64{s.Lat, s.Lon}); d < mejorKm {
			mejor, mejorKm, hay = s, d, true
		}
	}
	return mejor, hay
}

// leerPlaca devuelve lo que "ve" la camara. Rara vez se equivoca de caracter, que
// es lo que hace un lector automatico de placas: genera una matricula que no
// existe, o que es la de otro coche.
func (c *ciudad) leerPlaca(p string) string {
	if c.u() >= 0.005 {
		return p
	}
	b := []byte(p)
	i := 1 + c.rng.IntN(len(b)-1)
	if b[i] >= '0' && b[i] <= '9' {
		b[i] = byte('0' + c.rng.IntN(10))
	} else {
		b[i] = "BCDFGHJKLMNPRSTVWXYZ"[c.rng.IntN(20)]
	}
	return string(b)
}

func cardinal(a, b [2]float64) string {
	dy := b[0] - a[0]
	dx := (b[1] - a[1]) * math.Cos(a[0]*math.Pi/180)
	if math.Abs(dy) >= math.Abs(dx) {
		if dy >= 0 {
			return "NORTE"
		}
		return "SUR"
	}
	if dx >= 0 {
		return "ESTE"
	}
	return "OESTE"
}

// pasada es el instante en que una ruta pasa por delante de un sensor.
type pasada struct {
	s     sensor
	along float64 // metros desde el inicio de la ruta
	seg   int     // tramo de la ruta
}

// acercamiento mide a que distancia pasa la ruta de un punto, y en que metro.
func acercamiento(r *rutaCalle, p [2]float64) (dist, along float64, seg int) {
	dist = math.MaxFloat64
	kx := math.Cos(p[0]*math.Pi/180) * 111320.0
	const ky = 110574.0

	for i := 0; i+1 < len(r.vertices); i++ {
		a, b := r.vertices[i], r.vertices[i+1]
		ax, ay := (a[1]-p[1])*kx, (a[0]-p[0])*ky
		bx, by := (b[1]-p[1])*kx, (b[0]-p[0])*ky
		dx, dy := bx-ax, by-ay

		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = math.Min(1, math.Max(0, -(ax*dx+ay*dy)/l2))
		}
		if d := math.Hypot(ax+t*dx, ay+t*dy); d < dist {
			dist = d
			along = r.acumulado[i] + t*(r.acumulado[i+1]-r.acumulado[i])
			seg = i
		}
	}
	return dist, along, seg
}

// pasadasDe devuelve los sensores por los que pasa una ruta, en orden.
func (c *ciudad) pasadasDe(r *rutaCalle) []pasada {
	var out []pasada
	for _, lista := range [][]sensor{c.lpr, c.rad} {
		for _, s := range lista {
			if d, along, seg := acercamiento(r, [2]float64{s.Lat, s.Lon}); d <= 40 {
				out = append(out, pasada{s: s, along: along, seg: seg})
			}
		}
	}
	for i := 1; i < len(out); i++ { // insercion: pocas pasadas por ruta
		for j := i; j > 0 && out[j].along < out[j-1].along; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Eventos
// ---------------------------------------------------------------------------

func (c *ciudad) evento(tipo, productor, zona string, lat, lon float64, at time.Time,
	sev string, payload map[string]any) evProgramado {

	if sev == "" {
		sev = severidad(tipo)
	}
	return evProgramado{at: at, tipo: tipo, ev: map[string]any{
		"event_version": "1.0",
		"event_type":    tipo,
		"event_id":      uuidV4(),
		"timestamp":     at.UTC().Format(time.RFC3339Nano),
		"producer":      productor,
		"source":        "simulated",
		"geo":           map[string]any{"zone": zona, "lat": redondear(lat), "lon": redondear(lon)},
		"severity":      sev,
		"payload":       payload,
	}}
}

func (c *ciudad) programar(e evProgramado) {
	heap.Push(&c.cola, e)
	c.programados[e.tipo]++

	if c.etiq == nil {
		return // trafico de fondo: no es un incidente, no se anota
	}

	if e.at.After(c.ultimoEtiq) {
		c.ultimoEtiq = e.at
	}

	t := evTag{Tipo: e.tipo}
	t.ID, _ = e.ev["event_id"].(string)
	if geo, ok := e.ev["geo"].(map[string]any); ok {
		t.Zona, _ = geo["zone"].(string)
	}
	if pl, ok := e.ev["payload"].(map[string]any); ok {
		if v, ok := pl["velocidad_estimada"].(float64); ok {
			t.Vel = v
		}
		if v, ok := pl["velocidad_detectada"].(float64); ok {
			t.Vel = v
		}
		switch d := pl["nivel_decibeles"].(type) {
		case int:
			t.Db = float64(d)
		case float64:
			t.Db = d
		}
		if i, _ := pl["infraccion"].(string); i != "" {
			t.Infraccion = true
		}
		for _, k := range []string{"tipo_sonido_detectado", "tipo_evento", "tipo_de_alerta"} {
			if d, _ := pl[k].(string); d != "" {
				t.Detalle = d
			}
		}
	}
	c.etiq.Eventos = append(c.etiq.Eventos, t)
}

// zonaDelIncidente anota la zona del incidente en curso.
func (c *ciudad) zonaDelIncidente(zona string) {
	if c.etiq != nil {
		c.etiq.Zona = zona
	}
}

// guardarVerdad escribe lo que la simulacion sabe de cada incidente.
func (c *ciudad) guardarVerdad() {
	if c.rutaVerdad == "" {
		return
	}
	b, err := json.MarshalIndent(c.verdades, "", " ")
	if err != nil {
		fmt.Println("  no se pudo serializar la verdad:", err)
		return
	}
	if err := os.WriteFile(c.rutaVerdad, b, 0o644); err != nil {
		fmt.Println("  no se pudo guardar la verdad en", c.rutaVerdad, ":", err)
		return
	}
	fmt.Println("  verdad de la simulacion:", len(c.verdades), "incidentes guardados en", c.rutaVerdad)
}

// programarPasadas anota las lecturas que producira un vehiculo al recorrer una
// ruta: cada sensor lo ve a la hora a la que llega a su altura.
//
// Las lecturas anteriores al instante actual se descartan: el vehiculo ya habia
// salido cuando arranca la simulacion, y lo que ocurrio antes no se puede
// enviar sin falsear el orden en que llegan los eventos.
func (c *ciudad) programarPasadas(r *rutaCalle, v vehiculo, inicio time.Time, vavg float64,
	ahora time.Time, huida bool, cong float64) {

	for _, p := range c.pasadasDe(r) {
		t := inicio.Add(time.Duration(p.along / vavg * float64(time.Second)))
		if t.Before(ahora) {
			continue
		}

		vel := p.s.VelRef * cong * v.agresividad * c.entre(0.85, 1.15)
		if huida && c.u() < 0.8 {
			vel = math.Max(vel, c.entre(82, 107))
		}
		vel = math.Round(math.Min(140, math.Max(5, vel)))

		if p.s.Tipo == "velocidad" {
			sig := r.vertices[min(p.seg+1, len(r.vertices)-1)]
			c.programar(c.evento("sensor.speed", "gateway-radares", p.s.Zona, p.s.Lat, p.s.Lon, t, "",
				map[string]any{
					"velocidad_detectada": vel,
					"sensor_id":           p.s.ID,
					"direccion":           cardinal(r.vertices[p.seg], sig),
				}))
			continue
		}

		// Una camara no ve todos los coches: se le escapa alguno.
		if c.u() < 0.03 {
			continue
		}

		payload := map[string]any{
			"placa_vehicular":    c.leerPlaca(v.placa),
			"velocidad_estimada": vel,
			"modelo_vehiculo":    v.modelo,
			"color_vehiculo":     v.color,
			"ubicacion_sensor":   p.s.ID,
		}
		// Solo una camara de un cruce con semaforo puede ver un cruce en rojo.
		if p.s.Semaforo {
			if (huida && c.u() < 0.5) || (!huida && c.u() < 0.012) {
				payload["infraccion"] = InfraccionSemaforoRojo
			}
		}
		c.programar(c.evento("sensor.lpr", "gateway-camaras-lpr", p.s.Zona, p.s.Lat, p.s.Lon, t, "", payload))
	}
}

// InfraccionSemaforoRojo es el valor del payload que marca el cruce en rojo.
// Se repite aqui porque este paquete es el generador de carga y no importa el
// del correlator, que es otro modulo.
const InfraccionSemaforoRojo = "semaforo_rojo"

// viajeAleatorio crea el viaje de un vehiculo cualquiera.
func (c *ciudad) viajeAleatorio(inicio, ahora time.Time) {
	r, err := pedirRuta(c.puntoEn(c.zonaPonderada()), c.puntoEn(c.zonaPonderada()))
	if err != nil || r.distanciaM < 800 || r.duracionS <= 0 {
		return
	}
	v := c.vehiculoAlAzar()
	cong := congestion(factorHora(inicio))
	vavg := r.distanciaM / r.duracionS * cong * v.agresividad
	c.programarPasadas(r, v, inicio, vavg, ahora, false, cong)
}

// ---------------------------------------------------------------------------
// Incidentes
// ---------------------------------------------------------------------------

var mensajesAccidente = []string{
	"choque entre dos vehiculos en el cruce", "vehiculo volcado en la via",
	"motocicleta derribada", "colision multiple, hay heridos",
}
var mensajesIncendio = []string{
	"humo saliendo de un local comercial", "fuego en un vehiculo", "incendio en una bodega",
	"llamas en un edificio de apartamentos",
}
var mensajesAltercado = []string{
	"discusion que escala entre conductores", "pelea en la via publica", "grupo de personas alterado",
}
var origenesReporte = []string{"usuario", "app", "punto_fisico"}

func (c *ciudad) reporte(s sensor, tipo, msg, sev string, at time.Time) {
	lat, lon := c.cerca(s.Lat, s.Lon, 30)
	via := s.Via
	if via == "" {
		via = "via sin nombre"
	}
	c.programar(c.evento("citizen.report", "app-ciudadana", s.Zona, lat, lon, at, sev,
		map[string]any{
			"tipo_evento":          tipo,
			"mensaje_descriptivo":  msg,
			"ubicacion_aproximada": "sobre " + via,
			"origen":               origenesReporte[c.rng.IntN(len(origenesReporte))],
		}))
}

func (c *ciudad) acustico(s sensor, sonido string, db int, prob float64, at time.Time) {
	c.programar(c.evento("sensor.acoustic", "gateway-sensores-acusticos", s.Zona, s.Lat, s.Lon, at, "",
		map[string]any{
			"tipo_sonido_detectado":       sonido,
			"nivel_decibeles":             db,
			"probabilidad_evento_critico": math.Round(prob*100) / 100,
		}))
}

func (c *ciudad) alAzar(l []sensor) (sensor, bool) {
	if len(l) == 0 {
		return sensor{}, false
	}
	return l[c.rng.IntN(len(l))], true
}

// roboConHuida: alguien pulsa un boton de panico y un vehiculo se aleja a toda
// prisa, cruzando semaforos en rojo y por encima de la velocidad.
//
// El destino se elige entre varios candidatos y se prefiere el que pasa por mas
// camaras: quien huye no sabe donde estan, de modo que a veces se le ve poco y
// a veces nada, pero para una simulacion util conviene que no sea siempre lo
// segundo.
func (c *ciudad) roboConHuida(ahora time.Time) {
	b, ok := c.alAzar(c.btn)
	if !ok {
		return
	}
	c.zonaDelIncidente(b.Zona)

	c.programar(c.evento("panic.button", "gateway-botones-panico", b.Zona, b.Lat, b.Lon, ahora, "",
		map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": b.ID,
			"user_context":              "fijo",
		}))

	var mejor *rutaCalle
	mejorN := -1
	for intento := 0; intento < 6; intento++ {
		dest := c.zonaPonderada()
		if distanciaRectaKm([2]float64{b.Lat, b.Lon}, coordenadasDe(dest)) < 3 {
			continue
		}
		r, err := pedirRuta([2]float64{b.Lat, b.Lon}, c.puntoEn(dest))
		if err != nil || r.distanciaM < 2500 || r.duracionS <= 0 {
			continue
		}
		n := 0
		for _, p := range c.pasadasDe(r) {
			if p.s.Tipo == "lpr" && p.along > 100 {
				n++
			}
		}
		if n > mejorN {
			mejor, mejorN = r, n
		}
		if n >= 3 {
			break
		}
	}
	if mejor == nil {
		return // el pulsador queda solo: una falsa alarma mas
	}

	v := c.vehiculoAlAzar()
	v.agresividad = 1.3
	if c.etiq != nil {
		c.etiq.Placa = v.placa
	}
	salida := ahora.Add(time.Duration(10+c.rng.IntN(30)) * time.Second)
	cong := congestion(factorHora(salida))
	c.programarPasadas(mejor, v, salida, mejor.distanciaM/mejor.duracionS*cong*v.agresividad, ahora, true, cong)
}

func (c *ciudad) panicoFalso(ahora time.Time) {
	b, ok := c.alAzar(c.btn)
	if !ok {
		return
	}
	c.zonaDelIncidente(b.Zona)
	c.programar(c.evento("panic.button", "gateway-botones-panico", b.Zona, b.Lat, b.Lon, ahora, "",
		map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": b.ID,
			"user_context":              "fijo",
		}))
}

func (c *ciudad) accidente(ahora time.Time) {
	a, ok := c.alAzar(c.acu)
	if !ok {
		return
	}
	c.zonaDelIncidente(a.Zona)
	impacto := ahora.Add(12 * time.Second)

	// Si hay una camara cerca, ve el coche entrar rapido y salir casi parado.
	if cam, ok := c.camaraCerca(a.Lat, a.Lon, 0.7); ok && c.u() < 0.8 {
		v := c.vehiculoAlAzar()
		for _, l := range []struct {
			at         time.Time
			vmin, vmax float64
		}{
			{ahora.Add(2 * time.Second), 72, 100},
			{impacto.Add(3 * time.Second), 8, 28},
		} {
			c.programar(c.evento("sensor.lpr", "gateway-camaras-lpr", cam.Zona, cam.Lat, cam.Lon, l.at, "",
				map[string]any{
					"placa_vehicular":    v.placa,
					"velocidad_estimada": math.Round(c.entre(l.vmin, l.vmax)),
					"modelo_vehiculo":    v.modelo,
					"color_vehiculo":     v.color,
					"ubicacion_sensor":   cam.ID,
				}))
		}
	}

	if c.u() < 0.85 {
		sonido := "vidrio_roto"
		if c.u() < 0.2 {
			sonido = "explosion"
		}
		c.acustico(a, sonido, 90+c.rng.IntN(26), c.entre(0.7, 0.95), impacto)
	}

	c.reporte(a, "accidente", mensajesAccidente[c.rng.IntN(len(mensajesAccidente))], "warning",
		impacto.Add(time.Duration(c.entre(25, 150))*time.Second))
}

func (c *ciudad) incendio(ahora time.Time) {
	a, ok := c.alAzar(c.acu)
	if !ok {
		return
	}
	c.zonaDelIncidente(a.Zona)
	inicio := ahora.Add(8 * time.Second)
	if c.u() < 0.75 {
		c.acustico(a, "explosion", 101+c.rng.IntN(30), c.entre(0.75, 0.98), inicio)
	}
	c.reporte(a, "incendio", mensajesIncendio[c.rng.IntN(len(mensajesIncendio))], "critical",
		inicio.Add(time.Duration(c.entre(30, 240))*time.Second))
}

// altercado genera un reporte que ninguna regla correlaciona: es ruido, y en la
// vida real es la mayor parte de lo que llega por la aplicacion.
func (c *ciudad) altercado(ahora time.Time) {
	s, ok := c.alAzar(c.acu)
	if !ok {
		return
	}
	c.zonaDelIncidente(s.Zona)
	c.reporte(s, "altercado", mensajesAltercado[c.rng.IntN(len(mensajesAltercado))], "info",
		ahora.Add(time.Duration(c.entre(1, 20))*time.Second))
}

// ruidoFalso es un sensor acustico que oye algo que no es un incidente: petardos,
// un camion que descarga, una puerta de metal.
func (c *ciudad) ruidoFalso(ahora time.Time) {
	s, ok := c.alAzar(c.acu)
	if !ok {
		return
	}
	c.zonaDelIncidente(s.Zona)
	sonido := "vidrio_roto"
	if c.u() < 0.5 {
		sonido = "explosion"
	}
	c.acustico(s, sonido, 78+c.rng.IntN(19), c.entre(0.1, 0.4), ahora)
}

// ---------------------------------------------------------------------------
// Bucle principal
// ---------------------------------------------------------------------------

// ciudad arranca la simulacion y no vuelve hasta que termina el tiempo o se cancela.
func (g *generador) ciudad(ctx context.Context, rutaCatalogo string, semilla uint64, maxCamaras int,
	duracion time.Duration, intensidad float64, rutaVerdad string) *resultados {

	res := nuevosResultados()

	cat, err := cargarCatalogo(rutaCatalogo)
	if err != nil {
		fmt.Println("  no hay catalogo de sensores: se genera ahora (tarda un minuto)")
		cat, err = generarCatalogo(semilla, maxCamaras)
		if err != nil {
			fmt.Printf("  no se pudo generar el catalogo: %v\n", err)
			fmt.Println("  este escenario necesita el motor de rutas: ./make.sh rutas-preparar")
			res.registrarError("catalogo")
			return res
		}
		if err := guardarCatalogo(rutaCatalogo, cat); err != nil {
			fmt.Printf("  no se pudo guardar el catalogo: %v\n", err)
		}
	}

	c := nuevaCiudad(g, res, cat, semilla)
	c.rutaVerdad = rutaVerdad
	return c.correr(ctx, duracion, intensidad)
}

func (c *ciudad) correr(ctx context.Context, duracion time.Duration, intensidad float64) *resultados {
	inicio := time.Now()
	var limite time.Time
	if duracion > 0 {
		limite = inicio.Add(duracion)
	}

	fmt.Println("  Ciudad de Guatemala, simulacion en tiempo real")
	fmt.Printf("  hora local: %s   actividad ahora: x%.2f\n",
		inicio.In(zonaGT).Format("15:04"), factorHora(inicio))
	fmt.Printf("  red: %d camaras LPR, %d radares, %d acusticos, %d botones de panico; %d vehiculos\n",
		len(c.lpr), len(c.rad), len(c.acu), len(c.btn), len(c.flota))
	fmt.Println("  AVISO: red de sensores simulada, no es el inventario de la Municipalidad.")
	if duracion > 0 {
		fmt.Printf("  durara %s\n", duracion)
	} else {
		fmt.Println("  corre hasta que se pulse Ctrl+C")
	}
	fmt.Println("")

	envios := make(chan map[string]any, 8192)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range envios {
				c.g.enviar(ctx, ev, c.res)
			}
		}()
	}

	// Calentamiento. Una ciudad no arranca vacia: a las siete de la manana ya hay
	// miles de coches en la calle. Se crean los viajes que habrian empezado en
	// los ultimos doce minutos y se anota solo lo que aun esta por ocurrir.
	porMinuto := c.porMinuto
	ya := int(porMinuto * factorHora(inicio) * 12)
	fmt.Printf("  calentando: %d viajes que ya estan en la calle...\n", ya)
	for i := 0; i < ya && ctx.Err() == nil; i++ {
		c.viajeAleatorio(inicio.Add(-time.Duration(c.u()*12*float64(time.Minute))), inicio)
	}
	fmt.Printf("  listos: %d lecturas de sensores por venir\n\n", c.cola.Len())

	incs := []*incidente{
		{nombre: "robo_con_huida", porHora: 3, hacer: c.roboConHuida},
		{nombre: "panico_falso", porHora: 10, hacer: c.panicoFalso},
		{nombre: "accidente", porHora: 5, hacer: c.accidente},
		{nombre: "incendio", porHora: 1.5, hacer: c.incendio},
		{nombre: "altercado", porHora: 12, hacer: c.altercado},
		{nombre: "ruido_falso", porHora: 6, hacer: c.ruidoFalso},
	}
	for _, in := range incs {
		in.prox = inicio.Add(c.exp(in.porHora * intensidad / 60))
	}

	llegada := inicio
	ultimoInforme, enviadosAntes := inicio, 0

	tic := time.NewTicker(200 * time.Millisecond)
	defer tic.Stop()

bucle:
	for {
		select {
		case <-ctx.Done():
			break bucle
		case ahora := <-tic.C:
			if !limite.IsZero() && ahora.After(limite) {
				break bucle
			}
			f := factorHora(ahora)

			// Vehiculos que salen.
			if ahora.Sub(llegada) > 5*time.Second {
				llegada = ahora // se quedo atras: no se acumulan salidas
			}
			for n := 0; !llegada.After(ahora) && n < 200; n++ {
				c.viajeAleatorio(ahora, ahora)
				llegada = llegada.Add(c.exp(porMinuto * f))
			}

			// Incidentes. Su frecuencia sube con la actividad, pero menos que ella:
			// un robo no depende tanto de la hora pico como el trafico.
			for _, in := range incs {
				for n := 0; !in.prox.After(ahora) && n < 20; n++ {
					c.etiq = &verdad{
						ID:     fmt.Sprintf("INC-%04d", len(c.verdades)+1),
						Clase:  in.nombre,
						Inicio: ahora,
					}
					in.hacer(ahora)
					c.verdades = append(c.verdades, *c.etiq)
					c.etiq = nil
					c.incidentes[in.nombre]++
					in.prox = ahora.Add(c.exp(in.porHora * intensidad * math.Sqrt(f) / 60))
				}
			}

			// Eventos que ya les toca.
			for c.cola.Len() > 0 && !c.cola[0].at.After(ahora) {
				e := heap.Pop(&c.cola).(evProgramado)
				select {
				case envios <- e.ev:
					c.enviados[e.tipo]++
				default:
					c.perdidos++ // el ingestor no da abasto: se anota, no se bloquea el reloj
				}
			}

			if ahora.Sub(ultimoInforme) >= 30*time.Second {
				total := 0
				for _, n := range c.enviados {
					total += n
				}
				c.informe(ahora, f, float64(total-enviadosAntes)/ahora.Sub(ultimoInforme).Seconds())
				ultimoInforme, enviadosAntes = ahora, total
			}
		}
	}

	close(envios)
	wg.Wait()
	c.guardarVerdad()

	fmt.Println("")
	fmt.Printf("  Simulacion terminada tras %s\n", time.Since(inicio).Round(time.Second))
	fmt.Printf("  incidentes generados: %v\n", c.incidentes)
	fmt.Printf("  eventos enviados:     %v\n", c.enviados)
	fmt.Printf("  eventos aun en cola:  %d (no se enviaron)\n", c.cola.Len())
	if c.perdidos > 0 {
		fmt.Printf("  PERDIDOS por cola llena: %d\n", c.perdidos)
	}
	fmt.Println("")
	return c.res
}

func (c *ciudad) informe(ahora time.Time, f, evPorSeg float64) {
	fmt.Printf("  %s  actividad x%.2f  %.1f eventos/s  lpr=%d radar=%d panico=%d acustico=%d reporte=%d  incidentes=%v  cola=%d  errores=%d\n",
		ahora.In(zonaGT).Format("15:04:05"), f, evPorSeg,
		c.enviados["sensor.lpr"], c.enviados["sensor.speed"], c.enviados["panic.button"],
		c.enviados["sensor.acoustic"], c.enviados["citizen.report"],
		c.incidentes, c.cola.Len(), c.res.errores())
}
