package main

import (
	"context"
	"fmt"
	"math"
	"time"
)

// persecucion reproduce una huida tras un robo, como un VIAJE de A a B.
//
// Alguien pulsa el boton de panico en A. Un vehiculo sale de ahi, cruza un
// semaforo en rojo junto al pulsador, circula a velocidad excesiva por una
// camara intermedia y se le ve por ultima vez en otra, cerca de B. Despues de
// eso no hay mas camaras y se pierde el contacto.
//
// Los tres sensores estan SOBRE el camino que el motor de rutas calcula entre A
// y B, y las horas son las que corresponde a recorrerlo a la velocidad media de
// ese camino. Nada se dispersa al azar: un recorrido dibujado con estos datos
// es una linea de A a B, sin desvios ni vueltas atras.

// parejasDeHuida son los viajes de cada incidente: origen y destino.
//
// Las zonas de origen son DISTINTAS entre incidentes. Si dos pulsadores
// estuvieran en la misma zona, la regla podria anclar la huida de un vehiculo en
// el pulsador del otro, y para una demostracion eso no se explica solo.
var parejasDeHuida = [][2]string{
	{"zona_1", "zona_9"},
	{"zona_2", "zona_6"},
	{"zona_11", "zona_7"},
	{"zona_18", "zona_17"},
	{"zona_21", "zona_24"},
}

func (g *generador) persecucion(ctx context.Context, factorTrafico float64, cuantos int) *resultados {
	res := nuevosResultados()

	if cuantos < 1 {
		cuantos = 1
	}
	if cuantos > len(parejasDeHuida) {
		fmt.Printf("  solo hay %d viajes definidos; se generan esos\n\n", len(parejasDeHuida))
		cuantos = len(parejasDeHuida)
	}

	for i := 0; i < cuantos; i++ {
		if ctx.Err() != nil {
			return res
		}
		if cuantos > 1 {
			fmt.Printf("  --- incidente %d de %d ---\n", i+1, cuantos)
		}
		g.unaPersecucion(ctx, res, factorTrafico, parejasDeHuida[i], i)
	}

	fmt.Println("  En el tablero de trazabilidad, la tabla de la izquierda lista un boton")
	fmt.Println("  de panico por incidente. Al pulsar una fila, el mapa dibuja ESA huida:")
	fmt.Println("  INICIO donde se pulso el boton y ULTIMO CONTACTO donde se perdio la pista.")
	fmt.Println("")

	return res
}

