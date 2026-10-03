// Command correlator consume eventos canonicos, los correlaciona en ventanas
// temporales sobre Redis y emite alertas.
//
// Es el componente con toda la logica de dominio del sistema y el que sostiene
// el SLO de "alerta generada en menos de 2 segundos desde el evento".
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

	"ciudadinteligente/correlator/internal/broker"
	"ciudadinteligente/correlator/internal/canonical"
	"ciudadinteligente/correlator/internal/config"
	"ciudadinteligente/correlator/internal/correlate"
	"ciudadinteligente/correlator/internal/httpapi"
	"ciudadinteligente/correlator/internal/obs"
	"ciudadinteligente/correlator/internal/rules"
	"ciudadinteligente/correlator/internal/store"
	"ciudadinteligente/correlator/internal/window"
)

// intervaloBarrido es cada cuanto se revisan las coincidencias parciales que
// expiraron sin completarse, es decir, los falsos positivos.
const intervaloBarrido = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fallo al arrancar el correlator: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("correlator", cfg.LogLevel, cfg.LogFormat)
	metrics := obs.NewMetrics()

	validador, err := canonical.NewValidator()
	if err != nil {
		return fmt.Errorf("no se pudieron inicializar los contratos: %w", err)
	}

	reglas := rules.ConfigPorDefecto()
	reglas.Version = cfg.RuleVersion

	// Los umbrales del motor se pueden ajustar sin recompilar. Un valor sin
	// sentido impide arrancar en lugar de producir un motor que no detecta
	// nada o que alerta por todo, que son fallos silenciosos.
	reglas, err = reglas.AplicarEntorno(cfg.ReglasDesdeEntorno)
	if err != nil {
		return err
	}

	log.Info("configuracion cargada",
		slog.String("env", cfg.Env),
		slog.String("puerto", cfg.Port),
		slog.Any("kafka_brokers", cfg.KafkaBrokers),
		slog.String("topic_entrada", cfg.TopicEvents),
		slog.String("topic_salida", cfg.TopicAlerts),
		slog.String("topic_dlq", cfg.TopicDLQ),
		slog.String("consumer_group", cfg.ConsumerGroup),
		slog.String("reglas", reglas.Resumen()),
		slog.String("redis", cfg.RedisAddr),
		slog.Duration("ttl_idempotencia", cfg.SeenTTL),
		slog.Duration("ttl_ventana", cfg.WindowTTL),
		slog.Duration("ttl_supresion_alertas", cfg.AlertSuppressTTL),
		slog.String("rule_version", cfg.RuleVersion),
		slog.Bool("auth_habilitada", cfg.AuthEnabled()),
	)

	if !cfg.AuthEnabled() {
		log.Warn("API_TOKEN no configurado: la API de gestion queda abierta")
	}

	arranque, cancelArranque := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelArranque()

	ventanas, err := window.New(arranque, window.Opciones{
		Addr:         cfg.RedisAddr,
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		TTLVisto:     cfg.SeenTTL,
		TTLVentana:   cfg.WindowTTL,
		TTLSupresion: cfg.AlertSuppressTTL,
	})
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() { _ = ventanas.Close() }()
	log.Info("conectado a redis")

	// Acceso a PostgreSQL SOLO de lectura, para el endpoint /alerts/db. El
	// correlator no escribe en la nube: las alertas las archiva el persister.
	// Una consulta de un operador no esta en el camino critico de deteccion,
	// asi que aqui si es aceptable cruzar el enlace.
	almacen, err := store.New(arranque, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer almacen.Close()
	log.Info("conectado a postgres (solo lectura, para consultas)")

	publicador := broker.NewPublisher(cfg.KafkaBrokers, cfg.TopicAlerts, cfg.TopicDLQ)
	defer func() {
		if err := publicador.Close(); err != nil {
			log.Error("fallo al cerrar el publicador", slog.String("error", err.Error()))
		}
	}()

	consumidor := broker.NewConsumer(broker.OpcionesConsumer{
		Brokers:          cfg.KafkaBrokers,
		Topic:            cfg.TopicEvents,
		Grupo:            cfg.ConsumerGroup,
		DesdeElPrincipio: cfg.ConsumeFromStart,
	})
	defer func() {
		if err := consumidor.Close(); err != nil {
			log.Error("fallo al cerrar el consumidor", slog.String("error", err.Error()))
		}
	}()

	servicio := correlate.New(ventanas, publicador, validador, reglas, metrics, log)

	api := httpapi.NewAPI(ventanas, almacen, log, salud(ventanas, almacen, publicador, cfg))

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", api.Health)
	mux.HandleFunc("GET /schema/event", api.SchemaEvento)
	mux.HandleFunc("GET /schema/alert", api.SchemaAlerta)
	mux.Handle("GET /alerts/active",
		httpapi.ConAutenticacion(cfg.APIToken, log, http.HandlerFunc(api.AlertasActivas)))
	mux.Handle("GET /alerts/db",
		httpapi.ConAutenticacion(cfg.APIToken, log, http.HandlerFunc(api.AlertasPersistidas)))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.ConRecuperacion(log, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// API de gestion.
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("api de gestion escuchando", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("el servidor HTTP fallo: %w", err)
		}
	}()

	// Bucle de consumo.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := consumir(ctx, consumidor, servicio, metrics, log); err != nil {
			errCh <- err
		}
	}()

	// Barrido periodico de falsos positivos.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(intervaloBarrido)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				servicio.BarrerFalsosPositivos(ctx)
				servicio.RefrescarVentanas(ctx)
			}
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
	log.Info("correlator detenido limpiamente")
	return nil
}

