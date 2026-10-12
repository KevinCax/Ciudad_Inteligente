package rules

import (
	"math"
	"time"

	"ciudadinteligente/correlator/internal/canonical"
)

// NombreReglaPersecucion identifica la regla en los parciales y las metricas.
const NombreReglaPersecucion = "persecucion_vehicular"

// InfraccionSemaforoRojo es el valor que marca una lectura de camara de
// semaforo.
//
// Viaja en el payload de un sensor.lpr y no como tipo de evento propio: una
// camara de semaforo ES un lector de placas situado en un cruce, de modo que
// el evento canonico es el mismo y lo que cambia es la circunstancia. Ademas
// el payload no esta validado por el contrato, asi que modelarlo aqui no
// obliga a versionar el esquema ni a tocar los cinco servicios.
const InfraccionSemaforoRojo = "semaforo_rojo"

// VentanaVehiculo es la foto de las apariciones de un mismo vehiculo.
//
// A diferencia de Ventana, que agrupa por zona, esta agrupa por vehiculo y por
// tanto cruza zonas. Es la unica forma de ver una huida: un coche que escapa
// cambia de zona precisamente para dejar de aparecer en la ventana de la zona
// donde cometio el primer hecho.
type VentanaVehiculo struct {
	// Token es el seudonimo HMAC de la placa. Nunca la placa en claro.
	Token string

	Ahora   time.Time
	Eventos []Evento

	// Panicos son los botones de panico pulsados en las zonas por las que
	// paso el vehiculo. Los aporta el llamador porque viven en las ventanas
	// de zona, no en la del vehiculo: un boton de panico no lleva placa.
	Panicos []Evento
}

// Infracciones devuelve las apariciones del vehiculo que son infraccion.
//
// Se exporta para que el llamador pueda usarla como filtro barato antes de ir
// a buscar los botones de panico, que exige una consulta por cada zona que el
// vehiculo visito. Sin esa comprobacion previa, cada lectura de placa de la
// ciudad dispararia varias consultas adicionales a Redis.
func Infracciones(eventos []Evento, desde time.Time, cfg Config) []Evento {
	var out []Evento
	for _, e := range eventos {
		if e.Momento.Before(desde) {
			continue
		}
		if esInfraccion(e, cfg) {
			out = append(out, e)
		}
	}
	ordenar(out)
	return out
}

// CadenaPlausible descarta las apariciones a las que el vehiculo no pudo
// llegar desde la anterior.
//
// Es la comprobacion que impide encadenar un coche de la zona 10 con otro de
// la zona 6 dos segundos despues: 6.57 km que exigirian casi doce mil km/h.
// Sin ella el motor da por buena esa cadena, se vio ocurrir sobre el sistema
// en ejecucion, y la alerta resultante situa el incidente a kilometros de
// donde ocurrio.
//
// Un salto imposible casi nunca es un coche rapido. Es una placa mal leida por
// la camara —un digito confundido basta— o una placa clonada, que son
// problemas corrientes de los lectores automaticos. Por eso la aparicion se
// descarta y la cadena CONTINUA con la siguiente que si sea alcanzable, en
// lugar de cortarse: lo que no encaja es esa lectura, no el incidente.
//
// Se mide contra la ultima aparicion ACEPTADA, no contra la inmediatamente
// anterior. Si se midiera contra la anterior sin mas, una sola lectura falsa
// desplazaria el punto de referencia y arrastraria consigo a las lecturas
// buenas que vinieran despues.
//
// Una aparicion sin coordenadas no se puede juzgar, y ante la duda se acepta:
// descartar por falta de dato convertiria un hueco de informacion en una
// correlacion perdida, que es el fallo mas caro de los dos.
func CadenaPlausible(apariciones []Evento, cfg Config) []Evento {
	if len(apariciones) < 2 || cfg.VelocidadMaximaPlausible <= 0 {
		return apariciones
	}

	out := []Evento{apariciones[0]}
	ultima := apariciones[0]

	for _, e := range apariciones[1:] {
		if alcanzable(ultima, e, cfg.VelocidadMaximaPlausible) {
			out = append(out, e)
			ultima = e
		}
	}

	return out
}

