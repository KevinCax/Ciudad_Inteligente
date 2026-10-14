package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Este archivo pide al motor de rutas un viaje real entre dos puntos de la
// ciudad y permite colocar sensores SOBRE ese viaje.
//
// Importa porque el escenario anterior inventaba las posiciones: cada lectura
// se dispersaba al azar hasta 890 m alrededor del centro de una zona. Una
// huida generada asi hace zigzag —un coche que sale de A, pasa por una camara al
// norte y luego sigue al este— y el recorrido dibujado vuelve sobre sus pasos,
// que es algo que ningun vehiculo hace. Los datos eran validos para las reglas
// y absurdos para cualquiera que mirase el mapa.
//
// Un viaje real es A -> B. Las camaras estan en el camino, no al lado.

// urlOSRM es la base del motor de rutas. La fija el flag -osrm.
var urlOSRM = "http://localhost:5001"

// rutaCalle es un camino por calles calculado por el motor de rutas.
type rutaCalle struct {
	// vertices son los puntos (lat, lon) del trazado, en orden.
	vertices [][2]float64

	// acumulado[i] son los metros recorridos desde el inicio hasta vertices[i].
	acumulado []float64

	distanciaM float64
	duracionS  float64
}

// pedirRuta calcula el camino por calles entre dos coordenadas (lat, lon).
func pedirRuta(origen, destino [2]float64) (*rutaCalle, error) {
	u := fmt.Sprintf("%s/route/v1/driving/%f,%f;%f,%f?overview=full&geometries=geojson",
		urlOSRM, origen[1], origen[0], destino[1], destino[0])

	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Get(u)
	if err != nil {
		return nil, fmt.Errorf("el motor de rutas no responde: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("el motor de rutas devolvio %d", resp.StatusCode)
	}

	var cuerpo struct {
		Code   string `json:"code"`
		Routes []struct {
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil {
		return nil, fmt.Errorf("respuesta ilegible: %w", err)
	}
	if cuerpo.Code != "Ok" || len(cuerpo.Routes) == 0 || len(cuerpo.Routes[0].Geometry.Coordinates) < 2 {
		return nil, fmt.Errorf("no hay camino entre los puntos")
	}

	r := &rutaCalle{duracionS: cuerpo.Routes[0].Duration}

	// OSRM entrega [lon, lat]; en el resto del programa es (lat, lon).
	for i, c := range cuerpo.Routes[0].Geometry.Coordinates {
		v := [2]float64{c[1], c[0]}
		if i == 0 {
			r.acumulado = append(r.acumulado, 0)
		} else {
			r.acumulado = append(r.acumulado,
				r.acumulado[i-1]+distanciaRectaKm(r.vertices[i-1], v)*1000)
		}
		r.vertices = append(r.vertices, v)
	}
	r.distanciaM = r.acumulado[len(r.acumulado)-1]

	return r, nil
}

// en devuelve el punto situado a d metros del inicio, SOBRE el trazado.
//
// Se interpola dentro del tramo en lugar de tomar el vertice mas cercano: los
// vertices de una calle recta pueden estar a cientos de metros, y redondear al
// vertice colocaria el sensor lejos de donde se pidio.
func (r *rutaCalle) en(d float64) [2]float64 {
	if d <= 0 {
		return r.vertices[0]
	}
	if d >= r.distanciaM {
		return r.vertices[len(r.vertices)-1]
	}

	for i := 1; i < len(r.acumulado); i++ {
		if r.acumulado[i] < d {
			continue
		}
		tramo := r.acumulado[i] - r.acumulado[i-1]
		if tramo <= 0 {
			return r.vertices[i]
		}
		f := (d - r.acumulado[i-1]) / tramo
		a, b := r.vertices[i-1], r.vertices[i]
		return [2]float64{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f}
	}
	return r.vertices[len(r.vertices)-1]
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

// zonaMasCercana asigna un punto a la zona cuyo centro le queda mas proximo.
//
// Es una aproximacion: las zonas reales no son celdas de Voronoi. Sirve para
// etiquetar sensores situados a lo largo de un viaje sin tener a mano el
// poligono de cada zona, y es coherente con como se reparten los eventos entre
// particiones de Kafka (por la etiqueta, no por la geometria).
func zonaMasCercana(p [2]float64) string {
	mejor, mejorKm := zonas[0].nombre, math.MaxFloat64
	for _, z := range zonas {
		if km := distanciaRectaKm(p, [2]float64{z.lat, z.lon}); km < mejorKm {
			mejor, mejorKm = z.nombre, km
		}
	}
	return mejor
}
