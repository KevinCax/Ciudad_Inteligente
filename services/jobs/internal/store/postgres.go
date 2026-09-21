// Package store implementa las tareas que operan sobre PostgreSQL.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store es el acceso a la base de datos.
type Store struct {
	pool *pgxpool.Pool
}

// New abre el pool y verifica que la base responda.
//
// El pool es pequeno a proposito: estas tareas corren una vez al dia y no
// compiten por conexiones con los servicios en linea, que son los que de
// verdad las necesitan.
func New(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL invalida: %w", err)
	}

	cfg.MaxConns = 4
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 45 * time.Minute
	cfg.MaxConnIdleTime = 10 * time.Minute

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

// ---------------------------------------------------------------------------
// Retencion
// ---------------------------------------------------------------------------

// PoliticaRetencion son los plazos de degradacion del historico.
//
// Implementa la politica propuesta en el ADR-007. La idea es que el dato pierda
// capacidad de identificar conforme envejece, en lugar de conservarse intacto
// para siempre o desaparecer de golpe.
type PoliticaRetencion struct {
	// DiasPlaca: a partir de aqui se elimina el seudonimo de la placa. El
	// evento sigue sirviendo para estadisticas de trafico, pero deja de poder
	// asociarse a un vehiculo concreto.
	DiasPlaca int
	// DiasCoordenadas: a partir de aqui se eliminan lat y lon. Se conserva la
	// zona, que basta para analisis por sector y no permite reconstruir un
	// recorrido con precision.
	DiasCoordenadas int
	// DiasBorrado: a partir de aqui el evento se elimina. Lo que queda del
	// periodo son los agregados diarios.
	DiasBorrado int
}

// ResultadoRetencion detalla que hizo la purga.
type ResultadoRetencion struct {
	PlacasEliminadas      int64
	CoordenadasEliminadas int64
	EventosBorrados       int64
}

// Total suma las filas afectadas.
func (r ResultadoRetencion) Total() int64 {
	return r.PlacasEliminadas + r.CoordenadasEliminadas + r.EventosBorrados
}

// AplicarRetencion degrada y purga el historico segun la politica.
//
// Las tres fases se aplican de mas reciente a mas antigua y cada una en su
// propia sentencia, no en una transaccion unica: son operaciones sobre
// volumenes potencialmente grandes y conviene que un fallo en la ultima no
// deshaga el trabajo de las anteriores. Todas son idempotentes —repetirlas no
// cambia el resultado—, de modo que reintentar es seguro.
func (s *Store) AplicarRetencion(ctx context.Context, p PoliticaRetencion) (ResultadoRetencion, error) {
	var res ResultadoRetencion

	// Fase 1: quitar el seudonimo de la placa.
	// La condicion sobre el operador ? evita reescribir filas que ya fueron
	// purgadas en ejecuciones anteriores.
	const quitarPlaca = `
		UPDATE events
		SET payload = payload - 'placa_vehicular'
		WHERE ts_utc < now() - make_interval(days => $1)
		  AND payload ? 'placa_vehicular'`

	tag, err := s.pool.Exec(ctx, quitarPlaca, p.DiasPlaca)
	if err != nil {
		return res, fmt.Errorf("no se pudieron eliminar las placas antiguas: %w", err)
	}
	res.PlacasEliminadas = tag.RowsAffected()

	// Fase 2: quitar las coordenadas, conservando la zona.
	const quitarCoordenadas = `
		UPDATE events
		SET geo_lat = NULL, geo_lon = NULL
		WHERE ts_utc < now() - make_interval(days => $1)
		  AND (geo_lat IS NOT NULL OR geo_lon IS NOT NULL)`

	tag, err = s.pool.Exec(ctx, quitarCoordenadas, p.DiasCoordenadas)
	if err != nil {
		return res, fmt.Errorf("no se pudieron eliminar las coordenadas antiguas: %w", err)
	}
	res.CoordenadasEliminadas = tag.RowsAffected()

	// Fase 3: borrar lo que ya solo existe como agregado.
	const borrar = `DELETE FROM events WHERE ts_utc < now() - make_interval(days => $1)`

	tag, err = s.pool.Exec(ctx, borrar, p.DiasBorrado)
	if err != nil {
		return res, fmt.Errorf("no se pudieron borrar los eventos antiguos: %w", err)
	}
	res.EventosBorrados = tag.RowsAffected()

	return res, nil
}

// ---------------------------------------------------------------------------
// Agregados diarios
// ---------------------------------------------------------------------------

// ResultadoAgregados detalla las filas de resumen calculadas.
type ResultadoAgregados struct {
	FilasEventos int64
	FilasAlertas int64
	Dia          string
}

// Total suma las filas escritas.
func (r ResultadoAgregados) Total() int64 { return r.FilasEventos + r.FilasAlertas }

// CalcularAgregados resume la actividad de un dia en las tablas de metricas.
//
// Se recalcula el dia completo con ON CONFLICT DO UPDATE en lugar de insertar
// solo lo que falta: asi una segunda ejecucion corrige el resultado en vez de
// duplicarlo, que es lo que permite reintentar sin pensarlo. Si el dia se
// reprocesa tras una purga, los numeros reflejaran lo que quede en la base, de
// modo que conviene que la retencion corra despues del agregado, no antes.
func (s *Store) CalcularAgregados(ctx context.Context, dia time.Time) (ResultadoAgregados, error) {
	res := ResultadoAgregados{Dia: dia.Format("2006-01-02")}

	const agregarEventos = `
		INSERT INTO daily_event_metrics (dia, zone, event_type, eventos)
		SELECT $1::date,
		       coalesce(zone, 'desconocida'),
		       event_type,
		       count(*)
		FROM events
		WHERE ts_utc >= $1::date AND ts_utc < $1::date + 1
		GROUP BY 1, 2, 3
		ON CONFLICT (dia, zone, event_type)
		DO UPDATE SET eventos = EXCLUDED.eventos, calculado_en = now()`

	tag, err := s.pool.Exec(ctx, agregarEventos, dia)
	if err != nil {
		return res, fmt.Errorf("no se pudieron agregar los eventos: %w", err)
	}
	res.FilasEventos = tag.RowsAffected()

	const agregarAlertas = `
		INSERT INTO daily_alert_metrics (dia, zone, type, alertas, score_promedio)
		SELECT $1::date,
		       coalesce(zone, 'desconocida'),
		       type,
		       count(*),
		       avg(score)
		FROM alerts
		WHERE created_at >= $1::date AND created_at < $1::date + 1
		GROUP BY 1, 2, 3
		ON CONFLICT (dia, zone, type)
		DO UPDATE SET alertas = EXCLUDED.alertas,
		              score_promedio = EXCLUDED.score_promedio,
		              calculado_en = now()`

	tag, err = s.pool.Exec(ctx, agregarAlertas, dia)
	if err != nil {
		return res, fmt.Errorf("no se pudieron agregar las alertas: %w", err)
	}
	res.FilasAlertas = tag.RowsAffected()

	return res, nil
}