// unaPersecucion emite los cuatro eventos de un incidente.
func (g *generador) unaPersecucion(ctx context.Context, res *resultados,
	factorTrafico float64, pareja [2]string, indice int) {

	origen, destino := pareja[0], pareja[1]

	// Una matricula distinta por incidente. Repetirla fundiria las huidas en
	// una sola, porque la correlacion agrupa precisamente por vehiculo.
	matricula := fmt.Sprintf("P%03dFUG", 777+indice)

	ruta, err := pedirRuta(coordenadasDe(origen), coordenadasDe(destino))
	if err != nil {
		fmt.Printf("  no se pudo calcular el viaje %s -> %s: %v\n", origen, destino, err)
		fmt.Println("  este escenario necesita el motor de rutas: ./make.sh rutas-preparar")
		fmt.Println("  y despues levantar el servicio osrm")
		fmt.Println("")
		res.registrarError("osrm")
		return
	}

	total := ruta.distanciaM

	// Velocidad media del viaje, con congestion. 1.0 es flujo libre, que es lo
	// que hace quien huye saltandose las senales; 1.8 se acerca a una hora pico.
	v := total / (ruta.duracionS * factorTrafico) // m/s

	// Donde esta cada camara, en metros desde el pulsador.
	//
	// La primera esta casi junto al boton: es la que ve el cruce en rojo.
	// La segunda no puede quedar a mas de ocho minutos del pulsador, o cuando
	// llegue la lectura el pulsador ya habria salido de la ventana de
	// correlacion y no habria nada a lo que anclar la huida. La ultima esta
	// casi al final del viaje.
	d1 := math.Min(300, 0.10*total)
	d2 := math.Min(0.45*total, v*420)
	d2 = math.Max(d2, d1+0.15*total)
	d2 = math.Min(d2, 0.80*total)
	d3 := 0.97 * total

	const salida = 10.0 // segundos entre el pulsador y el arranque del vehiculo

	tras := func(d float64) float64 { return salida + d/v } // segundos desde el pulsador
	fin := tras(d3)
	const margen = 20.0 // el ultimo contacto fue "hace un momento"

	hace := func(segTrasPulsador float64) time.Duration {
		return time.Duration((fin-segTrasPulsador+margen)*1000) * time.Millisecond
	}

	puntoPulsador := ruta.en(0)
	p1, p2, p3 := ruta.en(d1), ruta.en(d2), ruta.en(d3)

	// El pulsador y la camara de su esquina comparten zona por construccion;
	// las demas se etiquetan por proximidad.
	z1 := origen
	z2 := zonaMasCercana(p2)
	z3 := zonaMasCercana(p3)

	fmt.Printf("  Viaje %s -> %s: %.1f km por calles, %.0f min a %.0f km/h de media (trafico x%.1f)\n",
		origen, destino, total/1000, ruta.duracionS*factorTrafico/60, v*3.6, factorTrafico)

	pasos := []struct {
		descripcion string
		zona        string
		metros      float64
		seg         float64
		evento      map[string]any
	}{
		{
			"boton de panico por robo", origen, 0, 0,
			eventoEnPunto("panic.button", origen, puntoPulsador, hace(0), map[string]any{
				"tipo_de_alerta":            "panico",
				"identificador_dispositivo": "BTN-" + origen,
				"user_context":              "movil",
			}),
		},
		{
			// La velocidad va por encima del umbral ademas de la infraccion:
			// asi esta misma lectura completa tambien la regla de robo, que
			// exige panico mas vehiculo a alta velocidad en la zona.
			"camara de semaforo: cruce en rojo", z1, d1, tras(d1),
			eventoEnPunto("sensor.lpr", z1, p1, hace(tras(d1)), map[string]any{
				"placa_vehicular":    matricula,
				"infraccion":         "semaforo_rojo",
				"velocidad_estimada": 95,
				"modelo_vehiculo":    "sedan",
				"color_vehiculo":     "gris",
				"ubicacion_sensor":   nombreSensor(p1),
			}),
		},
		{
			"camara intermedia: exceso de velocidad", z2, d2, tras(d2),
			eventoEnPunto("sensor.lpr", z2, p2, hace(tras(d2)), map[string]any{
				"placa_vehicular":    matricula,
				"velocidad_estimada": 112,
				"ubicacion_sensor":   nombreSensor(p2),
			}),
		},
		{
			"ultimo contacto", z3, d3, fin,
			eventoEnPunto("sensor.lpr", z3, p3, hace(fin), map[string]any{
				"placa_vehicular":    matricula,
				"velocidad_estimada": 118,
				"ubicacion_sensor":   nombreSensor(p3),
			}),
		},
	}

	for i, p := range pasos {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  %d. %-40s %-8s a %5.2f km  +%s\n", i+1, p.descripcion, p.zona,
			p.metros/1000, (time.Duration(p.seg) * time.Second).Round(time.Second))
		g.enviar(ctx, p.evento, res)
		time.Sleep(300 * time.Millisecond)
	}

	fmt.Printf("     se esperan possible_robbery en %s y vehicle_pursuit; el rastro termina en %s\n\n",
		origen, z3)
}

// nombreSensor da a una camara un nombre derivado de su posicion.
//
// Un sensor esta fijo: la misma camara tiene siempre las mismas coordenadas y
// el mismo nombre. Antes "cruce_6a_avenida" aparecia en cuatro lugares
// distintos, uno por incidente. Derivar el nombre de la posicion hace que
// repetir el escenario sobre el mismo viaje reconozca las mismas camaras.
func nombreSensor(p [2]float64) string {
	return fmt.Sprintf("LPR_%.4f_%.4f", p[0], p[1])
}

// eventoEnPunto construye un evento en una coordenada EXACTA, fechado en el pasado.
//
// No dispersa la posicion, al contrario que evento(): aqui la coordenada es la
// de un sensor concreto situado sobre una calle, y moverla la sacaria del camino.
//
// Lleva la marca de tiempo explicita porque el motor comprueba que el vehiculo
// haya podido recorrer la distancia entre dos lecturas. Fechar los eventos
// hacia atras, con las horas reales del recorrido, deja los datos coherentes y
// la demostracion sigue durando segundos en lugar de los minutos que tardaria
// el coche.
func eventoEnPunto(tipo, zona string, p [2]float64, hace time.Duration,
	payload map[string]any) map[string]any {

	return map[string]any{
		"event_version": "1.0",
		"event_type":    tipo,
		"event_id":      uuidV4(),
		"timestamp":     time.Now().UTC().Add(-hace).Format(time.RFC3339Nano),
		"producer":      "load-test",
		"source":        "simulated",
		"geo":           map[string]any{"zone": zona, "lat": redondear(p[0]), "lon": redondear(p[1])},
		"severity":      severidad(tipo),
		"payload":       payload,
	}
}