// alcanzable responde si un vehiculo pudo ir de a hasta b sin superar la
// velocidad media indicada, en km/h.
func alcanzable(a, b Evento, maxKmH float64) bool {
	km, ok := DistanciaKm(a, b)
	if !ok {
		return true // sin coordenadas no hay nada que juzgar
	}

	horas := b.Momento.Sub(a.Momento).Hours()
	if horas <= 0 {
		// Dos lecturas simultaneas en sitios distintos son imposibles salvo
		// que esten practicamente en el mismo punto. El margen cubre el
		// desorden de milisegundos entre sensores.
		return km < 0.5
	}

	return km/horas <= maxKmH
}

// DistanciaKm devuelve la distancia en kilometros entre dos eventos, y si se
// pudo calcular.
//
// Usa la formula del semiverseno sobre una esfera. El error frente a un
// elipsoide es de alrededor del 0.3 %, muy por debajo de la precision de un
// sensor urbano, y evita arrastrar una dependencia geoespacial entera para
// una cuenta que cabe en diez lineas.
func DistanciaKm(a, b Evento) (float64, bool) {
	if a.Lat == nil || a.Lon == nil || b.Lat == nil || b.Lon == nil {
		return 0, false
	}

	const radioTierraKm = 6371.0

	rad := func(g float64) float64 { return g * math.Pi / 180 }

	dLat := rad(*b.Lat - *a.Lat)
	dLon := rad(*b.Lon - *a.Lon)

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(*a.Lat))*math.Cos(rad(*b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * radioTierraKm * math.Asin(math.Sqrt(h)), true
}

// esInfraccion decide si una aparicion del vehiculo es una infraccion.
func esInfraccion(e Evento, cfg Config) bool {
	// Una infraccion declarada por el sensor vale por si sola: la camara ya
	// decidio que hubo cruce en rojo y no corresponde al correlator dudarlo.
	if texto(e.Payload, "infraccion") != "" {
		return true
	}

	var vel float64
	var ok bool
	switch e.Tipo {
	case canonical.TypeSensorLPR:
		vel, ok = numero(e.Payload, "velocidad_estimada")
	case canonical.TypeSensorSpeed:
		vel, ok = numero(e.Payload, "velocidad_detectada")
	}
	return ok && vel > cfg.UmbralVelocidad
}

// EvaluarVehiculo corre las reglas que agrupan por vehiculo.
//
// Devuelve un Resultado como Evaluar para que el llamador trate igual ambos
// caminos: una deteccion se emite y un parcial alimenta el recuento de falsos
// positivos, venga de donde venga.
func EvaluarVehiculo(v VentanaVehiculo, cfg Config) Resultado {
	var res Resultado

	det, parcial := reglaPersecucionVehicular(v, cfg)
	if det != nil {
		res.Detecciones = append(res.Detecciones, *det)
	}
	if parcial != nil {
		res.Parciales = append(res.Parciales, *parcial)
	}

	return res
}

// reglaPersecucionVehicular enlaza las infracciones sucesivas de un mismo
// vehiculo con el hecho que las origino.
//
// Modela la secuencia que ninguna regla de zona puede ver: alguien pulsa el
// boton de panico por un robo, y minutos despues el mismo vehiculo se salta un
// semaforo y circula a velocidad excesiva, quiza ya en otra zona. Por separado
// son una alerta de robo y una multa de transito; juntas son una huida.
//
// El boton de panico es el ancla deliberadamente. Sin el, dos infracciones del
// mismo coche son solo un conductor imprudente, y emitir "persecucion" por eso
// llenaria el tablero de falsas huidas hasta que el operador dejara de mirarlo.
func reglaPersecucionVehicular(v VentanaVehiculo, cfg Config) (*Deteccion, *Parcial) {
	desde := v.Ahora.Add(-cfg.VentanaPersecucion)

	// La cadena se filtra por plausibilidad fisica ANTES de contar: una
	// aparicion a la que el vehiculo no pudo llegar no es parte de esta
	// huida, y contarla llevaria a emitir una alerta situada en una zona por
	// la que el coche nunca paso.
	infracciones := CadenaPlausible(Infracciones(v.Eventos, desde, cfg), cfg)
	if len(infracciones) < cfg.MinInfraccionesPersecucion {
		return nil, nil
	}

	// Las zonas donde el vehiculo infringio. El ancla debe estar en una de
	// ellas: un boton de panico al otro lado de la ciudad no tiene relacion
	// con este coche, y aceptarlo convertiria la regla en un generador de
	// coincidencias.
	zonas := make(map[string]bool, len(infracciones))
	for _, e := range infracciones {
		zonas[e.Zona] = true
	}

	ancla, hayAncla := anclaDePanico(v.Panicos, zonas, desde, infracciones[0].Momento)
	if !hayAncla {
		// Coincidencia parcial: hay patron de huida pero falta el hecho que
		// la origine. Si la ventana expira sin aparecer, cuenta como falso
		// positivo evitado, igual que en las reglas de zona.
		return nil, &Parcial{
			Regla:      NombreReglaPersecucion,
			Disparador: infracciones[0].ID,
			Faltante:   "boton_de_panico_en_la_zona",
			Expira:     v.Ahora.Add(cfg.VentanaPersecucion),
		}
	}

	ultima := infracciones[len(infracciones)-1]

	return &Deteccion{
		Tipo: canonical.AlertVehiclePursuit,
		// La zona de la alerta es donde esta el vehiculo AHORA, no donde
		// empezo todo: es la que necesita quien tiene que interceptarlo.
		Zona:  ultima.Zona,
		Score: 0.92,
		// La evidencia empieza por el ancla para que al reconstruir el
		// incidente se lea en orden causal: primero el hecho, luego la huida.
		Evidencia: ids([]Evento{ancla}, infracciones),
		// El inicio es el primero de los dos hechos, no siempre el ancla: si
		// la victima pulso el boton despues de ver arrancar al coche, tomar el
		// ancla dejaria la ventana empezando DESPUES de una evidencia que
		// contiene, y eso es una incoherencia que cualquiera detecta al
		// mirar la alerta.
		Inicio: masAntiguo(ancla.Momento, infracciones[0].Momento),
		Fin:    v.Ahora,
	}, nil
}

// anclaDePanico busca el boton de panico de la ventana que mejor explica la
// huida, entre los pulsados en alguna de las zonas donde el vehiculo infringio.
//
// Se elige el MAS PROXIMO EN EL TIEMPO a la primera infraccion, no el mas
// antiguo. La diferencia importa cuando hay dos incidentes solapados en la
// misma zona: con "el mas antiguo", una huida que empieza a las 10:30 se
// anclaria en un pulsador de las 10:22 que no tuvo nada que ver, y la alerta
// mezclaria dos incidentes en una sola cadena de evidencias. Se vio ocurrir
// sobre el sistema en ejecucion.
//
// Tampoco se exige que el panico PRECEDA a la infraccion. Parece lo natural
// —primero el hecho, luego la fuga— pero deja fuera un caso real y frecuente:
// la victima que pulsa el boton despues de ver al coche arrancar. Medir por
// cercania cubre los dos ordenes sin privilegiar ninguno.
func anclaDePanico(panicos []Evento, zonas map[string]bool, desde time.Time,
	primeraInfraccion time.Time) (Evento, bool) {

	var mejor Evento
	var mejorDistancia time.Duration
	encontrado := false

	for _, p := range panicos {
		if p.Momento.Before(desde) || !zonas[p.Zona] {
			continue
		}

		distancia := p.Momento.Sub(primeraInfraccion)
		if distancia < 0 {
			distancia = -distancia
		}

		if !encontrado || distancia < mejorDistancia {
			mejor = p
			mejorDistancia = distancia
			encontrado = true
		}
	}

	return mejor, encontrado
}

// masAntiguo devuelve el anterior de dos instantes.
func masAntiguo(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ZonasVisitadas enumera, sin repetir, las zonas donde el vehiculo aparecio.
//
// La usa el llamador para saber en que ventanas de zona debe buscar los
// botones de panico.
func ZonasVisitadas(eventos []Evento, desde time.Time) []string {
	visto := map[string]bool{}
	var out []string
	for _, e := range eventos {
		if e.Momento.Before(desde) || e.Zona == "" || visto[e.Zona] {
			continue
		}
		visto[e.Zona] = true
		out = append(out, e.Zona)
	}
	return out
}

// PanicosDe filtra los botones de panico de un conjunto de eventos.
func PanicosDe(eventos []Evento) []Evento {
	var out []Evento
	for _, e := range eventos {
		if e.Tipo == canonical.TypePanicButton {
			out = append(out, e)
		}
	}
	return out
}
