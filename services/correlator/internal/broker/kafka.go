// Package broker consume eventos de Kafka y publica alertas y errores.
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

// Consumer lee eventos canonicos del topic de entrada.
type Consumer struct {
	reader *kafka.Reader
}

// OpcionesConsumer configura el consumidor.
type OpcionesConsumer struct {
	Brokers []string
	Topic   string
	Grupo   string

	// DesdeElPrincipio decide si un grupo nuevo empieza por el offset mas
	// antiguo disponible.
	//
	// Por defecto es false, es decir, se arranca desde el final, y la razon
	// importa: el TTL de idempotencia es de 10 minutos, mucho menor que la
	// retencion del topic. Leer desde el principio al reiniciar reprocesaria
	// hasta 72 horas de eventos cuyas claves de deduplicacion ya expiraron, y
	// regeneraria en masa alertas historicas, todas con la fecha de creacion
	// del momento del reinicio.
	//
	// Se pone en true solo para reprocesar un historico a proposito.
	DesdeElPrincipio bool
}

// NewConsumer construye el consumidor.
func NewConsumer(o OpcionesConsumer) *Consumer {
	inicio := kafka.LastOffset
	if o.DesdeElPrincipio {
		inicio = kafka.FirstOffset
	}

	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:     o.Brokers,
			Topic:       o.Topic,
			GroupID:     o.Grupo,
			StartOffset: inicio,
			MinBytes:    1,
			MaxBytes:    10 << 20,

			// CommitInterval en cero desactiva el commit automatico: el
			// offset se confirma explicitamente tras procesar el mensaje.
			// Con commit automatico, un fallo de procesamiento deja el offset
			// avanzado igual y el evento se pierde en silencio.
			CommitInterval: 0,

			MaxAttempts:    3,
			SessionTimeout: 45 * time.Second,
			ReadBackoffMax: 1 * time.Second,
		}),
	}
}

// Close cierra el consumidor y libera la membresia del grupo.
func (c *Consumer) Close() error { return c.reader.Close() }

// Mensaje es un evento recibido pendiente de confirmar.
type Mensaje struct {
	Valor     []byte
	Clave     string
	TraceID   string
	Particion int
	Offset    int64
	raw       kafka.Message
}

// Leer bloquea hasta recibir un mensaje o hasta que se cancele el contexto.
func (c *Consumer) Leer(ctx context.Context) (*Mensaje, error) {
	m, err := c.reader.FetchMessage(ctx)
	if err != nil {
		return nil, err
	}

	return &Mensaje{
		Valor:     m.Value,
		Clave:     string(m.Key),
		TraceID:   cabecera(m, "trace_id"),
		Particion: m.Partition,
		Offset:    m.Offset,
		raw:       m,
	}, nil
}

// Confirmar avanza el offset del mensaje.
//
// Se llama despues de procesar, tanto si el evento se correlaciono como si se
// descarto hacia la DLQ: en ambos casos el mensaje quedo atendido y bloquear
// la particion reintentandolo solo detendria el resto de la zona.
func (c *Consumer) Confirmar(ctx context.Context, m *Mensaje) error {
	return c.reader.CommitMessages(ctx, m.raw)
}

// Retraso devuelve el lag del consumidor, util como metrica de salud.
func (c *Consumer) Retraso() int64 { return c.reader.Lag() }

func cabecera(m kafka.Message, clave string) string {
	for _, h := range m.Headers {
		if h.Key == clave {
			return string(h.Value)
		}
	}
	return ""
}

// Publisher publica alertas correlacionadas y mensajes de DLQ.
type Publisher struct {
	alertas *kafka.Writer
	dlq     *kafka.Writer
	brokers []string
}

