package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Este archivo calcula cuanto tarda de verdad un automovil en ir de una zona a
// otra, para que los escenarios no inventen los tiempos.
//
// Importa mas de lo que parece. Un escenario que coloca un boton de panico y,
// treinta segundos despues, una lectura de placa a kilometros de distancia, no
// esta probando el sistema: esta probando una situacion que no puede ocurrir.
// Y si el motor la da por buena, el fallo no se descubre hasta que alguien
// mira el mapa y ve un coche cruzando la ciudad en medio minuto.
//
// Las distancias y los tiempos salen del callejero real de la capital, que ya
// esta en el sistema para dibujar los recorridos. Si no responde, se cae a una
// estimacion conservadora en lugar de fallar: el generador de carga tiene que
// poder usarse sin tener levantado el motor de rutas.

// urlOSRM es la base del motor de rutas. La fija el flag -osrm.
var urlOSRM = "http://localhost:5001"

// velocidadUrbanaKmH es la media que se supone cuando no hay callejero
// disponible.
//
// Es deliberadamente baja. Medido contra el callejero de la capital, un coche
// a flujo libre promedia entre 26 y 34 km/h en linea recta; con trafico, menos.
// Quedarse corto hace que el escenario separe los eventos de mas, que como
// mucho resulta aburrido. Pasarse hace que genere cadenas imposibles, que es
// justo lo que se quiere evitar.
const velocidadUrbanaKmH = 25.0

// factorSinuosidad convierte distancia en recta en distancia por asfalto.
//
// Medido sobre los mismos tramos: 1.69 km en recta son 2.74 por calle, 2.56
// son 3.32, 6.57 son 7.77. La relacion ronda 1.3 y se usa 1.4 para no quedarse
// corto en los tramos con menos conectividad.
const factorSinuosidad = 1.4

// TiempoDeViaje devuelve cuanto tarda un automovil entre dos coordenadas.
//
// factorTrafico multiplica el resultado: 1.0 es flujo libre —lo que hace un
// vehiculo que huye saltandose las normas— y valores mayores modelan
// congestion. Un incidente en hora pico con 1.8 no es exagerado en esta ciudad.
func TiempoDeViaje(origen, destino [2]float64, factorTrafico float64) time.Duration {
	if seg, ok := duracionSegunCallejero(origen, destino); ok {
		return time.Duration(seg*factorTrafico) * time.Second
	}

	km := distanciaRectaKm(origen, destino) * factorSinuosidad
	horas := km / velocidadUrbanaKmH
	return time.Duration(horas*3600*factorTrafico) * time.Second
}

// duracionSegunCallejero consulta el motor de rutas.
func duracionSegunCallejero(origen, destino [2]float64) (float64, bool) {
	u := fmt.Sprintf("%s/route/v1/driving/%f,%f;%f,%f?overview=false",
		urlOSRM, origen[1], origen[0], destino[1], destino[0])

	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Get(u)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, false
	}

	var cuerpo struct {
		Code   string `json:"code"`
		Routes []struct {
			Duration float64 `json:"duration"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil {
		return 0, false
	}
	if cuerpo.Code != "Ok" || len(cuerpo.Routes) == 0 {
		return 0, false
	}
	return cuerpo.Routes[0].Duration, true
}

// distanciaRectaKm es el semiverseno entre dos puntos (lat, lon).
func distanciaRectaKm(a, b [2]float64) float64 {
	const radioTierraKm = 6371.0

	rad := func(g float64) float64 { return g * math.Pi / 180 }

	dLat := rad(b[0] - a[0])
	dLon := rad(b[1] - a[1])

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a[0]))*math.Cos(rad(b[0]))*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * radioTierraKm * math.Asin(math.Sqrt(h))
}

// coordenadasDe devuelve el par (lat, lon) de una zona.
func coordenadasDe(zona string) [2]float64 {
	z := coordenadas(zona)
	return [2]float64{z.lat, z.lon}
}
