// Command indexer indexa en Elasticsearch los eventos y alertas que circulan
// por Kafka.
//
// Sustituye al ETL por lotes del sistema Java, que copiaba de PostgreSQL a
// Elasticsearch una vez por hora: una busqueda podia no encontrar un incidente
// ocurrido cincuenta minutos antes. Al consumir directamente del broker, la
// busqueda queda al dia en segundos (ver ADR-009).
//
// Escribe fuera de la LAN, asi que —como el persister— esta sobre la frontera
// de privacidad y tokeniza la placa vehicular antes de que el dato cruce
// (ver ADR-007).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"ciudadinteligente/indexer/internal/broker"
	"ciudadinteligente/indexer/internal/canonical"
	"ciudadinteligente/indexer/internal/config"
	"ciudadinteligente/indexer/internal/esearch"
	"ciudadinteligente/indexer/internal/httpapi"
	"ciudadinteligente/indexer/internal/index"
	"ciudadinteligente/indexer/internal/obs"
	"ciudadinteligente/indexer/internal/privacy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fallo al arrancar el indexer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("indexer", cfg.LogLevel, cfg.LogFormat)
	metrics := obs.NewMetrics()

	validador, err := canonical.NewValidator()
	if err != nil {
		return fmt.Errorf("no se pudo inicializar el validador: %w", err)
	}

	log.Info("configuracion cargada",
		slog.String("env", cfg.Env),
		slog.String("puerto", cfg.Port),
		slog.Any("kafka_brokers", cfg.KafkaBrokers),
		slog.String("topic_eventos", cfg.TopicEvents),
		slog.String("topic_alertas", cfg.TopicAlerts),
		slog.String("topic_dlq", cfg.TopicDLQ),
		slog.String("consumer_group", cfg.ConsumerGroup),
		slog.Int("tamano_lote", cfg.TamanoLote),
		slog.Duration("espera_lote", cfg.EsperaLote),
		slog.Bool("tokenizacion_placas", cfg.PrivacyEnabled()),
	)

	if !cfg.PrivacyEnabled() {
		log.Warn("PLATE_HMAC_KEY no configurada: las placas se archivaran en claro en la nube")
	}

	arranque, cancelArranque := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelArranque()

	es := esearch.New(esearch.Opciones{
		URL:     cfg.ElasticURL,
		Usuario: cfg.ElasticUsuario,
		Clave:   cfg.ElasticClave,
	})
	if err := es.Ping(arranque); err != nil {
		return fmt.Errorf("elasticsearch: %w", err)
	}
	log.Info("conectado a elasticsearch", slog.String("url", cfg.ElasticURL))

	// Las plantillas se aplican al arrancar y son idempotentes. Sin ellas,
	// Elasticsearch deduciria los tipos del primer documento y las coordenadas
	// dejarian de ser consultables como geolocalizacion.
	if err := es.AsegurarPlantillas(arranque); err != nil {
		return fmt.Errorf("plantillas de indice: %w", err)
	}
	log.Info("plantillas de indice aplicadas")

	publicador := broker.NewPublisher(cfg.KafkaBrokers, cfg.TopicDLQ)
	defer func() {
		if err := publicador.Close(); err != nil {
			log.Error("fallo al cerrar el publicador", slog.String("error", err.Error()))
		}
	}()

	// Dos consumidores independientes, cada uno con su propio grupo: los
	// eventos y las alertas avanzan a su ritmo y un atasco en uno no frena
	// al otro.
	consEventos := broker.NewConsumer(broker.OpcionesConsumer{
		Brokers:          cfg.KafkaBrokers,
		Topic:            cfg.TopicEvents,
		Grupo:            cfg.ConsumerGroup,
		DesdeElPrincipio: cfg.ConsumeFromStart,
	})
	defer func() {
		if err := consEventos.Close(); err != nil {
			log.Error("fallo al cerrar el consumidor de eventos", slog.String("error", err.Error()))
		}
	}()

	consAlertas := broker.NewConsumer(broker.OpcionesConsumer{
		Brokers:          cfg.KafkaBrokers,
		Topic:            cfg.TopicAlerts,
		Grupo:            cfg.ConsumerGroup + "-alerts",
		DesdeElPrincipio: cfg.ConsumeFromStart,
	})
	defer func() {
		if err := consAlertas.Close(); err != nil {
			log.Error("fallo al cerrar el consumidor de alertas", slog.String("error", err.Error()))
		}
	}()

	tokenizador := privacy.NewTokenizador(cfg.PlateHMACKey)
	servicio := index.New(es, publicador, validador, tokenizador, metrics, log)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", healthHandler(es, publicador, cfg))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 3)

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
		if err := consumir(ctx, consEventos, servicio.IndexarEventos, "eventos", cfg, log); err != nil {
			errCh <- err
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := consumir(ctx, consAlertas, servicio.IndexarAlertas, "alertas", cfg, log); err != nil {
			errCh <- err
		}
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
	log.Info("indexer detenido limpiamente")
	return nil
}

