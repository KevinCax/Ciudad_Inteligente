// Package rutas calcula el camino por calles entre los puntos donde se vio un
// vehiculo.
//
// ADVERTENCIA DE INTERPRETACION, que se repite aqui porque es donde se genera
// el dato: el sistema NO sabe por donde circulo el vehiculo. Sabe que una
// camara lo vio en varios puntos. Lo que este paquete produce es el camino que
// un motor de rutas considera mas probable entre esos puntos. Es una
// estimacion y no una medida, y asi queda marcado en la tabla.
package rutas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Punto es una coordenada por la que paso el recorrido.
type Punto struct {
	Lat float64
	Lon float64
}

// Ruta es el resultado de un calculo.
type Ruta struct {
	// Geometria son los vertices del camino, en orden. Incluye los puntos
	// intermedios de cada calle, que es lo que hace que la linea siga el
	// trazado real en lugar de cortar por encima de las manzanas.
	Geometria []Punto

	DistanciaM float64
	DuracionS  float64
}

// Cliente habla con un servidor OSRM.
type Cliente struct {
	base   string
	perfil string
	http   *http.Client
}

// NewCliente construye el cliente.
func NewCliente(base string, timeout time.Duration) *Cliente {
	return &Cliente{
		base: strings.TrimSuffix(base, "/"),
		// El perfil de automovil es el que se preproceso con car.lua. Pedir
		// otro contra el mismo indice devuelve rutas sin sentido en lugar de
		// un error, asi que no se expone como opcion.
		perfil: "driving",
		http:   &http.Client{Timeout: timeout},
	}
}

// ErrSinRuta indica que el motor respondio correctamente pero no encontro
// camino entre los puntos. Es un resultado legitimo, no un fallo: ocurre con
// coordenadas fuera del mapa o en islas sin conexion por carretera.
var ErrSinRuta = fmt.Errorf("el motor de rutas no encontro camino entre los puntos")

// Calcular pide el camino que pasa por todos los puntos, en orden.
func (c *Cliente) Calcular(ctx context.Context, puntos []Punto) (*Ruta, error) {
	if len(puntos) < 2 {
		return nil, fmt.Errorf("hacen falta al menos dos puntos, hay %d", len(puntos))
	}

	// OSRM espera lon,lat —el orden de GeoJSON— separados por punto y coma.
	// Invertirlo no da error: devuelve una ruta por otro continente.
	partes := make([]string, 0, len(puntos))
	for _, p := range puntos {
		partes = append(partes,
			strconv.FormatFloat(p.Lon, 'f', 6, 64)+","+
				strconv.FormatFloat(p.Lat, 'f', 6, 64))
	}

	q := url.Values{}
	// overview=full devuelve todos los vertices; simplified los recorta y la
	// linea deja de seguir las curvas.
	q.Set("overview", "full")
	q.Set("geometries", "geojson")
	q.Set("continue_straight", "false")

	destino := fmt.Sprintf("%s/route/v1/%s/%s?%s",
		c.base, c.perfil, strings.Join(partes, ";"), q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, destino, nil)
	if err != nil {
		return nil, fmt.Errorf("no se pudo construir la peticion: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("el motor de rutas no respondio: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("el motor de rutas devolvio %d", resp.StatusCode)
	}

	var cuerpo struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil {
		return nil, fmt.Errorf("respuesta ilegible del motor de rutas: %w", err)
	}

	// OSRM responde 200 con code distinto de "Ok" cuando no hay ruta. Tratarlo
	// como fallo haria que la tarea reintentara eternamente algo que nunca va
	// a cambiar.
	if cuerpo.Code != "Ok" || len(cuerpo.Routes) == 0 {
		return nil, ErrSinRuta
	}

	r := cuerpo.Routes[0]
	geo := make([]Punto, 0, len(r.Geometry.Coordinates))
	for _, c := range r.Geometry.Coordinates {
		geo = append(geo, Punto{Lat: c[1], Lon: c[0]})
	}

	if len(geo) == 0 {
		return nil, ErrSinRuta
	}

	return &Ruta{Geometria: geo, DistanciaM: r.Distance, DuracionS: r.Duration}, nil
}

// Ping comprueba que el motor responde y tiene el mapa cargado.
//
// Pide una ruta trivial entre dos puntos del centro de la capital en lugar de
// consultar una pagina de estado: OSRM responde a / aunque el indice este
// corrupto, y lo que interesa saber es si puede calcular.
func (c *Cliente) Ping(ctx context.Context) error {
	_, err := c.Calcular(ctx, []Punto{
		{Lat: 14.6349, Lon: -90.5069},
		{Lat: 14.6280, Lon: -90.5220},
	})
	return err
}
