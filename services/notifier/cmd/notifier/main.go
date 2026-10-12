// Command notifier avisa a los organismos de respuesta de cada alerta
// correlacionada y deja constancia de a quien se aviso.
//
// Responde a una pregunta que el resto del sistema no respondia: no "que
// ocurrio", sino "a quien se le dijo". Sin este registro, una alerta y una
// alerta atendida son indistinguibles en el archivo.
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

	"ciudadinteligente/notifier/internal/broker"
	"ciudadinteligente/notifier/internal/canonical"
	"ciudadinteligente/notifier/internal/config"
	"ciudadinteligente/notifier/internal/dispatch"
	"ciudadinteligente/notifier/internal/httpapi"
	"ciudadinteligente/notifier/internal/obs"
	"ciudadinteligente/notifier/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fallo al arrancar el notifier: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := obs.NewLogger("notifier", cfg.LogLevel, cfg.LogFormat)
	metrics := obs.NewMetrics()

	validador, err := canonical.NewValidator()
	if err != nil {
		return fmt.Errorf("no se pudieron inicializar los contratos: %w", err)
	}

	matriz, err := dispatch.MatrizPorDefecto().AplicarEntorno(cfg.DespachoDesdeEntorno)
	if err != nil {
		return fmt.Errorf("matriz de despacho: %w", err)
	}

	log.Info("configuracion cargada",
		slog.String("env", cfg.Env),
		slog.String("puerto", cfg.Port),
		slog.Any("kafka_brokers", cfg.KafkaBrokers),
		slog.String("topic_entrada", cfg.TopicAlerts),
		slog.String("topic_dlq", cfg.TopicDLQ),
		slog.String("consumer_group", cfg.ConsumerGroup),
		slog.Bool("desde_el_principio", cfg.ConsumeFromStart),
		slog.String("canal", cfg.Canal),
		slog.String("matriz_despacho", matriz.Resumen()),
	)

	// Un tipo de alerta sin destinatarios no rompe nada: sencillamente no
	// avisa a nadie. Por eso se grita al arrancar, que es el unico momento en
	// que alguien esta mirando.
	if faltan := matriz.SinDestinatarios(); len(faltan) > 0 {
		log.Warn("hay tipos de alerta sin ningun organismo asignado: esas emergencias no avisaran a nadie",
			slog.Any("tipos", faltan))
	}

	var canal dispatch.Canal
	switch cfg.Canal {
	case config.CanalWebhook:
		canal = dispatch.NewCanalWebhook(cfg.WebhookURL, cfg.WebhookTimeout)
		log.Info("canal de entrega: webhook", slog.String("url", cfg.WebhookURL))
	default:
		canal = dispatch.NewCanalSimulado(log)
		log.Warn("canal de entrega SIMULADO: los avisos se registran pero no se entregan a ningun organismo real")
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

	consumidor := broker.NewConsumer(broker.OpcionesConsumer{
		Brokers:          cfg.KafkaBrokers,
		Topic:            cfg.TopicAlerts,
		Grupo:            cfg.ConsumerGroup,
		DesdeElPrincipio: cfg.ConsumeFromStart,
	})
	defer func() {
		if err := consumidor.Close(); err != nil {
			log.Error("fallo al cerrar el consumidor", slog.String("error", err.Error()))
		}
	}()

	servicio := dispatch.New(matriz, canal, almacen, validador, publicador, metrics, log)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, salud(r.Context(), almacen, publicador, cfg, canal))
	})
	// La matriz vigente se expone para que se pueda comprobar que criterio de
	// despacho esta aplicando esta instancia. Un cambio por variable de
	// entorno no deja rastro en el repositorio, y sin este endpoint habria que
	// leer el log de arranque para saberlo.
	mux.HandleFunc("GET /dispatch", func(w http.ResponseWriter, _ *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{
			"canal":  canal.Nombre(),
			"matriz": matriz,
		})
	})
	mux.HandleFunc("GET /schema/alert", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/schema+json")
		_, _ = w.Write(canonical.AlertSchemaJSON())
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("api de salud escuchando", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("el servidor HTTP fallo: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := consumir(ctx, consumidor, servicio, log); err != nil {
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
	log.Info("notifier detenido limpiamente")
	return nil
}

// consumir es el bucle principal: leer, despachar y confirmar.
//
// El offset se confirma despues de despachar, no antes. Con confirmacion
// automatica, un fallo dejaria el offset avanzado igual y la alerta no se
// despacharia nunca, sin dejar rastro de que alguien debio ser avisado.
func consumir(ctx context.Context, c *broker.Consumer, s *dispatch.Service,
	log *slog.Logger) error {

	log.Info("consumidor iniciado, esperando alertas")

	for {
		msg, err := c.Leer(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				log.Info("consumidor detenido")
				return nil
			}
			log.Error("fallo al leer de kafka", slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := s.Procesar(ctx, msg); err != nil {
			log.Error("fallo de infraestructura despachando la alerta, no se confirma el offset",
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
func salud(ctx context.Context, a *store.Store, p *broker.Publisher,
	cfg *config.Config, canal dispatch.Canal) map[string]any {

	checks := map[string]any{}
	estado := "UP"

	// Sin base no hay registro del aviso. Se podria entregar igualmente y no
	// anotarlo, pero entonces el servicio dejaria de cumplir su funcion, que
	// es precisamente dejar constancia.
	if err := a.Ping(ctx); err != nil {
		checks["postgres"] = "unavailable: " + err.Error()
		estado = "DOWN"
	} else {
		checks["postgres"] = "available"
	}

	if err := p.Ping(ctx); err != nil {
		checks["kafka"] = "unavailable: " + err.Error()
		estado = "DOWN"
	} else {
		checks["kafka"] = "available"
	}

	checks["consumer_group"] = cfg.ConsumerGroup
	checks["canal"] = canal.Nombre()
	checks["entrega_real"] = canal.Nombre() != "simulado"

	return map[string]any{
		"status":    estado,
		"service":   "notifier",
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"checks":    checks,
	}
}
