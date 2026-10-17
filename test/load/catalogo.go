package main

import (
	"encoding/json"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Catalogo de sensores de la ciudad.
//
// Un sensor real esta fijo: la misma camara tiene siempre las mismas
// coordenadas y el mismo identificador. Hasta ahora cada escenario inventaba
// las posiciones al vuelo, de modo que "la camara de la 6a avenida" aparecia
// en cuatro sitios distintos. Este archivo define la red UNA vez y la guarda en
// disco; la simulacion de la ciudad la lee y no la modifica.
//
// ADVERTENCIA, que tambien queda escrita dentro del propio archivo generado:
// la red es una SIMULACION. No es el inventario de la Municipalidad, que no se
// tiene. Las posiciones estan sobre calles reales (salen del callejero) y en
// los cruces mas transitados, pero la camara concreta que figura en cada una no
// existe. Cuando haya un inventario real, es lo que debe sustituir a este
// archivo.

// sensor es un dispositivo fijo de la red.
type sensor struct {
	ID   string `json:"id"`
	Tipo string `json:"tipo"` // lpr | velocidad | acustico | panico
	Zona string `json:"zona"`

	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`

	// Via es el nombre de la calle donde esta, segun el callejero.
	Via string `json:"via,omitempty"`

	// Semaforo marca las camaras situadas en un cruce semaforizado, que son las
	// unicas que pueden registrar un cruce en rojo.
	Semaforo bool `json:"semaforo,omitempty"`

	// VelRef es la velocidad tipica de circulacion libre en ese punto, en km/h,
	// tomada de la velocidad media del tramo segun el callejero.
	VelRef float64 `json:"vel_ref_kmh,omitempty"`
}

// catalogo es el archivo completo.
type catalogo struct {
	Version  string   `json:"version"`
	Nota     string   `json:"nota"`
	Generado string   `json:"generado"`
	Semilla  uint64   `json:"semilla"`
	Sensores []sensor `json:"sensores"`
}

const notaCatalogo = "RED SIMULADA. Las posiciones estan sobre calles reales y en los cruces " +
	"mas transitados segun el callejero, pero NO es el inventario de camaras y sensores de la " +
	"Municipalidad de Guatemala. Sustituir por el inventario real cuando exista."

func (c *catalogo) deTipo(tipo string) []sensor {
	var out []sensor
	for _, s := range c.Sensores {
		if s.Tipo == tipo {
			out = append(out, s)
		}
	}
	return out
}

func guardarCatalogo(ruta string, c *catalogo) error {
	if err := os.MkdirAll(filepath.Dir(ruta), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ruta, append(b, '\n'), 0o644)
}

func cargarCatalogo(ruta string) (*catalogo, error) {
	b, err := os.ReadFile(ruta)
	if err != nil {
		return nil, err
	}
	var c catalogo
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalogo ilegible: %w", err)
	}
	return &c, nil
}

// ---------------------------------------------------------------------------
// Consultas al motor de rutas
// ---------------------------------------------------------------------------

// paso es un tramo de una ruta entre dos maniobras.
type paso struct {
	lat, lon float64 // donde empieza el tramo (la maniobra)
	distM    float64
	durS     float64
	nombre   string
	tipo     string
}

// pedirPasos devuelve los tramos de la ruta entre dos puntos (lat, lon).
//
// Los puntos de maniobra son los cruces y giros del camino, que es donde se
// instalan las camaras de verdad: nadie pone una camara en mitad de una recta
// sin cruces.
func pedirPasos(origen, destino [2]float64) ([]paso, error) {
	u := fmt.Sprintf("%s/route/v1/driving/%f,%f;%f,%f?overview=false&steps=true",
		urlOSRM, origen[1], origen[0], destino[1], destino[0])

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Get(u)
	if err != nil {
		return nil, fmt.Errorf("el motor de rutas no responde: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var cuerpo struct {
		Code   string `json:"code"`
		Routes []struct {
			Legs []struct {
				Steps []struct {
					Distance float64 `json:"distance"`
					Duration float64 `json:"duration"`
					Name     string  `json:"name"`
					Maneuver struct {
						Location [2]float64 `json:"location"`
						Type     string     `json:"type"`
					} `json:"maneuver"`
				} `json:"steps"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil {
		return nil, fmt.Errorf("respuesta ilegible: %w", err)
	}
	if cuerpo.Code != "Ok" || len(cuerpo.Routes) == 0 {
		return nil, fmt.Errorf("no hay camino")
	}

	var out []paso
	for _, leg := range cuerpo.Routes[0].Legs {
		for _, s := range leg.Steps {
			out = append(out, paso{
				lat: s.Maneuver.Location[1], lon: s.Maneuver.Location[0],
				distM: s.Distance, durS: s.Duration, nombre: s.Name, tipo: s.Maneuver.Type,
			})
		}
	}
	return out, nil
}

// ajustarACalle lleva un punto a la calle mas cercana y devuelve su nombre.
func ajustarACalle(p [2]float64) ([2]float64, string, error) {
	u := fmt.Sprintf("%s/nearest/v1/driving/%f,%f?number=1", urlOSRM, p[1], p[0])

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(u)
	if err != nil {
		return p, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var cuerpo struct {
		Code      string `json:"code"`
		Waypoints []struct {
			Name     string     `json:"name"`
			Location [2]float64 `json:"location"`
		} `json:"waypoints"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil || cuerpo.Code != "Ok" ||
		len(cuerpo.Waypoints) == 0 {
		return p, "", fmt.Errorf("no se pudo ajustar el punto a la calle")
	}
	w := cuerpo.Waypoints[0]
	return [2]float64{w.Location[1], w.Location[0]}, w.Name, nil
}

// ---------------------------------------------------------------------------
// Generacion
// ---------------------------------------------------------------------------

// celda agrupa las maniobras que caen en un mismo cruce.
type celda struct {
	lat, lon float64
	hits     int
	sumVel   float64
	n        int
	via      string
}

func (c *celda) velMedia() float64 {
	if c.n == 0 {
		return 0
	}
	return c.sumVel / float64(c.n)
}

// generarCatalogo construye la red de sensores sobre el callejero.
//
// El criterio es que las camaras esten donde pasa la gente. Se calculan rutas
// entre cada zona y sus vecinas mas proximas, y se cuenta cuantas pasan por cada
// cruce: los cruces por los que pasan mas rutas son los que reciben camara. Asi
// la red queda concentrada en las arterias y sin huecos en el centro, que es
// como se instalaria de verdad, y no repartida al azar por la ciudad.
func generarCatalogo(semilla uint64, maxLPR int) (*catalogo, error) {
	rng := mrand.New(mrand.NewPCG(semilla, semilla^0x9e3779b97f4a7c15))

	// Aristas: cada zona con sus tres vecinas mas cercanas, sin repetir.
	type arista [2]int
	visto := map[arista]bool{}
	var aristas []arista
	for i := range zonas {
		idx := make([]int, 0, len(zonas)-1)
		for j := range zonas {
			if j != i {
				idx = append(idx, j)
			}
		}
		sort.Slice(idx, func(a, b int) bool {
			return distanciaRectaKm(coordenadasDe(zonas[i].nombre), coordenadasDe(zonas[idx[a]].nombre)) <
				distanciaRectaKm(coordenadasDe(zonas[i].nombre), coordenadasDe(zonas[idx[b]].nombre))
		})
		for _, j := range idx[:3] {
			a := arista{min(i, j), max(i, j)}
			if !visto[a] {
				visto[a] = true
				aristas = append(aristas, a)
			}
		}
	}
	sort.Slice(aristas, func(a, b int) bool {
		if aristas[a][0] != aristas[b][0] {
			return aristas[a][0] < aristas[b][0]
		}
		return aristas[a][1] < aristas[b][1]
	})

	celdas := map[[2]int]*celda{}
	for n, a := range aristas {
		pasos, err := pedirPasos(coordenadasDe(zonas[a[0]].nombre), coordenadasDe(zonas[a[1]].nombre))
		if err != nil {
			return nil, fmt.Errorf("ruta %s -> %s: %w", zonas[a[0]].nombre, zonas[a[1]].nombre, err)
		}
		for _, p := range pasos {
			if p.tipo == "depart" || p.tipo == "arrive" || p.distM < 40 {
				continue
			}
			k := [2]int{int(p.lat * 650), int(p.lon * 650)} // celdas de ~170 m
			c := celdas[k]
			if c == nil {
				c = &celda{lat: p.lat, lon: p.lon}
				celdas[k] = c
			}
			c.hits++
			if p.durS > 0 {
				c.sumVel += p.distM / p.durS * 3.6
				c.n++
			}
			if c.via == "" {
				c.via = p.nombre
			}
		}
		if (n+1)%10 == 0 {
			fmt.Printf("  analizadas %d de %d rutas entre zonas\n", n+1, len(aristas))
		}
	}

	ordenadas := make([]*celda, 0, len(celdas))
	for _, c := range celdas {
		ordenadas = append(ordenadas, c)
	}
	sort.Slice(ordenadas, func(a, b int) bool {
		x, y := ordenadas[a], ordenadas[b]
		if x.hits != y.hits {
			return x.hits > y.hits
		}
		if x.lat != y.lat {
			return x.lat < y.lat
		}
		return x.lon < y.lon
	})
	fmt.Printf("  %d cruces candidatos\n", len(ordenadas))

	usadas := map[*celda]bool{}

	// elegir toma celdas por orden de trafico respetando una distancia minima
	// entre las de su misma categoria.
	elegir := func(minKm float64, max int, filtro func(*celda) bool) []*celda {
		var out []*celda
		for _, c := range ordenadas {
			if len(out) >= max {
				break
			}
			if usadas[c] || (filtro != nil && !filtro(c)) {
				continue
			}
			cerca := false
			for _, o := range out {
				if distanciaRectaKm([2]float64{c.lat, c.lon}, [2]float64{o.lat, o.lon}) < minKm {
					cerca = true
					break
				}
			}
			if !cerca {
				out = append(out, c)
				usadas[c] = true
			}
		}
		return out
	}

	cat := &catalogo{
		Version:  "1.0",
		Nota:     notaCatalogo,
		Generado: time.Now().UTC().Format(time.RFC3339),
		Semilla:  semilla,
	}

	velRef := func(c *celda) float64 {
		// Velocidad media del tramo segun el callejero, llevada a circulacion
		// libre. Se acota: un tramo de 8 km/h por un semaforo no es la velocidad
		// a la que pasa un coche, y uno de 110 no se da en una ciudad.
		return math.Round(math.Min(95, math.Max(28, c.velMedia()*1.25)))
	}

	// Camaras de lectura de placas, en los cruces mas transitados.
	for i, c := range elegir(0.55, maxLPR, nil) {
		z := zonaMasCercana([2]float64{c.lat, c.lon})
		cat.Sensores = append(cat.Sensores, sensor{
			ID: fmt.Sprintf("LPR-%03d", i+1), Tipo: "lpr", Zona: z,
			Lat: redondear(c.lat), Lon: redondear(c.lon), Via: c.via,
			Semaforo: c.via != "" && rng.Float64() < 0.35,
			VelRef:   velRef(c),
		})
	}

	// Radares de velocidad, en tramos rapidos.
	for i, c := range elegir(1.2, 18, func(c *celda) bool { return c.velMedia() >= 50 }) {
		z := zonaMasCercana([2]float64{c.lat, c.lon})
		cat.Sensores = append(cat.Sensores, sensor{
			ID: fmt.Sprintf("RAD-%02d", i+1), Tipo: "velocidad", Zona: z,
			Lat: redondear(c.lat), Lon: redondear(c.lon), Via: c.via, VelRef: velRef(c),
		})
	}

	// Sensores acusticos, repartidos por la zona mas densa.
	for i, c := range elegir(0.9, 28, nil) {
		z := zonaMasCercana([2]float64{c.lat, c.lon})
		cat.Sensores = append(cat.Sensores, sensor{
			ID: fmt.Sprintf("ACU-%02d", i+1), Tipo: "acustico", Zona: z,
			Lat: redondear(c.lat), Lon: redondear(c.lon), Via: c.via,
		})
	}

	// Botones de panico: dos por zona, en la calle mas cercana a un punto
	// proximo al centro. Son puntos publicos (parques, paradas, plazas).
	var botones []sensor
	for _, zn := range zonas {
		for k := 0; k < 2; k++ {
			p := [2]float64{zn.lat + (rng.Float64()-0.5)*0.008, zn.lon + (rng.Float64()-0.5)*0.008}
			aj, via, err := ajustarACalle(p)
			if err != nil {
				continue
			}
			demasiadoCerca := false
			for _, b := range botones {
				if distanciaRectaKm(aj, [2]float64{b.Lat, b.Lon}) < 0.2 {
					demasiadoCerca = true
					break
				}
			}
			if demasiadoCerca {
				continue
			}
			botones = append(botones, sensor{
				ID:   fmt.Sprintf("BTN-%s-%d", zn.nombre[5:], k+1),
				Tipo: "panico", Zona: zonaMasCercana(aj),
				Lat: redondear(aj[0]), Lon: redondear(aj[1]), Via: via,
			})
		}
	}
	cat.Sensores = append(cat.Sensores, botones...)

	return cat, nil
}

// resumen imprime cuantos sensores hay de cada tipo y por zona.
func (c *catalogo) resumen() {
	porTipo := map[string]int{}
	porZona := map[string]int{}
	for _, s := range c.Sensores {
		porTipo[s.Tipo]++
		if s.Tipo == "lpr" {
			porZona[s.Zona]++
		}
	}
	fmt.Printf("  sensores: %d camaras LPR, %d radares, %d acusticos, %d botones de panico\n",
		porTipo["lpr"], porTipo["velocidad"], porTipo["acustico"], porTipo["panico"])

	sem := 0
	for _, s := range c.Sensores {
		if s.Semaforo {
			sem++
		}
	}
	fmt.Printf("  %d camaras en cruces semaforizados\n", sem)

	sinCamara := 0
	for _, z := range zonas {
		if porZona[z.nombre] == 0 {
			sinCamara++
		}
	}
	fmt.Printf("  zonas sin ninguna camara LPR: %d de %d\n", sinCamara, len(zonas))
}
