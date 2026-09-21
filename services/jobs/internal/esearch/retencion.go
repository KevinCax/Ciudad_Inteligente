package esearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Indice describe un indice del cluster.
type Indice struct {
	Nombre string
	Mes    time.Time
}

// ResultadoRetencion detalla los indices eliminados.
type ResultadoRetencion struct {
	Eliminados  []string
	Conservados int
}

// PurgarIndicesAntiguos elimina los indices mensuales anteriores al limite.
//
// Aqui se cobra la decision de indexar por mes en lugar de en un indice unico
// (ver ADR-009): aplicar la retencion es borrar indices completos, una
// operacion inmediata que ademas libera el espacio en disco de una vez. Sobre
// un indice unico habria que lanzar un DELETE BY QUERY que recorre millones de
// documentos, marca cada uno como borrado y deja el indice fragmentado hasta
// que una fusion de segmentos recupere el espacio.
func (c *Client) PurgarIndicesAntiguos(ctx context.Context, prefijos []string, meses int) (ResultadoRetencion, error) {
	var res ResultadoRetencion

	limite := time.Now().UTC().AddDate(0, -meses, 0)
	// Se compara por mes, no por dia: el indice 2026.03 cubre marzo entero, y
	// solo debe borrarse cuando el mes completo quedo fuera del plazo.
	limiteMes := time.Date(limite.Year(), limite.Month(), 1, 0, 0, 0, 0, time.UTC)

	indices, err := c.listarIndices(ctx, prefijos)
	if err != nil {
		return res, err
	}

	var aBorrar []string
	for _, idx := range indices {
		if idx.Mes.Before(limiteMes) {
			aBorrar = append(aBorrar, idx.Nombre)
		} else {
			res.Conservados++
		}
	}

	if len(aBorrar) == 0 {
		return res, nil
	}

	sort.Strings(aBorrar)
	if err := c.borrarIndices(ctx, aBorrar); err != nil {
		return res, err
	}
	res.Eliminados = aBorrar

	return res, nil
}

// listarIndices devuelve los indices mensuales que coinciden con los prefijos.
func (c *Client) listarIndices(ctx context.Context, prefijos []string) ([]Indice, error) {
	patron := make([]string, 0, len(prefijos))
	for _, p := range prefijos {
		patron = append(patron, p+"-*")
	}

	req, err := c.nuevaPeticion(ctx, http.MethodGet,
		"/_cat/indices/"+strings.Join(patron, ",")+"?format=json&h=index", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron listar los indices: %w", err)
	}
	defer cerrar(resp)

	// 404 significa que todavia no existe ningun indice: no es un error.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		detalle, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("listar indices devolvio %s: %s", resp.Status, detalle)
	}

	var filas []struct {
		Index string `json:"index"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&filas); err != nil {
		return nil, fmt.Errorf("listado de indices ilegible: %w", err)
	}

	var out []Indice
	for _, f := range filas {
		mes, ok := mesDeIndice(f.Index)
		if !ok {
			// Un indice que no sigue la convencion de nombres no se toca:
			// borrar algo que no reconocemos seria peor que conservarlo.
			continue
		}
		out = append(out, Indice{Nombre: f.Index, Mes: mes})
	}
	return out, nil
}

// mesDeIndice extrae la fecha del sufijo "-AAAA.MM".
func mesDeIndice(nombre string) (time.Time, bool) {
	i := strings.LastIndex(nombre, "-")
	if i < 0 || i+1 >= len(nombre) {
		return time.Time{}, false
	}
	mes, err := time.Parse("2006.01", nombre[i+1:])
	if err != nil {
		return time.Time{}, false
	}
	return mes, true
}

func (c *Client) borrarIndices(ctx context.Context, nombres []string) error {
	req, err := c.nuevaPeticion(ctx, http.MethodDelete, "/"+strings.Join(nombres, ","), nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudieron borrar los indices: %w", err)
	}
	defer cerrar(resp)

	if resp.StatusCode >= 300 {
		detalle, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("borrar indices devolvio %s: %s", resp.Status, detalle)
	}
	return nil
}
