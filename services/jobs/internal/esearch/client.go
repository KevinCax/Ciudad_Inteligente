// Package esearch opera sobre los indices de Elasticsearch.
//
// Aqui solo se necesitan tres operaciones —comprobar salud, listar indices y
// borrarlos—, todas peticiones HTTP simples. Igual que en el indexer, se usa la
// API directamente en lugar del cliente oficial, que arrastra un arbol de
// dependencias considerable a cambio de comodidades que no se usan.
package esearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client habla con Elasticsearch.
type Client struct {
	baseURL string
	http    *http.Client
	usuario string
	clave   string
}

// Opciones de construccion.
type Opciones struct {
	URL     string
	Usuario string
	Clave   string
	Timeout time.Duration
}

// New construye el cliente.
func New(o Opciones) *Client {
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(o.URL, "/"),
		usuario: o.Usuario,
		clave:   o.Clave,
		http: &http.Client{
			Timeout: o.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
			},
		},
	}
}

// Ping comprueba que el cluster responda.
func (c *Client) Ping(ctx context.Context) error {
	req, err := c.nuevaPeticion(ctx, http.MethodGet, "/_cluster/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("elasticsearch no responde: %w", err)
	}
	defer cerrar(resp)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("elasticsearch devolvio %s", resp.Status)
	}

	var salud struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&salud); err != nil {
		return fmt.Errorf("respuesta de salud ilegible: %w", err)
	}

	// "red" significa que hay particiones primarias sin asignar: escribir en
	// ese estado puede fallar o perder datos.
	if salud.Status == "red" {
		return fmt.Errorf("el cluster esta en estado rojo")
	}
	return nil
}

func (c *Client) nuevaPeticion(ctx context.Context, metodo, ruta string, cuerpo io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, metodo, c.baseURL+ruta, cuerpo)
	if err != nil {
		return nil, fmt.Errorf("no se pudo construir la peticion: %w", err)
	}
	if c.usuario != "" {
		req.SetBasicAuth(c.usuario, c.clave)
	}
	return req, nil
}

// cerrar drena y cierra el cuerpo para que la conexion pueda reutilizarse.
func cerrar(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
