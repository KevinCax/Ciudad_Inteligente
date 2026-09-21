// Command jobs ejecuta las tareas periodicas del sistema.
//
// Sustituye a Apache Airflow (ver ADR-009). Son dos tareas nocturnas
// independientes entre si: calcular los agregados del dia anterior y aplicar
// la politica de retencion del ADR-007. Para eso, Airflow —servidor web,
// planificador y base de metadatos propia— resultaba desproporcionado.
//
// El cerrojo distribuido en Redis cumple dos funciones a la vez: impide que dos
// instancias ejecuten la misma purga, y garantiza que cada tarea corra una sola
// vez al dia aunque el proceso se reinicie.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"ciudadinteligente/jobs/internal/config"
	"ciudadinteligente/jobs/internal/esearch"
	"ciudadinteligente/jobs/internal/httpapi"
	"ciudadinteligente/jobs/internal/obs"
	"ciudadinteligente/jobs/internal/scheduler"
	"ciudadinteligente/jobs/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fallo al arrancar el servicio de tareas: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("jobs", cfg.LogLevel, cfg.LogFormat)
	metrics := obs.NewMetrics()

	log.Info("configuracion cargada",
		slog.String("env", cfg.Env),
		slog.String("puerto", cfg.Port),
		slog.String("zona_horaria", cfg.ZonaHoraria),
		slog.String("hora_agregados", cfg.HoraAgregados),
		slog.String("hora_retencion", cfg.HoraRetencion),
		slog.Int("retencion_dias_placa", cfg.DiasPlaca),
		slog.Int("retencion_dias_coordenadas", cfg.DiasCoordenadas),
		slog.Int("retencion_dias_borrado", cfg.DiasBorrado),
		slog.Int("retencion_meses_indices", cfg.MesesIndices),
		slog.Bool("purga_indices", cfg.ElasticEnabled()),
		slog.Bool("auth_habilitada", cfg.AuthEnabled()),
	)

	if !cfg.AuthEnabled() {
		log.Warn("API_TOKEN no configurado: la ejecucion manual de tareas queda abierta")
	}

	arranque, cancelArranque := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelArranque()

	almacen, err := store.New(arranque, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer almacen.Close()
	log.Info("conectado a postgres")

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	defer func() { _ = rdb.Close() }()

	if err := rdb.Ping(arranque).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	log.Info("conectado a redis")

	var es *esearch.Client
	if cfg.ElasticEnabled() {
		es = esearch.New(esearch.Opciones{URL: cfg.ElasticURL})
		if err := es.Ping(arranque); err != nil {
			// Elasticsearch no es imprescindible: sin el, la purga de indices
			// se omite pero la retencion en PostgreSQL sigue funcionando.
			log.Warn("elasticsearch no responde, la purga de indices quedara desactivada",
				slog.String("error", err.Error()))
			es = nil
		} else {
			log.Info("conectado a elasticsearch")
		}
	}

	plan, err := scheduler.New(rdb, cfg.ZonaHoraria, metrics, log)
	if err != nil {
		return err
	}

	// El orden importa: los agregados corren ANTES que la retencion. Si la
	// purga borrara los eventos primero, el resumen del dia saldria incompleto
	// y esa perdida seria irreversible, porque el agregado es justamente lo
	// que sobrevive al borrado.
	if err := plan.Registrar(scheduler.Tarea{
		Nombre: "agregados_diarios",
		Hora:   cfg.HoraAgregados,
		Correr: tareaAgregados(almacen, log),
	}); err != nil {
		return err
	}

	if err := plan.Registrar(scheduler.Tarea{
		Nombre: "retencion",
		Hora:   cfg.HoraRetencion,
		Correr: tareaRetencion(almacen, es, cfg, log),
	}); err != nil {
		return err
	}

	api := httpapi.NewAPI(plan, almacen, cfg.APIToken, log, salud(almacen, rdb, cfg))

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", api.Health)
	mux.HandleFunc("GET /tareas", api.Listar)
	mux.Handle("POST /tareas/{nombre}/ejecutar", api.Proteger(http.HandlerFunc(api.Ejecutar)))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("endpoints de salud y metricas escuchando", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("el servidor HTTP fallo: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		plan.Correr(ctx)
	}()

	select {
	case err := <-errCh:
		stop()
		return err
	case <-ctx.Done():
		log.Info("senal de apagado recibida, cerrando")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("apagado forzado del servidor HTTP", slog.String("error", err.Error()))
	}

	wg.Wait()
	log.Info("servicio de tareas detenido limpiamente")
	return nil
}

// tareaAgregados resume la actividad del dia anterior.
func tareaAgregados(almacen *store.Store, log *slog.Logger) func(context.Context, scheduler.Parametros) (scheduler.Resultado, error) {
	return func(ctx context.Context, params scheduler.Parametros) (scheduler.Resultado, error) {
		// Por defecto, el dia anterior: a las tres de la madrugada el dia en
		// curso apenas ha empezado y su resumen no significaria nada.
		// Una ejecucion manual puede pedir otra fecha, que es el backfill
		// minimo que sustituye a esa funcion de Airflow.
		dia := time.Now().AddDate(0, 0, -1)
		if d, ok := params.Dia(); ok {
			dia = d
			log.Info("recalculando una fecha concreta", slog.String("dia", d.Format("2006-01-02")))
		}

		res, err := almacen.CalcularAgregados(ctx, dia)
		if err != nil {
			return scheduler.Resultado{}, err
		}

		return scheduler.Resultado{
			Afectadas: res.Total(),
			Detalle: map[string]any{
				"dia":           res.Dia,
				"filas_eventos": res.FilasEventos,
				"filas_alertas": res.FilasAlertas,
			},
		}, nil
	}
}

// tareaRetencion degrada y purga el historico segun el ADR-007.
func tareaRetencion(almacen *store.Store, es *esearch.Client, cfg *config.Config,
	log *slog.Logger) func(context.Context, scheduler.Parametros) (scheduler.Resultado, error) {

	return func(ctx context.Context, _ scheduler.Parametros) (scheduler.Resultado, error) {
		pg, err := almacen.AplicarRetencion(ctx, store.PoliticaRetencion{
			DiasPlaca:       cfg.DiasPlaca,
			DiasCoordenadas: cfg.DiasCoordenadas,
			DiasBorrado:     cfg.DiasBorrado,
		})
		if err != nil {
			return scheduler.Resultado{}, err
		}

		detalle := map[string]any{
			"placas_eliminadas":      pg.PlacasEliminadas,
			"coordenadas_eliminadas": pg.CoordenadasEliminadas,
			"eventos_borrados":       pg.EventosBorrados,
		}
		total := pg.Total()

		// La purga de indices no bloquea la tarea: si Elasticsearch falla, la
		// retencion en PostgreSQL —que es la que cumple la obligacion legal—
		// ya quedo aplicada y no conviene deshacerla ni repetirla.
		if es != nil {
			idx, err := es.PurgarIndicesAntiguos(ctx,
				[]string{"events", "alerts"}, cfg.MesesIndices)
			if err != nil {
				log.Error("no se pudieron purgar los indices antiguos",
					slog.String("error", err.Error()))
				detalle["indices_error"] = err.Error()
			} else {
				detalle["indices_eliminados"] = idx.Eliminados
				detalle["indices_conservados"] = idx.Conservados
				total += int64(len(idx.Eliminados))
			}
		}

		return scheduler.Resultado{Afectadas: total, Detalle: detalle}, nil
	}
}

// salud comprueba las dependencias.
func salud(almacen *store.Store, rdb *redis.Client, cfg *config.Config) func(context.Context) map[string]any {
	return func(ctx context.Context) map[string]any {
		checks := map[string]any{}
		estado := "UP"

		if err := almacen.Ping(ctx); err != nil {
			checks["postgres"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["postgres"] = "available"
		}

		if err := rdb.Ping(ctx).Err(); err != nil {
			checks["redis"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["redis"] = "available"
		}

		checks["zona_horaria"] = cfg.ZonaHoraria
		checks["retencion"] = strings.Join([]string{
			fmt.Sprintf("placa=%dd", cfg.DiasPlaca),
			fmt.Sprintf("coordenadas=%dd", cfg.DiasCoordenadas),
			fmt.Sprintf("borrado=%dd", cfg.DiasBorrado),
		}, " ")

		return map[string]any{
			"status":    estado,
			"service":   "jobs",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"checks":    checks,
		}
	}
}
