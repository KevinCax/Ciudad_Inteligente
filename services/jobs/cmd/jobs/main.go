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
	"ciudadinteligente/jobs/internal/rutas"
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

	// Recorridos por calle. Por intervalo y no a una hora fija: su resultado
	// se consulta en vivo sobre el mapa, no en el informe del dia siguiente.
	if err := plan.Registrar(scheduler.Tarea{
		Nombre: "rutas_de_alertas",
		Cada:   cfg.IntervaloRutas,
		Correr: tareaRutas(almacen, rutas.NewCliente(cfg.OSRMURL, cfg.OSRMTimeout), cfg, log),
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

// tareaRutas calcula el recorrido por calles de las alertas recientes.
//
// Corre cada minuto y no una vez al dia como las demas, porque su resultado
// se mira en vivo: calcular a las tres de la madrugada el recorrido de un
// incidente de media tarde no le sirve a nadie, el operador ya vio el mapa
// vacio.
//
// Es trabajo de este servicio y no del correlator por una razon de diseno:
// pedir una ruta es una llamada HTTP a un motor externo, y meterla en el
// camino critico de la deteccion significaria que un fallo del motor de rutas
// retrasa la emision de una alerta de incendio. El recorrido es presentacion;
// la alerta es la funcion del sistema.
func tareaRutas(almacen *store.Store, cliente *rutas.Cliente, cfg *config.Config,
	log *slog.Logger) func(context.Context, scheduler.Parametros) (scheduler.Resultado, error) {

	return func(ctx context.Context, _ scheduler.Parametros) (scheduler.Resultado, error) {
		pendientes, err := almacen.AlertasSinRuta(ctx,
			cfg.RutasHorasAtras, cfg.RutasMaxIntentos, cfg.RutasPorPasada)
		if err != nil {
			return scheduler.Resultado{}, err
		}
		if len(pendientes) == 0 {
			return scheduler.Resultado{Afectadas: 0}, nil
		}

		var calculadas, sinRuta, fallidas int64

		for _, a := range pendientes {
			puntos := puntosDistintos(a.Puntos)

			// Con un solo punto no hay recorrido que trazar. Se registra como
			// "sin ruta" en vez de dejarlo pendiente, o la tarea lo reintentaria
			// cada minuto para siempre.
			if len(puntos) < 2 {
				sinRuta++
				if err := almacen.GuardarRuta(ctx, a.AlertID, store.RutaSinRuta, 0, 0, nil,
					"la alerta ocurre en un solo punto: no hay recorrido"); err != nil {
					log.Warn("no se pudo registrar la alerta sin recorrido",
						slog.String("alert_id", a.AlertID),
						slog.String("error", err.Error()))
				}
				continue
			}

			ruta, err := cliente.Calcular(ctx, puntos)

			switch {
			case errors.Is(err, rutas.ErrSinRuta):
				sinRuta++
				_ = almacen.GuardarRuta(ctx, a.AlertID, store.RutaSinRuta, 0, 0, nil,
					"el motor no encontro camino entre los puntos")

			case err != nil:
				fallidas++
				log.Warn("fallo el calculo del recorrido",
					slog.String("alert_id", a.AlertID),
					slog.String("error", err.Error()))
				_ = almacen.GuardarRuta(ctx, a.AlertID, store.RutaFallida, 0, 0, nil, err.Error())

			default:
				calculadas++
				// Se guarda en el orden de GeoJSON, [lon, lat], que es como lo
				// devuelve el motor y como lo espera el mapa de Grafana.
				geo := make([][2]float64, 0, len(ruta.Geometria))
				for _, p := range ruta.Geometria {
					geo = append(geo, [2]float64{p.Lon, p.Lat})
				}
				if err := almacen.GuardarRuta(ctx, a.AlertID, store.RutaCalculada,
					ruta.DistanciaM, ruta.DuracionS, geo, ""); err != nil {
					log.Warn("no se pudo guardar el recorrido",
						slog.String("alert_id", a.AlertID),
						slog.String("error", err.Error()))
				}
			}
		}

		return scheduler.Resultado{
			Afectadas: calculadas + sinRuta + fallidas,
			Detalle: map[string]any{
				"calculadas": calculadas,
				"sin_ruta":   sinRuta,
				"fallidas":   fallidas,
			},
		}, nil
	}
}

// puntosDistintos descarta los puntos repetidos consecutivos.
//
// Dos lecturas en la misma esquina producen coordenadas identicas, y el motor
// de rutas las trata como un tramo de longitud cero que no aporta nada a la
// geometria pero si cuenta para el limite de puntos de la peticion.
func puntosDistintos(crudos [][2]float64) []rutas.Punto {
	var out []rutas.Punto
	for _, p := range crudos {
		if n := len(out); n > 0 && out[n-1].Lat == p[0] && out[n-1].Lon == p[1] {
			continue
		}
		out = append(out, rutas.Punto{Lat: p[0], Lon: p[1]})
	}
	return out
}
