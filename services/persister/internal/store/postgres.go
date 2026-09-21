// Package store escribe eventos canonicos en PostgreSQL.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ciudadinteligente/persister/internal/canonical"
)

// Store es el acceso a la base de datos de eventos.
type Store struct {
	pool *pgxpool.Pool
}

// New abre el pool de conexiones y verifica que la base responda.
func New(ctx context.Context, url string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL invalida: %w", err)
	}

	cfg.MaxConns = maxConns
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 45 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear el pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("la base no responde: %w", err)
	}

	return &Store{pool: pool}, nil
}

// Close libera el pool.
func (s *Store) Close() { s.pool.Close() }

// Ping verifica que la base siga respondiendo.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// ErrUUIDInvalido indica que un campo que la base exige como uuid no lo es.
type ErrUUIDInvalido struct {
	Campo string
	Valor string
}

func (e *ErrUUIDInvalido) Error() string {
	return fmt.Sprintf("%s debe ser un UUID valido, se recibio %q", e.Campo, e.Valor)
}

// ResultadoLote resume que paso con cada evento de un lote.
type ResultadoLote struct {
	// Insertados son los eventos nuevos que quedaron en la base.
	Insertados int
	// Duplicados son los que ya existian. No es un error: significa que el
	// evento se reproceso, cosa normal tras un rebalanceo de Kafka o un
	// reinicio del servicio.
	Duplicados int
	// Rechazados son los que no se pudieron escribir por su contenido.
	Rechazados []EventoRechazado
}

// EventoRechazado es un evento que no pudo persistirse, con su motivo.
type EventoRechazado struct {
	Evento *canonical.Event
	Motivo string
}

const insertarEvento = `
	INSERT INTO events (
		event_id, event_type, event_version, producer, source,
		correlation_id, trace_id, partition_key, ts_utc,
		zone, geo_lat, geo_lon, severity, payload
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	ON CONFLICT (event_id) DO NOTHING`

// InsertarLote escribe varios eventos en un solo viaje a la base.
//
// Escribir por lotes es la ventaja concreta de haber sacado la persistencia
// del camino critico: cuando el INSERT ocurria dentro de la peticion HTTP,
// cada evento pagaba su propia ida y vuelta contra una base que esta en la
// nube. Agrupando cien eventos en un lote, ese costo se paga una vez.
//
// Los eventos mal formados no abortan el lote: se apartan con su motivo para
// que el llamante los derive a la cola de mensajes muertos, y el resto se
// escribe igual.
func (s *Store) InsertarLote(ctx context.Context, eventos []*canonical.Event) (ResultadoLote, error) {
	var res ResultadoLote
	if len(eventos) == 0 {
		return res, nil
	}

	lote := &pgx.Batch{}
	aplicables := make([]*canonical.Event, 0, len(eventos))

	for _, ev := range eventos {
		args, err := argumentosDe(ev)
		if err != nil {
			res.Rechazados = append(res.Rechazados, EventoRechazado{Evento: ev, Motivo: err.Error()})
			continue
		}
		lote.Queue(insertarEvento, args...)
		aplicables = append(aplicables, ev)
	}

	if lote.Len() == 0 {
		return res, nil
	}

	resultados := s.pool.SendBatch(ctx, lote)
	defer func() { _ = resultados.Close() }()

	for i := range aplicables {
		tag, err := resultados.Exec()
		if err != nil {
			// Un fallo aqui suele ser de conexion y afecta al lote entero:
			// se devuelve el error para que el llamante no confirme offsets
			// y el lote se reintente.
			return res, fmt.Errorf("fallo al escribir el evento %d del lote: %w", i, err)
		}
		if tag.RowsAffected() == 0 {
			res.Duplicados++
		} else {
			res.Insertados++
		}
	}

	if err := resultados.Close(); err != nil {
		return res, fmt.Errorf("fallo al cerrar el lote: %w", err)
	}

	return res, nil
}

// argumentosDe convierte un evento en los parametros del INSERT.
func argumentosDe(ev *canonical.Event) ([]any, error) {
	eventID, err := uuid.Parse(ev.EventID)
	if err != nil {
		return nil, &ErrUUIDInvalido{Campo: "event_id", Valor: ev.EventID}
	}

	correlationID, err := uuidOrNil("correlation_id", ev.CorrelationID)
	if err != nil {
		return nil, err
	}
	traceID, err := uuidOrNil("trace_id", ev.TraceID)
	if err != nil {
		return nil, err
	}

	ts, err := ev.TimestampUTC()
	if err != nil {
		return nil, fmt.Errorf("timestamp invalido: %w", err)
	}

	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload no serializable: %w", err)
	}

	return []any{
		eventID, ev.EventType, ev.EventVersion, ev.Producer, ev.Source,
		correlationID, traceID, ev.PartitionKey, ts,
		ev.Geo.Zone, ev.Geo.Lat, ev.Geo.Lon, ev.Severity, payload,
	}, nil
}

