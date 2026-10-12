package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Aviso es lo que se entrega a un organismo.
type Aviso struct {
	AlertID   string  `json:"alert_id"`
	Tipo      string  `json:"alert_type"`
	Zona      string  `json:"zone"`
	Score     float64 `json:"score"`
	Organismo string  `json:"organismo"`
	CreadoEn  string  `json:"created_at"`
}

// Canal entrega un aviso a su destinatario.
//
// Es una interfaz y no una funcion suelta porque la entrega real a un cuerpo
// de emergencias no es un detalle intercambiable: cada organismo tendria su
// protocolo, su autenticacion y su acuse. Dejar el punto de extension marcado
// aqui evita que el dia que exista una integracion real haya que reescribir el
// servicio entero.
type Canal interface {
	// Nombre identifica el canal en la columna canal y en las metricas.
	Nombre() string

	// Enviar entrega el aviso. Un error significa que NO se entrego.
	Enviar(ctx context.Context, a Aviso) error
}

// CanalSimulado deja constancia del aviso en el registro y nada mas.
//
// Es el canal por defecto y conviene ser explicito sobre lo que significa:
// NO existe integracion con bomberos, policia ni ambulancias. Este canal
// documenta la decision de despacho y permite demostrar la trazabilidad de
// extremo a extremo, pero nadie acude. Marcarlo como "entregado" sin esta
// advertencia seria describir como funcionalidad algo que no lo es.
type CanalSimulado struct {
	log *slog.Logger
}

// NewCanalSimulado construye el canal de registro.
func NewCanalSimulado(log *slog.Logger) *CanalSimulado {
	return &CanalSimulado{log: log}
}

// Nombre devuelve el identificador del canal.
func (c *CanalSimulado) Nombre() string { return "simulado" }

// Enviar registra el aviso. No puede fallar: escribir en el log no depende de
// ninguna dependencia externa, y devolver un error ficticio aqui falsearia las
// metricas de entrega.
func (c *CanalSimulado) Enviar(_ context.Context, a Aviso) error {
	c.log.Info("aviso despachado (canal simulado, no hay entrega real)",
		slog.String("alert_id", a.AlertID),
		slog.String("tipo", a.Tipo),
		slog.String("zona", a.Zona),
		slog.String("organismo", a.Organismo),
		slog.Float64("score", a.Score))
	return nil
}

// CanalWebhook entrega el aviso por HTTP a un receptor externo.
//
// Existe para que la extension sea demostrable y no hipotetica: apuntando la
// URL a un receptor de pruebas se comprueba el camino completo, incluido el
// manejo de un fallo de entrega. Esta apagado por defecto.
type CanalWebhook struct {
	url    string
	client *http.Client
}

// NewCanalWebhook construye el canal HTTP.
func NewCanalWebhook(url string, timeout time.Duration) *CanalWebhook {
	return &CanalWebhook{
		url:    url,
		client: &http.Client{Timeout: timeout},
	}
}

// Nombre devuelve el identificador del canal.
func (c *CanalWebhook) Nombre() string { return "webhook" }

// Enviar hace POST del aviso y exige una respuesta 2xx.
//
// Cualquier otra respuesta se trata como no entregado. En particular un 200
// con cuerpo de error cuenta como entregado, y es correcto: el contrato de un
// webhook es el codigo de estado, y adivinar la semantica del cuerpo de un
// tercero produciria reintentos que el receptor no pidio.
func (c *CanalWebhook) Enviar(ctx context.Context, a Aviso) error {
	cuerpo, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("no se pudo serializar el aviso: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(cuerpo))
	if err != nil {
		return fmt.Errorf("no se pudo construir la peticion: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo entregar el aviso: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("el receptor respondio %d", resp.StatusCode)
	}
	return nil
}
