// Command ingestor es el punto de entrada REST del sistema.
//
// Recibe eventos canonicos v1.0 por HTTP, los enriquece, los valida contra el
// contrato, los persiste y los publica en Kafka. Es el primer eslabon del
// pipeline y el unico componente expuesto a los productores.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ciudadinteligente/ingestor/internal/broker"
	"ciudadinteligente/ingestor/internal/canonical"
	"ciudadinteligente/ingestor/internal/config"
	"ciudadinteligente/ingestor/internal/dedupe"
	"ciudadinteligente/ingestor/internal/httpapi"
	"ciudadinteligente/ingestor/internal/ingest"
	"ciudadinteligente/ingestor/internal/obs"
)

// maxCuerpoEvento acota el cuerpo de /events a 1 MiB, alineado con el
// message.max.bytes por defecto de Kafka.
const maxCuerpoEvento = 1 << 20

// maxCuerpoBulk acota el cuerpo de /events/bulk a 8 MiB: un lote razonable
// sin permitir que una sola peticion agote la memoria del proceso.
const maxCuerpoBulk = 8 << 20

func main() {
	if err := run(); err != nil {
		// El logger puede no existir todavia si fallo la configuracion.
		fmt.Fprintf(os.Stderr, "fallo al arrancar el ingestor: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("ingestor", cfg.LogLevel, cfg.LogFormat)
	metrics := obs.NewMetrics()

	validator, err := canonical.NewValidator()
	if err != nil {
		return fmt.Errorf("no se pudo inicializar el validador: %w", err)
	}
	enricher := canonical.NewEnricher(cfg.PartitionKeyStrategy)

	log.Info("configuracion cargada",
		slog.String("env", cfg.Env),
		slog.String("puerto", cfg.Port),
		slog.Any("kafka_brokers", cfg.KafkaBrokers),
		slog.String("topic_eventos", cfg.TopicEvents),
		slog.String("topic_dlq", cfg.TopicDLQ),
		slog.String("particionado", cfg.PartitionKeyStrategy),
		slog.String("redis", cfg.RedisAddr),
		slog.Duration("ttl_deduplicacion", cfg.DedupeTTL),
		slog.Bool("auth_habilitada", cfg.AuthEnabled()),
	)

	if !cfg.AuthEnabled() {
		log.Warn("API_KEY no configurada: los endpoints de ingesta quedan abiertos")
	}

	arranque, cancelArranque := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelArranque()

	deduplicador, err := dedupe.New(arranque, dedupe.Opciones{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
		TTL:      cfg.DedupeTTL,
	})
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() { _ = deduplicador.Close() }()
	log.Info("conectado a redis")

	publicador := broker.New(cfg.KafkaBrokers, cfg.TopicEvents, cfg.TopicDLQ)
	defer func() {
		if err := publicador.Close(); err != nil {
			log.Error("fallo al cerrar el publicador", slog.String("error", err.Error()))
		}
	}()

	// Kafka no bloquea el arranque: si esta caido al iniciar, el servicio
	// arranca en estado DEGRADED y el health lo reporta. Asi el ingestor
	// puede recuperarse solo cuando el broker vuelva, en lugar de entrar en
	// un ciclo de reinicios del contenedor.
	if err := publicador.Ping(arranque); err != nil {
		log.Warn("kafka no responde al arrancar, el servicio queda degradado",
			slog.String("error", err.Error()))
	} else {
		log.Info("conectado a kafka")
	}

	servicio := ingest.New(deduplicador, publicador, validator, enricher, metrics, log)
	api := httpapi.NewAPI(servicio, log, salud(deduplicador, publicador, cfg))

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /events/health", api.Health)
	mux.HandleFunc("GET /events/schema", api.Schema)

	mux.Handle("POST /events", httpapi.Encadenar(
		http.HandlerFunc(api.IngestOne),
		httpapi.ConMetricas(metrics, "/events"),
		httpapi.ConAutenticacion(cfg.APIKey, log),
		httpapi.ConLimiteDeCuerpo(maxCuerpoEvento),
	))

	mux.Handle("POST /events/bulk", httpapi.Encadenar(
		http.HandlerFunc(api.IngestBulk),
		httpapi.ConMetricas(metrics, "/events/bulk"),
		httpapi.ConAutenticacion(cfg.APIKey, log),
		httpapi.ConLimiteDeCuerpo(maxCuerpoBulk),
	))

	// Traza y recuperacion envuelven todo, incluidos health y metrics.
	manejador := httpapi.Encadenar(mux,
		httpapi.ConRecuperacion(log),
		httpapi.ConTrace(),
	)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           manejador,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Arranque y apagado ordenado: ante SIGTERM se deja de aceptar conexiones
	// nuevas y se espera a que terminen las en curso, para no cortar eventos a
	// medio persistir cuando el orquestador recicla el contenedor.
	errCh := make(chan error, 1)
	go func() {
		log.Info("ingestor escuchando", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("el servidor HTTP fallo: %w", err)
	case <-ctx.Done():
		log.Info("senal de apagado recibida, cerrando")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("apagado forzado: %w", err)
	}

	log.Info("ingestor detenido limpiamente")
	return nil
}

// salud comprueba de verdad cada dependencia.
//
// Comprobar que el cliente existe no es comprobar que la dependencia
// responde: un health asi devuelve "UP" con Kafka caido, que es peor que no
// tenerlo, porque el orquestador nunca saca la instancia de rotacion.
//
// Ninguna de las dependencias que se comprueban aqui esta en la nube: el
// ingestor depende solo de Redis y Kafka, ambos en la LAN municipal.
func salud(deduplicador *dedupe.Store, publicador *broker.Publisher, cfg *config.Config) func(context.Context) map[string]any {
	return func(ctx context.Context) map[string]any {
		checks := map[string]any{}
		estado := "UP"

		if err := deduplicador.Ping(ctx); err != nil {
			checks["redis"] = "unavailable: " + err.Error()
			estado = "DOWN" // sin deduplicacion no se puede aceptar nada
		} else {
			checks["redis"] = "available"
		}

		if err := publicador.Ping(ctx); err != nil {
			checks["kafka"] = "unavailable: " + err.Error()
			if estado == "UP" {
				estado = "DEGRADED"
			}
		} else {
			checks["kafka"] = "available"
		}

		checks["particionado"] = cfg.PartitionKeyStrategy
		checks["auth"] = cfg.AuthEnabled()

		return map[string]any{
			"status":    estado,
			"service":   "ingestor",
			"version":   canonical.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"checks":    checks,
		}
	}
}