// Indexador escribe un lote de mensajes. Lo implementan IndexarEventos e
// IndexarAlertas, de modo que el bucle de acumulacion y confirmacion de offsets
// se escribe una sola vez.
type Indexador func(context.Context, []*broker.Mensaje) error

// consumir acumula mensajes y los archiva por lotes.
//
// Un lote se cierra por volumen (BATCH_SIZE) o por tiempo (BATCH_TIMEOUT), lo
// que ocurra primero. El limite por tiempo es lo que evita que un evento se
// quede esperando indefinidamente cuando el trafico baja de madrugada.
//
// Los offsets se confirman DESPUES de que la base acepto el lote. Si la
// escritura falla, no se confirma nada y el lote se reintenta: es preferible
// reprocesar un evento ya archivado —el INSERT lleva ON CONFLICT DO NOTHING—
// que dejar un hueco permanente en el historico.
func consumir(ctx context.Context, c *broker.Consumer, indexar Indexador,
	que string, cfg *config.Config, log *slog.Logger) error {

	log = log.With(slog.String("flujo", que))
	log.Info("consumidor iniciado, acumulando",
		slog.Int("tamano_lote", cfg.TamanoLote),
		slog.Duration("espera_maxima", cfg.EsperaLote))

	lote := make([]*broker.Mensaje, 0, cfg.TamanoLote)
	temporizador := time.NewTimer(cfg.EsperaLote)
	defer temporizador.Stop()

	// leidos entrega los mensajes desde una goroutine aparte para que la
	// lectura bloqueante de Kafka no impida que venza el temporizador del lote.
	leidos := make(chan *broker.Mensaje)
	errLectura := make(chan error, 1)

	go func() {
		defer close(leidos)
		for {
			msg, err := c.Leer(ctx)
			if err != nil {
				if ctx.Err() == nil {
					errLectura <- err
				}
				return
			}
			select {
			case leidos <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	// vaciar archiva lo acumulado y confirma los offsets.
	vaciar := func() error {
		if len(lote) == 0 {
			return nil
		}
		if err := indexar(ctx, lote); err != nil {
			// No se confirma: el lote se reintenta cuando la base vuelva.
			return err
		}
		if err := c.ConfirmarLote(ctx, lote); err != nil && ctx.Err() == nil {
			log.Error("no se pudieron confirmar los offsets del lote",
				slog.Int("eventos", len(lote)),
				slog.String("error", err.Error()))
		}
		lote = lote[:0]
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			// Se archiva lo pendiente antes de salir, con un contexto propio
			// porque el original ya esta cancelado.
			if len(lote) > 0 {
				cierre, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := indexar(cierre, lote); err != nil {
					log.Error("no se pudo indexar el ultimo lote al cerrar",
						slog.Int("eventos", len(lote)),
						slog.String("error", err.Error()))
				} else {
					_ = c.ConfirmarLote(cierre, lote)
					log.Info("ultimo lote indexado antes de cerrar", slog.Int("eventos", len(lote)))
				}
				cancel()
			}
			log.Info("consumidor detenido")
			return nil

		case err := <-errLectura:
			log.Error("fallo al leer de kafka", slog.String("error", err.Error()))
			return err

		case msg, ok := <-leidos:
			if !ok {
				return nil
			}
			lote = append(lote, msg)
			if len(lote) >= cfg.TamanoLote {
				if err := vaciar(); err != nil {
					esperar(ctx, 2*time.Second)
				}
				reiniciar(temporizador, cfg.EsperaLote)
			}

		case <-temporizador.C:
			if err := vaciar(); err != nil {
				esperar(ctx, 2*time.Second)
			}
			reiniciar(temporizador, cfg.EsperaLote)
		}
	}
}

func reiniciar(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func esperar(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// healthHandler reporta el estado real de las dependencias.
func healthHandler(es *esearch.Client, publicador *broker.Publisher,
	cfg *config.Config) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		checks := map[string]any{}
		estado := "UP"

		if err := es.Ping(ctx); err != nil {
			checks["elasticsearch"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["elasticsearch"] = "available"
		}

		if err := publicador.Ping(ctx); err != nil {
			checks["kafka"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["kafka"] = "available"
		}

		checks["consumer_group"] = cfg.ConsumerGroup
		checks["tamano_lote"] = cfg.TamanoLote
		checks["tokenizacion_placas"] = cfg.PrivacyEnabled()

		codigo := http.StatusOK
		if estado != "UP" {
			codigo = http.StatusServiceUnavailable
		}

		httpapi.WriteJSON(w, codigo, map[string]any{
			"status":    estado,
			"service":   "indexer",
			"version":   canonical.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"checks":    checks,
		})
	}
}
