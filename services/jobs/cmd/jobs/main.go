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
	"math"
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
			"recorridos_borrados":    pg.RecorridosBorrados,
			"avisos_borrados":        pg.AvisosBorrados,
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

// tareaRutas calcula el recorrido por calles de las alertas recientes, de
// principio a ultimo contacto.
//
// Corre cada minuto y no una vez al dia como las demas, porque su resultado
// se mira en vivo: calcular a las tres de la madrugada el recorrido de un
// incidente de media tarde no le sirve a nadie, el operador ya vio el mapa
// vacio.
//
// Recalcula, ademas de calcular. La alerta se emite en cuanto el patron se
// completa y el vehiculo sigue circulando despues: cada camara que lo vea de
// nuevo alarga el rastro, y la ruta que se calculo al nacer la alerta se queda
// corta. Por eso se compara el numero de puntos con el que tenia la ruta y se
// rehace cuando hay mas.
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
			cfg.RutasHorasAtras, cfg.RutasMaxIntentos, cfg.RutasPorPasada, cfg.RutasSilencioMin)
		if err != nil {
			return scheduler.Resultado{}, err
		}
		if len(pendientes) == 0 {
			return scheduler.Resultado{Afectadas: 0}, nil
		}

		var calculadas, sinRuta, fallidas int64

		for _, a := range pendientes {
			// Puntos cuenta los que HAY, no los que se usan: la tarea compara este
			// numero con el del rastro para saber si la ruta se quedo atras, y ese
			// numero sale de la consulta, sin filtrar.
			calculo := store.Calculo{
				AlertID: a.AlertID,
				Puntos:  len(a.Puntos),
			}

			// Se descartan los puntos a los que el vehiculo no pudo llegar: una
			// placa mal leida o clonada deja una lectura a 8 km de la anterior, 25
			// segundos despues, y forzar la ruta a pasar por ahi dibuja un recorrido
			// de 12 km en un minuto.
			a.Puntos = plausibles(a.Puntos, cfg.RutasVelMaxKmH)
			if n := len(a.Puntos); n > 0 {
				calculo.Desde, calculo.Hasta = a.Puntos[0].Momento, a.Puntos[n-1].Momento
			}

			// Si no hay un vehiculo huyendo de un pulsador, no hay recorrido que
			// trazar. Se deja constancia explicita en lugar de omitir la alerta:
			// sin la fila, la alerta pareceria tener el calculo pendiente para
			// siempre y nadie sabria distinguir "todavia no" de "no aplica".
			if !a.Aplica {
				sinRuta++
				calculo.Estado = store.RutaSinRuta
				calculo.Detalle = "no aplica: el recorrido solo se traza cuando hay un " +
					"boton de panico y un unico vehiculo implicado"
				if err := almacen.GuardarRuta(ctx, calculo); err != nil {
					log.Warn("no se pudo registrar la alerta sin recorrido",
						slog.String("alert_id", a.AlertID),
						slog.String("error", err.Error()))
				}
				continue
			}

			puntos := puntosDistintos(a.Puntos)

			// Con un solo punto no hay recorrido que trazar. Se registra como
			// "sin ruta" en vez de dejarlo pendiente, o la tarea lo reintentaria
			// cada minuto para siempre.
			if len(puntos) < 2 {
				sinRuta++
				calculo.Estado = store.RutaSinRuta
				calculo.Detalle = "el incidente ocurre en un solo punto: no hay recorrido"
				if err := almacen.GuardarRuta(ctx, calculo); err != nil {
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
				calculo.Estado = store.RutaSinRuta
				calculo.Detalle = "el motor no encontro camino entre los puntos"

			case err != nil:
				fallidas++
				calculo.Estado = store.RutaFallida
				calculo.Detalle = err.Error()
				log.Warn("fallo el calculo del recorrido",
					slog.String("alert_id", a.AlertID),
					slog.String("error", err.Error()))

			default:
				calculadas++
				// Se guarda en el orden de GeoJSON, [lon, lat], que es como lo
				// devuelve el motor y como lo espera el mapa de Grafana.
				geo := make([][2]float64, 0, len(ruta.Geometria))
				for _, p := range ruta.Geometria {
					geo = append(geo, [2]float64{p.Lon, p.Lat})
				}
				calculo.Estado = store.RutaCalculada
				calculo.DistanciaM, calculo.DuracionS = ruta.DistanciaM, ruta.DuracionS
				calculo.Geometria = geo
			}

			if err := almacen.GuardarRuta(ctx, calculo); err != nil {
				log.Warn("no se pudo guardar el recorrido",
					slog.String("alert_id", a.AlertID),
					slog.String("error", err.Error()))
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

// plausibles descarta los puntos a los que el vehiculo no pudo llegar desde el
// ultimo punto ACEPTADO.
//
// Es el mismo criterio que aplica el correlator al encadenar infracciones, y se
// mide contra el ultimo aceptado y no contra el anterior: si no, una sola lectura
// falsa desplazaria la referencia y arrastraria consigo a las buenas que vinieran
// despues. Se descuentan 300 m de margen, porque los sensores no tienen precision
// de GPS.
func plausibles(ps []store.Punto, maxKmH float64) []store.Punto {
	if maxKmH <= 0 || len(ps) < 2 {
		return ps
	}

	out := []store.Punto{ps[0]}
	for _, p := range ps[1:] {
		ult := out[len(out)-1]
		km := distanciaKm(ult.Lat, ult.Lon, p.Lat, p.Lon) - 0.3
		horas := p.Momento.Sub(ult.Momento).Hours()

		if km <= 0 || (horas > 0 && km/horas <= maxKmH) {
			out = append(out, p)
		}
	}
	return out
}

// distanciaKm es el semiverseno entre dos puntos.
func distanciaKm(lat1, lon1, lat2, lon2 float64) float64 {
	const radioTierraKm = 6371.0
	rad := func(g float64) float64 { return g * math.Pi / 180 }

	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * radioTierraKm * math.Asin(math.Sqrt(h))
}

// puntosDistintos descarta los puntos repetidos consecutivos.
//
// Dos lecturas en la misma esquina producen coordenadas identicas, y el motor
// de rutas las trata como un tramo de longitud cero que no aporta nada a la
// geometria pero si cuenta para el limite de puntos de la peticion.
func puntosDistintos(crudos []store.Punto) []rutas.Punto {
	var out []rutas.Punto
	for _, p := range crudos {
		if n := len(out); n > 0 && out[n-1].Lat == p.Lat && out[n-1].Lon == p.Lon {
			continue
		}
		out = append(out, rutas.Punto{Lat: p.Lat, Lon: p.Lon})
	}
	return out
}
