// Package broker publica eventos en Kafka y errores en la dead-letter queue.
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

// Publisher publica en el topic de eventos y en la DLQ.
type Publisher struct {
	eventos *kafka.Writer
	dlq     *kafka.Writer
	brokers []string
}

// New construye el publicador.
//
// Dos decisiones que no son el valor por defecto de la libreria y que
// importan:
//
//   - Balancer Murmur2: kafka-go reparte por round-robin si no se indica otra
//     cosa, ignorando por completo la clave del mensaje. Con el default, el
//     partition_key no decidiria nada y se perderia la afinidad por zona en la
//     que se apoya la correlacion. Murmur2 ademas reproduce el particionado
//     del cliente Java, de modo que durante la migracion en paralelo una misma
//     clave cae en la misma particion en ambos sistemas y las salidas son
//     comparables.
//
//   - BatchTimeout bajo: el valor por defecto es 1 segundo, que en escrituras
//     sincronas de un mensaje se traduce en hasta 1s de latencia por peticion.
//     Con un SLO de alerta en menos de 2 segundos extremo a extremo, ese solo
//     parametro se comeria la mitad del presupuesto.
func New(brokers []string, topicEventos, topicDLQ string) *Publisher {
	return &Publisher{
		brokers: brokers,
		eventos: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topicEventos,
			Balancer:     &kafka.Murmur2Balancer{},
			RequiredAcks: kafka.RequireAll,
			Async:        false,
			BatchTimeout: 10 * time.Millisecond,
			WriteTimeout: 10 * time.Second,
			MaxAttempts:  3,
		},
		dlq: &kafka.Writer{
			Addr:         kafka.TCP(brokers...),
			Topic:        topicDLQ,
			Balancer:     &kafka.Murmur2Balancer{},
			RequiredAcks: kafka.RequireAll,
			Async:        false,
			BatchTimeout: 10 * time.Millisecond,
			WriteTimeout: 10 * time.Second,
			MaxAttempts:  2,
		},
	}
}

// Close cierra ambos writers.
func (p *Publisher) Close() error {
	var primero error
	if err := p.eventos.Close(); err != nil {
		primero = err
	}
	if err := p.dlq.Close(); err != nil && primero == nil {
		primero = err
	}
	return primero
}

// Publish escribe el evento y espera confirmacion de todas las replicas.
//
// Es sincrono a proposito. El servicio Java publicaba de forma asincrona y
// respondia 202 sin esperar: si la publicacion fallaba, el cliente ya se habia
// llevado un "aceptado", el evento quedaba solo en la base y el correlator
// nunca lo veia. Aqui el 202 significa que Kafka confirmo.
func (p *Publisher) Publish(ctx context.Context, clave string, valor []byte, cabeceras map[string]string) error {
	msg := kafka.Message{
		Key:     []byte(clave),
		Value:   valor,
		Headers: aCabeceras(cabeceras),
		Time:    time.Now().UTC(),
	}
	if err := p.eventos.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("no se pudo publicar en %s: %w", p.eventos.Topic, err)
	}
	return nil
}

// MensajeDLQ es el formato de los mensajes de la dead-letter queue.
//
// Lleva el payload original intacto para poder reprocesar el evento una vez
// corregida la causa, y el motivo estructurado para poder agrupar por tipo de
// fallo en los dashboards.
type MensajeDLQ struct {
	Servicio  string          `json:"service"`
	Motivo    string          `json:"reason"`
	Detalle   any             `json:"detail,omitempty"`
	TraceID   string          `json:"trace_id,omitempty"`
	FallidoEn string          `json:"failed_at"`
	Original  json.RawMessage `json:"original_payload"`
}

// PublishDLQ envia a la dead-letter queue lo que no se pudo procesar.
//
// El enunciado exige tratar explicitamente las colas de mensajes muertos
// (seccion 4.2, resiliencia y tolerancia a fallos). El sistema Java descartaba
// en silencio todo lo que fallaba.
func (p *Publisher) PublishDLQ(ctx context.Context, motivo string, detalle any, traceID string, original []byte) error {
	cuerpo, err := json.Marshal(MensajeDLQ{
		Servicio:  "ingestor",
		Motivo:    motivo,
		Detalle:   detalle,
		TraceID:   traceID,
		FallidoEn: time.Now().UTC().Format(time.RFC3339Nano),
		Original:  json.RawMessage(original),
	})
	if err != nil {
		return fmt.Errorf("no se pudo serializar el mensaje de DLQ: %w", err)
	}

	clave := traceID
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
//
// kafka-go no expone un health check propio, asi que se pide metadata: es la
// operacion mas barata que confirma que hay un broker vivo y alcanzable.
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

func aCabeceras(m map[string]string) []kafka.Header {
	if len(m) == 0 {
		return nil
	}
	hs := make([]kafka.Header, 0, len(m))
	for k, v := range m {
		hs = append(hs, kafka.Header{Key: k, Value: []byte(v)})
	}
	return hs
}
