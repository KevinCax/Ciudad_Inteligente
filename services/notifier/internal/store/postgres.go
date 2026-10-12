// Package store registra en PostgreSQL los avisos emitidos a terceros.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store es el acceso a la tabla notifications.
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

// Aviso es una fila de notifications pendiente de escribir.
type Aviso struct {
	AlertID   string
	AlertType string
	Zona      string
	Organismo string
	Canal     string
	Estado    string
	Detalle   string
	EnviadoEn *time.Time
}

// Estados posibles de un aviso. Coinciden con el CHECK de la tabla: si
// divergieran, la base rechazaria la fila y el fallo apareceria como un error
// de escritura sin explicacion.
const (
	EstadoPendiente = "pendiente"
	EstadoEntregado = "entregado"
	EstadoFallido   = "fallido"
)

// RegistrarAviso escribe el aviso y dice si era nuevo.
//
// La idempotencia la da la restriccion UNIQUE (alert_id, organismo), no una
// comprobacion previa en el codigo: consultar y despues escribir deja una
// ventana de carrera por la que dos instancias del notificador avisarian dos
// veces del mismo incendio. ON CONFLICT lo resuelve en una sola operacion
// atomica dentro de la base.
//
// Devuelve false cuando la fila ya existia, que es lo normal tras un
// rebalanceo de Kafka o un reinicio y no constituye un error.
func (s *Store) RegistrarAviso(ctx context.Context, a Aviso) (bool, error) {
	alertID, err := uuid.Parse(a.AlertID)
	if err != nil {
		return false, &ErrUUIDInvalido{Campo: "alert_id", Valor: a.AlertID}
	}

	const sql = `
		INSERT INTO notifications (
			notification_id, alert_id, alert_type, zone,
			organismo, canal, estado, intentos, enviado_en, detalle
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, $9)
		ON CONFLICT (alert_id, organismo) DO NOTHING`

	tag, err := s.pool.Exec(ctx, sql,
		uuid.New(), alertID, a.AlertType, textoONulo(a.Zona),
		a.Organismo, a.Canal, a.Estado, a.EnviadoEn, textoONulo(a.Detalle))
	if err != nil {
		return false, fmt.Errorf("no se pudo registrar el aviso: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

// textoONulo convierte la cadena vacia en NULL.
//
// Importa para poder distinguir en una consulta "no hubo detalle" de "el
// detalle era una cadena vacia", y porque un indice no almacena los NULL.
func textoONulo(v string) any {
	if v == "" {
		return nil
	}
	return v
}
