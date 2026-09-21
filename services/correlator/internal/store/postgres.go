// Package store consulta las alertas archivadas en PostgreSQL.
//
// Es de SOLO LECTURA. El correlator no escribe en la nube: publica la alerta
// en Kafka y el servicio persister la archiva. Aqui solo se sirve el endpoint
// de consulta /alerts/db, que no esta en el camino critico de deteccion.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store es el acceso a la base de datos de alertas.
type Store struct {
	pool *pgxpool.Pool
}

// New abre el pool de conexiones y verifica que la base responda.
func New(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL invalida: %w", err)
	}

	cfg.MaxConns = 10
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

// AlertaPersistida es una fila de la tabla alerts.
type AlertaPersistida struct {
	AlertID       string    `json:"alert_id"`
	CorrelationID *string   `json:"correlation_id,omitempty"`
	Type          string    `json:"type"`
	Score         *float64  `json:"score,omitempty"`
	Zone          *string   `json:"zone,omitempty"`
	WindowStart   *string   `json:"window_start,omitempty"`
	WindowEnd     *string   `json:"window_end,omitempty"`
	Evidence      []string  `json:"evidence"`
	CreatedAt     time.Time `json:"created_at"`
}

// AlertasPorZona consulta las alertas persistidas, opcionalmente filtradas.
//
// El limite es obligatorio: el endpoint equivalente del sistema Java hacia
// findAll() sin filtro ni tope y devolvia la tabla entera en cada llamada.
func (s *Store) AlertasPorZona(ctx context.Context, zona string, limite int) ([]AlertaPersistida, error) {
	if limite <= 0 || limite > 500 {
		limite = 100
	}

	var filas pgx.Rows
	var err error

	if zona == "" {
		const q = `
			SELECT alert_id, correlation_id, type, score, zone,
			       window_start, window_end, evidence, created_at
			FROM alerts ORDER BY created_at DESC LIMIT $1`
		filas, err = s.pool.Query(ctx, q, limite)
	} else {
		const q = `
			SELECT alert_id, correlation_id, type, score, zone,
			       window_start, window_end, evidence, created_at
			FROM alerts WHERE zone = $1 ORDER BY created_at DESC LIMIT $2`
		filas, err = s.pool.Query(ctx, q, zona, limite)
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudieron consultar las alertas: %w", err)
	}
	defer filas.Close()

	var out []AlertaPersistida
	for filas.Next() {
		var (
			a           AlertaPersistida
			alertID     uuid.UUID
			correlation *uuid.UUID
			inicio, fin *time.Time
			evidencia   []byte
		)

		if err := filas.Scan(&alertID, &correlation, &a.Type, &a.Score, &a.Zone,
			&inicio, &fin, &evidencia, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("no se pudo leer la fila: %w", err)
		}

		a.AlertID = alertID.String()
		if correlation != nil {
			s := correlation.String()
			a.CorrelationID = &s
		}
		a.WindowStart = formatOpcional(inicio)
		a.WindowEnd = formatOpcional(fin)
		if len(evidencia) > 0 {
			_ = json.Unmarshal(evidencia, &a.Evidence)
		}

		out = append(out, a)
	}
	if err := filas.Err(); err != nil {
		return nil, fmt.Errorf("error recorriendo las alertas: %w", err)
	}

	return out, nil
}

func formatOpcional(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
