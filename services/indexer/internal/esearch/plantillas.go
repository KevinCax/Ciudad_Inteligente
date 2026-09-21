package esearch

import (
	"context"
	_ "embed"
	"fmt"
)

// Plantillas de indice, versionadas junto al codigo y aplicadas al arrancar.
//
// El mapeo explicito importa: sin el, Elasticsearch deduce los tipos del primer
// documento que recibe. Las coordenadas quedarian como dos numeros sueltos en
// lugar de un geo_point, y el mapa de calor de Grafana no funcionaria.
//
//go:embed plantillas/events.json
var plantillaEventos []byte

//go:embed plantillas/alerts.json
var plantillaAlertas []byte

// AsegurarPlantillas aplica ambas plantillas. Es idempotente.
func (c *Client) AsegurarPlantillas(ctx context.Context) error {
	if err := c.AsegurarPlantilla(ctx, "ciudad-eventos", plantillaEventos); err != nil {
		return fmt.Errorf("plantilla de eventos: %w", err)
	}
	if err := c.AsegurarPlantilla(ctx, "ciudad-alertas", plantillaAlertas); err != nil {
		return fmt.Errorf("plantilla de alertas: %w", err)
	}
	return nil
}