// uuidOrNil convierte un campo opcional a *uuid.UUID.
//
// Vacio se persiste como NULL; un valor presente pero mal formado se rechaza
// en lugar de guardarse como NULL en silencio: perder la trazabilidad sin
// dejar rastro es peor que derivar el evento a la cola de mensajes muertos,
// donde alguien puede revisarlo.
func uuidOrNil(campo, valor string) (*uuid.UUID, error) {
	if valor == "" {
		return nil, nil
	}
	id, err := uuid.Parse(valor)
	if err != nil {
		return nil, &ErrUUIDInvalido{Campo: campo, Valor: valor}
	}
	return &id, nil
}

// EsUUIDInvalido indica si el error viene de un identificador mal formado.
func EsUUIDInvalido(err error) bool {
	var e *ErrUUIDInvalido
	return errors.As(err, &e)
}

// ---------------------------------------------------------------------------
// Alertas
// ---------------------------------------------------------------------------

const insertarAlerta = `
	INSERT INTO alerts (
		alert_id, correlation_id, type, score, zone,
		window_start, window_end, evidence, created_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	ON CONFLICT (alert_id) DO NOTHING`

// AlertaRechazada es una alerta que no pudo archivarse, con su motivo.
type AlertaRechazada struct {
	Alerta *canonical.Alert
	Motivo string
}

// ResultadoLoteAlertas resume que paso con cada alerta de un lote.
type ResultadoLoteAlertas struct {
	Insertadas int
	Duplicadas int
	Rechazadas []AlertaRechazada
}

// InsertarLoteAlertas escribe varias alertas en un solo viaje a la base.
//
// El score se guarda como float64 sobre la columna numeric. La entidad del
// sistema Java lo declaraba Integer y lo redondeaba, de modo que 0.85, 0.90 y
// 0.95 acababan los tres como 1 y los dashboards mostraban un dato que no
// correspondia a ninguna de las reglas.
func (s *Store) InsertarLoteAlertas(ctx context.Context, alertas []*canonical.Alert) (ResultadoLoteAlertas, error) {
	var res ResultadoLoteAlertas
	if len(alertas) == 0 {
		return res, nil
	}

	lote := &pgx.Batch{}
	aplicables := make([]*canonical.Alert, 0, len(alertas))

	for _, a := range alertas {
		args, err := argumentosDeAlerta(a)
		if err != nil {
			res.Rechazadas = append(res.Rechazadas, AlertaRechazada{Alerta: a, Motivo: err.Error()})
			continue
		}
		lote.Queue(insertarAlerta, args...)
		aplicables = append(aplicables, a)
	}

	if lote.Len() == 0 {
		return res, nil
	}

	resultados := s.pool.SendBatch(ctx, lote)
	defer func() { _ = resultados.Close() }()

	for i := range aplicables {
		tag, err := resultados.Exec()
		if err != nil {
			return res, fmt.Errorf("fallo al escribir la alerta %d del lote: %w", i, err)
		}
		if tag.RowsAffected() == 0 {
			res.Duplicadas++
		} else {
			res.Insertadas++
		}
	}

	if err := resultados.Close(); err != nil {
		return res, fmt.Errorf("fallo al cerrar el lote de alertas: %w", err)
	}

	return res, nil
}

func argumentosDeAlerta(a *canonical.Alert) ([]any, error) {
	alertID, err := uuid.Parse(a.AlertID)
	if err != nil {
		return nil, &ErrUUIDInvalido{Campo: "alert_id", Valor: a.AlertID}
	}

	// Un correlation_id mal formado no justifica perder la alerta: se archiva
	// como NULL y la alerta sigue siendo util para la investigacion.
	var correlationID *uuid.UUID
	if a.CorrelationID != "" {
		if id, err := uuid.Parse(a.CorrelationID); err == nil {
			correlationID = &id
		}
	}

	inicio, err := parseOpcional(a.Window.Start)
	if err != nil {
		return nil, fmt.Errorf("window.start invalido: %w", err)
	}
	fin, err := parseOpcional(a.Window.End)
	if err != nil {
		return nil, fmt.Errorf("window.end invalido: %w", err)
	}

	creada, err := time.Parse(time.RFC3339Nano, a.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("created_at invalido: %w", err)
	}

	evidencia, err := json.Marshal(a.Evidence)
	if err != nil {
		return nil, fmt.Errorf("evidencia no serializable: %w", err)
	}

	return []any{
		alertID, correlationID, a.Type, a.Score, a.Zone,
		inicio, fin, evidencia, creada,
	}, nil
}

func parseOpcional(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