// NewPublisher construye el publicador.
//
// Igual que en el ingestor: balanceador Murmur2 para que la clave decida la
// particion (kafka-go usa round-robin si no se indica, ignorando la clave) y
// BatchTimeout bajo para no gastar el presupuesto del SLO en el batching por
// defecto de un segundo.
func NewPublisher(brokers []string, topicAlertas, topicDLQ string) *Publisher {
	nuevo := func(topic string, intentos int) *kafka.Writer {
		return &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topic,
			Balancer:     &kafka.Murmur2Balancer{},
			RequiredAcks: kafka.RequireAll,
			Async:        false,
			BatchTimeout: 10 * time.Millisecond,
			WriteTimeout: 10 * time.Second,
			MaxAttempts:  intentos,
		}
	}

	return &Publisher{
		alertas: nuevo(topicAlertas, 3),
		dlq:     nuevo(topicDLQ, 2),
		brokers: brokers,
	}
}

// Close cierra ambos writers.
func (p *Publisher) Close() error {
	var primero error
	if err := p.alertas.Close(); err != nil {
		primero = err
	}
	if err := p.dlq.Close(); err != nil && primero == nil {
		primero = err
	}
	return primero
}

// PublicarAlerta escribe la alerta y espera confirmacion de todas las replicas.
//
// La clave es la zona: todas las alertas de una zona caen en la misma
// particion y conservan su orden relativo, que es lo que necesitan los
// consumidores aguas abajo para reconstruir la cronologia de un incidente.
func (p *Publisher) PublicarAlerta(ctx context.Context, zona string, alerta []byte, traceID string) error {
	msg := kafka.Message{
		Key:   []byte(zona),
		Value: alerta,
		Time:  time.Now().UTC(),
	}
	if traceID != "" {
		msg.Headers = []kafka.Header{{Key: "trace_id", Value: []byte(traceID)}}
	}

	if err := p.alertas.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("no se pudo publicar la alerta en %s: %w", p.alertas.Topic, err)
	}
	return nil
}

// MensajeDLQ es el formato de los mensajes de la dead-letter queue.
type MensajeDLQ struct {
	Servicio  string          `json:"service"`
	Motivo    string          `json:"reason"`
	Detalle   any             `json:"detail,omitempty"`
	TraceID   string          `json:"trace_id,omitempty"`
	Particion int             `json:"partition"`
	Offset    int64           `json:"offset"`
	FallidoEn string          `json:"failed_at"`
	Original  json.RawMessage `json:"original_payload"`
}

// PublicarDLQ envia a la dead-letter queue lo que no se pudo procesar.
//
// Limitarse a registrar el error y seguir haria que un evento mal formado
// desapareciera sin rastro. Aqui queda el payload original intacto para poder
// reprocesarlo, junto con su particion y offset.
func (p *Publisher) PublicarDLQ(ctx context.Context, motivo string, detalle any, m *Mensaje) error {
	cuerpo, err := json.Marshal(MensajeDLQ{
		Servicio:  "correlator",
		Motivo:    motivo,
		Detalle:   detalle,
		TraceID:   m.TraceID,
		Particion: m.Particion,
		Offset:    m.Offset,
		FallidoEn: time.Now().UTC().Format(time.RFC3339Nano),
		Original:  json.RawMessage(m.Valor),
	})
	if err != nil {
		return fmt.Errorf("no se pudo serializar el mensaje de DLQ: %w", err)
	}

	clave := m.TraceID
	if clave == "" {
		clave = motivo
	}

	if err := p.dlq.WriteMessages(ctx, kafka.Message{
		Key:   []byte(clave),
		Value: cuerpo,
		Time:  time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("no se pudo publicar en la DLQ: %w", err)
	}
	return nil
}

// Ping comprueba que al menos un broker responda metadatos.
func (p *Publisher) Ping(ctx context.Context) error {
	if len(p.brokers) == 0 {
		return fmt.Errorf("no hay brokers configurados")
	}
	conn, err := kafka.DialContext(ctx, "tcp", p.brokers[0])
	if err != nil {
		return fmt.Errorf("no se pudo conectar a %s: %w", p.brokers[0], err)
	}
	defer conn.Close()

	if _, err := conn.Brokers(); err != nil {
		return fmt.Errorf("el broker no respondio metadatos: %w", err)
	}
	return nil
}