// consumir es el bucle principal: leer, procesar y confirmar.
//
// El offset se confirma despues de procesar, no antes. Con confirmacion
// automatica, un fallo de procesamiento avanzaria el offset igual y el evento
// se perderia sin dejar rastro.
func consumir(ctx context.Context, c *broker.Consumer, s *correlate.Service,
	m *obs.Metrics, log *slog.Logger) error {

	log.Info("consumidor iniciado, esperando eventos")

	for {
		msg, err := c.Leer(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				log.Info("consumidor detenido")
				return nil
			}
			// Un fallo de lectura suele ser un rebalanceo o una caida
			// transitoria del broker: se registra y se reintenta.
			log.Error("fallo al leer de kafka", slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := s.Procesar(ctx, msg); err != nil {
			// Error de infraestructura: NO se confirma el offset, para que el
			// evento se reintente cuando la dependencia vuelva.
			m.EventsSkipped.WithLabelValues("infraestructura").Inc()
			log.Error("fallo de infraestructura procesando el evento, no se confirma el offset",
				slog.Int("particion", msg.Particion),
				slog.Int64("offset", msg.Offset),
				slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}

		if err := c.Confirmar(ctx, msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Error("no se pudo confirmar el offset",
				slog.Int("particion", msg.Particion),
				slog.Int64("offset", msg.Offset),
				slog.String("error", err.Error()))
		}
	}
}

// salud comprueba de verdad cada dependencia.
func salud(v *window.Store, a *store.Store, p *broker.Publisher,
	cfg *config.Config) func(context.Context) map[string]any {

	return func(ctx context.Context) map[string]any {
		checks := map[string]any{}
		estado := "UP"

		// Sin Redis no hay ventanas de correlacion: el servicio no puede
		// cumplir su funcion.
		if err := v.Ping(ctx); err != nil {
			checks["redis"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["redis"] = "available"
		}

		// Postgres solo se usa para consultas: si no responde, la deteccion
		// sigue funcionando y el servicio queda degradado, no caido.
		if err := a.Ping(ctx); err != nil {
			checks["postgres_consultas"] = "unavailable: " + err.Error()
			if estado == "UP" {
				estado = "DEGRADED"
			}
		} else {
			checks["postgres_consultas"] = "available"
		}

		if err := p.Ping(ctx); err != nil {
			checks["kafka"] = "unavailable: " + err.Error()
			estado = "DOWN"
		} else {
			checks["kafka"] = "available"
		}

		checks["consumer_group"] = cfg.ConsumerGroup
		checks["rule_version"] = cfg.RuleVersion
		checks["auth"] = cfg.AuthEnabled()

		return map[string]any{
			"status":    estado,
			"service":   "correlator",
			"version":   canonical.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"checks":    checks,
		}
	}
}
