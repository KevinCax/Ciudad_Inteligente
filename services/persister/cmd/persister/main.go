// Command persister archiva en PostgreSQL los eventos canonicos que circulan
// por Kafka.
//
// Existe para sacar la escritura del camino critico. Cuando el INSERT ocurria
// dentro de la peticion HTTP del ingestor, cada evento pagaba una ida y vuelta
// contra una base que esta en la nube, y una caida del enlace detenia la
// ingesta por completo. Con este servicio aparte, el ingestor solo valida y
// publica en Kafka —que vive en la LAN— y el archivado avanza a su ritmo.
//
// El persister se situa justo sobre la frontera LAN -> nube, de modo que es el
// componente natural para tokenizar la placa vehicular antes de que el dato
// salga del control municipal.
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

	"ciudadinteligente/persister/internal/broker"
	"ciudadinteligente/persister/internal/canonical"
	"ciudadinteligente/persister/internal/config"
	"ciudadinteligente/persister/internal/httpapi"
	"ciudadinteligente/persister/internal/obs"
	"ciudadinteligente/persister/internal/persist"
	"ciudadinteligente/persister/internal/privacy"
	"ciudadinteligente/persister/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fallo al arrancar el persister: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("persister", cfg.LogLevel, cfg.LogFormat)
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

	almacen, err := store.New(arranque, cfg.DatabaseURL, cfg.MaxConns)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer almacen.Close()
	log.Info("conectado a postgres")

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
	servicio := persist.New(almacen, publicador, validador, tokenizador, metrics, log)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", healthHandler(almacen, publicador, cfg))

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
		if err := consumir(ctx, consEventos, servicio.ArchivarLote, "eventos", cfg, log); err != nil {
			errCh <- err
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := consumir(ctx, consAlertas, servicio.ArchivarLoteAlertas, "alertas", cfg, log); err != nil {
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
	log.Info("persister detenido limpiamente")
	return nil
}

// Archivador escribe un lote de mensajes. Lo implementan ArchivarLote para
// eventos y ArchivarLoteAlertas para alertas, de modo que el bucle de
// acumulacion y confirmacion de offsets se escribe una sola vez.
type Archivador func(context.Context, []*broker.Mensaje) error

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
func consumir(ctx context.Context, c *broker.Consumer, archivar Archivador,
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
		if err := archivar(ctx, lote); err != nil {
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
				if err := archivar(cierre, lote); err != nil {
					log.Error("no se pudo archivar el ultimo lote al cerrar",
						slog.Int("eventos", len(lote)),
						slog.String("error", err.Error()))
				} else {
					_ = c.ConfirmarLote(cierre, lote)
					log.Info("ultimo lote archivado antes de cerrar", slog.Int("eventos", len(lote)))
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
func healthHandler(almacen *store.Store, publicador *broker.Publisher,
	cfg *config.Config) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		checks := map[string]any{}
		estado := "UP"

		if err := almacen.Ping(ctx); err != nil {
			checks["postgres"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["postgres"] = "available"
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
			"service":   "persister",
			"version":   canonical.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"checks":    checks,
		})
	}
}
