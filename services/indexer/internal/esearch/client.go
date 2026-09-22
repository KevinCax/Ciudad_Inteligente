// Package esearch indexa documentos en Elasticsearch.
//
// Usa la API HTTP directamente en lugar del cliente oficial. La operacion que
// se necesita es una sola —_bulk— y su formato es JSON delimitado por saltos de
// linea, trivial de construir con la biblioteca estandar. El cliente oficial
// arrastra un arbol de dependencias considerable a cambio de comodidades que
// aqui no se usan, y el ADR-002 fijó como criterio mantener el numero de
// dependencias justificable una a una.
package esearch

import (
	"bytes"
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

// Documento es una unidad a indexar.
type Documento struct {
	Indice string
	ID     string
	Cuerpo any
}

// ResultadoBulk resume que paso con un lote.
type ResultadoBulk struct {
	Indexados int
	Fallidos  []FalloDocumento
}

// FalloDocumento identifica un documento que Elasticsearch rechazo.
type FalloDocumento struct {
	ID     string
	Motivo string
}

// Bulk indexa varios documentos en una sola peticion.
//
// Se usa la operacion "index" y no "create": reindexar un documento existente
// lo sobrescribe en lugar de fallar. Eso hace la indexacion idempotente, que es
// lo que permite reprocesar un lote tras un reinicio sin efectos secundarios
// (ver ADR-006).
func (c *Client) Bulk(ctx context.Context, docs []Documento) (ResultadoBulk, error) {
	var res ResultadoBulk
	if len(docs) == 0 {
		return res, nil
	}

	var cuerpo bytes.Buffer
	for _, d := range docs {
		accion := map[string]any{
			"index": map[string]string{"_index": d.Indice, "_id": d.ID},
		}
		if err := json.NewEncoder(&cuerpo).Encode(accion); err != nil {
			return res, fmt.Errorf("no se pudo construir la accion bulk: %w", err)
		}
		if err := json.NewEncoder(&cuerpo).Encode(d.Cuerpo); err != nil {
			return res, fmt.Errorf("no se pudo serializar el documento %s: %w", d.ID, err)
		}
	}

	req, err := c.nuevaPeticion(ctx, http.MethodPost, "/_bulk", &cuerpo)
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := c.http.Do(req)
	if err != nil {
		return res, fmt.Errorf("fallo la peticion bulk: %w", err)
	}
	defer cerrar(resp)

	if resp.StatusCode >= 300 {
		detalle, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return res, fmt.Errorf("bulk devolvio %s: %s", resp.Status, detalle)
	}

	// La respuesta trae un resultado por documento. Un 200 global no implica
	// que todos se hayan indexado: hay que recorrer los elementos.
	var salida struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&salida); err != nil {
		return res, fmt.Errorf("respuesta bulk ilegible: %w", err)
	}

	for _, item := range salida.Items {
		for _, r := range item {
			if r.Error != nil {
				res.Fallidos = append(res.Fallidos, FalloDocumento{
					ID:     r.ID,
					Motivo: r.Error.Type + ": " + r.Error.Reason,
				})
			} else {
				res.Indexados++
			}
		}
	}

	return res, nil
}

// AsegurarPlantilla crea o actualiza una plantilla de indice.
//
// Se aplica al arrancar y es idempotente. Tenerla en el codigo evita que
// cargar las plantillas sea un comando manual que alguien deba recordar: si se
// olvidara, Elasticsearch deduciria los tipos por su cuenta y las coordenadas
// dejarian de ser consultables como geolocalizacion.
func (c *Client) AsegurarPlantilla(ctx context.Context, nombre string, plantilla []byte) error {
	req, err := c.nuevaPeticion(ctx, http.MethodPut, "/_index_template/"+nombre, bytes.NewReader(plantilla))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo aplicar la plantilla %s: %w", nombre, err)
	}
	defer cerrar(resp)

	if resp.StatusCode >= 300 {
		detalle, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("la plantilla %s fue rechazada (%s): %s", nombre, resp.Status, detalle)
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
