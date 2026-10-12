// Package correlate orquesta el ciclo del correlator: consumir, correlacionar,
// persistir y publicar.
package correlate

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"ciudadinteligente/correlator/internal/broker"
	"ciudadinteligente/correlator/internal/canonical"
	"ciudadinteligente/correlator/internal/obs"
	"ciudadinteligente/correlator/internal/privacy"
	"ciudadinteligente/correlator/internal/rules"
	"ciudadinteligente/correlator/internal/window"
)

// Service es el motor del correlator.
//
// No escribe en la base de datos. Las alertas se publican en Kafka y es el
// servicio persister el que las archiva, de modo que el recorrido completo
// evento -> alerta ocurre integramente dentro de la LAN municipal: una caida
// del enlace a internet retrasa el archivo historico, pero no interrumpe la
// deteccion.
type Service struct {
	ventanas    *window.Store
	publicador  *broker.Publisher
	validador   *canonical.Validator
	tokenizador *privacy.Tokenizador
	reglas      rules.Config
	metrics     *obs.Metrics
	log         *slog.Logger
}

// New construye el motor.
//
// El correlator SI tokeniza placas, pero por un motivo distinto al del
// persister y el indexer. Ellos lo hacen para que el dato personal no cruce a
// la nube. Aqui no se escribe nada fuera de la LAN: el seudonimo se usa como
// clave de la ventana de vehiculo, y tiene que ser el mismo que archivan
// aquellos para que una alerta de persecucion se pueda cruzar despues con el
// historico del coche. Por eso la llave HMAC debe ser identica en los tres.
func New(v *window.Store, p *broker.Publisher, val *canonical.Validator,
	tok *privacy.Tokenizador, cfg rules.Config, m *obs.Metrics, log *slog.Logger) *Service {
	return &Service{
		ventanas: v, publicador: p, validador: val, tokenizador: tok,
		reglas: cfg, metrics: m, log: log,
	}
}

// Procesar atiende un mensaje del topic de eventos.
//
// Devuelve error solo cuando el fallo es de infraestructura y conviene
// reintentar; los eventos que no cumplen el contrato se derivan a la DLQ y se
// dan por atendidos, para no bloquear la particion indefinidamente.
func (s *Service) Procesar(ctx context.Context, m *broker.Mensaje) error {
	ctx = obs.WithTrace(ctx, m.TraceID)
	log := obs.LoggerFrom(ctx, s.log)

	// Defensa en profundidad: la guia pide validar en productor y de nuevo en
	// consumidor. El correlator no confia en que el ingestor haya validado.
	if err := s.validador.ValidateEventBytes(m.Valor); err != nil {
		s.metrics.EventsSkipped.WithLabelValues("contrato").Inc()
		s.aDLQ(ctx, "contrato", detalleDeValidacion(err), m)
		return nil
	}

	ev, err := canonical.ParseEvent(m.Valor)
	if err != nil {
		s.metrics.EventsSkipped.WithLabelValues("mapeo").Inc()
		s.aDLQ(ctx, "mapeo", err.Error(), m)
		return nil
	}

	momento, err := ev.TimestampUTC()
	if err != nil {
		s.metrics.EventsSkipped.WithLabelValues("timestamp").Inc()
		s.aDLQ(ctx, "timestamp", err.Error(), m)
		return nil
	}

	// Idempotencia atomica: SET NX en una sola operacion. Comprobar y despues
	// escribir dejaria una ventana de carrera por la que dos instancias
	// podrian procesar el mismo evento.
	nuevo, err := s.ventanas.MarcarVisto(ctx, ev.EventID)
	if err != nil {
		return err // Redis caido: conviene reintentar
	}
	if !nuevo {
		s.metrics.EventsSkipped.WithLabelValues("duplicado").Inc()
		log.Debug("evento ya procesado", slog.String("event_id", ev.EventID))
		return nil
	}

	zona := ev.Zone()

	if err := s.ventanas.AgregarEvento(ctx, &window.Resumen{
		EventID:   ev.EventID,
		EventType: ev.EventType,
		Timestamp: ev.Timestamp,
		Zone:      zona,
		Payload:   ev.Payload,
		Lat:       ev.Geo.Lat,
		Lon:       ev.Geo.Lon,
	}, momento); err != nil {
		return err
	}

	s.metrics.EventsProcessed.WithLabelValues(ev.EventType, zona).Inc()

	// La ventana se consulta hasta el maximo que necesita cualquier regla;
	// cada regla acota despues su propio rango.
	desde := momento.Add(-s.ventanaMaxima())
	resumenes, err := s.ventanas.EventosEnVentana(ctx, zona, desde, momento)
	if err != nil {
		return err
	}
	// El valor se actualiza aqui, pero quien saca del gauge las zonas que ya
	// no tienen ventana es RefrescarVentanas: aqui nunca llega a cero, porque
	// el evento que se acaba de insertar ya cuenta.
	s.metrics.WindowSize.WithLabelValues(zona).Set(float64(len(resumenes)))

	resultado := rules.Evaluar(rules.Ventana{
		Zona:    zona,
		Ahora:   momento,
		Eventos: rules.DesdeResumenes(resumenes),
	}, s.reglas)

	for _, det := range resultado.Detecciones {
		if err := s.emitir(ctx, det, ev, momento, det.Zona); err != nil {
			log.Error("no se pudo emitir la alerta",
				slog.String("tipo", det.Tipo),
				slog.String("zona", zona),
				slog.String("error", err.Error()))
		}
	}

	for _, p := range resultado.Parciales {
		if err := s.ventanas.RegistrarParcial(ctx, window.Pendiente{
			Regla:      p.Regla,
			Zona:       zona,
			Disparador: p.Disparador,
			Faltante:   p.Faltante,
			Expira:     p.Expira,
		}); err != nil {
			log.Warn("no se pudo registrar la coincidencia parcial",
				slog.String("regla", p.Regla),
				slog.String("error", err.Error()))
		}
	}

	// Segundo camino de correlacion, independiente del anterior: el que
	// agrupa por vehiculo en lugar de por zona.
	//
	// Un boton de panico no trae placa, pero es el ancla de la regla de
	// persecucion, asi que al llegar hay que revisar si algun vehiculo de la
	// zona ya tenia una cadena a medias esperandolo. Sin esta rama hay una
	// carrera real: Kafka reparte por zona, de modo que el evento de la zona 9
	// y el panico de la zona 1 viajan en particiones distintas y su orden
	// relativo NO esta garantizado. Si la particion del panico va retrasada
	// —cosa normal tras un reinicio o un rebalanceo— la regla se evalua antes
	// de que exista su ancla y la alerta no se emite nunca.
	//
	// Se vio ocurrir: una cadena completa quedo solo como coincidencia parcial
	// en corr:pending:persecucion_vehicular:zona_9, por diecisiete segundos de
	// desfase entre particiones.
	if ev.EventType == canonical.TypePanicButton {
		return s.reevaluarVehiculosDeLaZona(ctx, ev, momento, resumenes)
	}

	return s.correlacionarVehiculo(ctx, ev, momento)
}

// reevaluarVehiculosDeLaZona vuelve a evaluar la regla de persecucion para
// cada vehiculo que aparece en la ventana de la zona donde se pulso el boton.
//
// El coste esta acotado por los vehiculos distintos de esa ventana, que son
// pocos, y no exige ningun indice nuevo: la ventana de zona ya se leyo para
// las reglas anteriores y se reutiliza tal cual.
func (s *Service) reevaluarVehiculosDeLaZona(ctx context.Context, ev *canonical.Event,
	momento time.Time, resumenes []window.Resumen) error {

	vistos := map[string]bool{}

	for _, r := range resumenes {
		placa, _ := r.Payload["placa_vehicular"].(string)
		if placa == "" {
			continue
		}
		token := s.tokenizador.Tokenizar(placa)
		if vistos[token] {
			continue
		}
		vistos[token] = true

		if err := s.evaluarVehiculo(ctx, token, ev, momento); err != nil {
			return err
		}
	}

	return nil
}

// correlacionarVehiculo sigue las apariciones de un mismo coche.
//
// Existe porque la ventana de zona no puede ver una huida: un vehiculo que
// escapa cambia de zona precisamente para dejar de aparecer en ella. Cada
// aparicion se anota ademas en una ventana indexada por el seudonimo de la
// placa, que cruza zonas.
func (s *Service) correlacionarVehiculo(ctx context.Context, ev *canonical.Event,
	momento time.Time) error {

	placa, _ := ev.Payload["placa_vehicular"].(string)
	if placa == "" {
		return nil
	}

	// El seudonimo, nunca la placa en claro: es lo que se usa como clave de
	// Redis y lo que coincide con el dato archivado en PostgreSQL.
	token := s.tokenizador.Tokenizar(placa)

	if err := s.ventanas.AgregarEventoVehiculo(ctx, token, &window.Resumen{
		EventID:   ev.EventID,
		EventType: ev.EventType,
		Timestamp: ev.Timestamp,
		Zone:      ev.Zone(),
		Payload:   ev.Payload,
		Lat:       ev.Geo.Lat,
		Lon:       ev.Geo.Lon,
	}, momento); err != nil {
		return err
	}

	return s.evaluarVehiculo(ctx, token, ev, momento)
}

// evaluarVehiculo corre la regla de persecucion sobre la ventana de un
// vehiculo. No modifica la ventana: solo la lee.
//
// Esta separado de correlacionarVehiculo porque hay dos caminos que llevan
// aqui. Uno es la llegada de una lectura de placa, que primero anota la
// aparicion. El otro es la llegada de un boton de panico, que no anota nada
// —un pulsador no tiene matricula— pero puede completar la cadena de un
// vehiculo que ya estaba a medias.
func (s *Service) evaluarVehiculo(ctx context.Context, token string,
	ev *canonical.Event, momento time.Time) error {

	log := obs.LoggerFrom(ctx, s.log)

	// La ventana se lee en AMBOS sentidos alrededor del evento que la
	// dispara, no solo hacia atras.
	//
	// Acotar el extremo superior al instante del evento parece lo natural
	// —no se puede ver el futuro— y es un error cuando los eventos llegan
	// desordenados, que es lo normal: Kafka reparte por zona, de modo que una
	// lectura de la zona 1 y otra de la zona 9 viajan en particiones distintas
	// y su orden de proceso no respeta el de los relojes. Si el evento mas
	// ANTIGUO de la cadena se procesa el ULTIMO, con el extremo superior
	// cerrado su consulta no ve ninguna de las lecturas posteriores que ya
	// habian llegado: cuenta una sola infraccion y se va. La cadena se pierde
	// sin dejar rastro, que es la forma mas cara de fallar.
	//
	// Se vio ocurrir con un escenario de cuatro eventos.
	margen := s.reglas.VentanaPersecucion
	resumenes, err := s.ventanas.EventosDeVehiculo(ctx, token,
		momento.Add(-margen), momento.Add(margen))
	if err != nil {
		return err
	}
	eventos := rules.DesdeResumenes(resumenes)

	// El punto de referencia es la aparicion MAS RECIENTE que se conoce del
	// vehiculo, no el evento que disparo la evaluacion. Asi la ventana mide lo
	// mismo llegue el ultimo evento primero o el primero el ultimo.
	ahora := momento
	for _, e := range eventos {
		if e.Momento.After(ahora) {
			ahora = e.Momento
		}
	}
	desde := ahora.Add(-s.reglas.VentanaPersecucion)

	// Filtro barato ANTES de las consultas por zona. Buscar los botones de
	// panico exige una lectura de Redis por cada zona que el vehiculo visito;
	// hacerlo para cada lectura de placa de la ciudad multiplicaria el trafico
	// contra Redis sin que la inmensa mayoria llegue a cumplir la regla.
	infracciones := rules.CadenaPlausible(
		rules.Infracciones(eventos, desde, s.reglas), s.reglas)
	if len(infracciones) < s.reglas.MinInfraccionesPersecucion {
		return nil
	}

	var panicos []rules.Evento
	for _, z := range rules.ZonasVisitadas(infracciones, desde) {
		rs, err := s.ventanas.EventosEnVentana(ctx, z, desde, ahora)
		if err != nil {
			return err
		}
		panicos = append(panicos, rules.PanicosDe(rules.DesdeResumenes(rs))...)
	}

	resultado := rules.EvaluarVehiculo(rules.VentanaVehiculo{
		Token:   token,
		Ahora:   ahora,
		Eventos: eventos,
		Panicos: panicos,
	}, s.reglas)

	for _, det := range resultado.Detecciones {
		// La supresion va por vehiculo, no por zona: dos coches huyendo por
		// la misma zona son dos incidentes distintos.
		if err := s.emitir(ctx, det, ev, momento, token); err != nil {
			log.Error("no se pudo emitir la alerta de persecucion",
				slog.String("zona", det.Zona),
				slog.String("error", err.Error()))
		}
	}

	for _, p := range resultado.Parciales {
		if err := s.ventanas.RegistrarParcial(ctx, window.Pendiente{
			Regla:      p.Regla,
			Zona:       ev.Zone(),
			Disparador: p.Disparador,
			Faltante:   p.Faltante,
			Expira:     p.Expira,
		}); err != nil {
			log.Warn("no se pudo registrar la coincidencia parcial del vehiculo",
				slog.String("regla", p.Regla),
				slog.String("error", err.Error()))
		}
	}

	return nil
}

// emitir persiste, publica y registra una alerta detectada.
//
// claveSupresion es el ambito dentro del cual una alerta de este tipo se
// considera repetida. Para las reglas de zona es la zona; para la persecucion
// es el vehiculo, porque dos coches distintos huyendo por la misma zona son
// dos incidentes y suprimir el segundo seria perderlo.
func (s *Service) emitir(ctx context.Context, det rules.Deteccion, ev *canonical.Event,
	momento time.Time, claveSupresion string) error {

	log := obs.LoggerFrom(ctx, s.log)

	// Supresion: si ya hay una alerta equivalente vigente, no se emite otra.
	// Sin esto, cada evento nuevo de la ventana vuelve a disparar la misma
	// regla sobre la misma evidencia, y un solo incidente en curso llena la
	// tabla de alertas casi identicas.
	zona := det.Zona

	reservada, err := s.ventanas.ReservarAlerta(ctx, det.Tipo, claveSupresion)
	if err != nil {
		return err
	}
	if !reservada {
		s.metrics.AlertsSuppressed.WithLabelValues(det.Tipo).Inc()
		return nil
	}

	alerta := canonical.NewAlert(
		det.Tipo,
		zona,
		det.Score,
		canonical.Window{
			Start: det.Inicio.UTC().Format(time.RFC3339Nano),
			End:   det.Fin.UTC().Format(time.RFC3339Nano),
		},
		det.Evidencia,
		ev.CorrelationID,
		s.reglas.Version,
	)

	// Validar la salida antes de publicarla. Sin un contrato formal para la
	// alerta, cualquier cambio de forma llegaria sin control a los
	// consumidores aguas abajo y se descubriria alli, no aqui.
	if err := s.validador.ValidateAlert(alerta); err != nil {
		s.metrics.EventsSkipped.WithLabelValues("alerta_invalida").Inc()
		log.Error("la alerta generada no cumple su contrato",
			slog.String("tipo", det.Tipo),
			slog.String("error", err.Error()))
		return err
	}

	cuerpo, err := json.Marshal(alerta)
	if err != nil {
		return err
	}

	// Publicar es la unica escritura del camino critico, y va contra un broker
	// que esta en la LAN. El archivado en la base lo hace despues el persister,
	// consumiendo este mismo topic.
	if err := s.publicador.PublicarAlerta(ctx, zona, cuerpo, ev.TraceID); err != nil {
		return err
	}

	if err := s.ventanas.GuardarAlertaActiva(ctx, zona, cuerpo, momento); err != nil {
		log.Warn("la alerta no quedo disponible en /alerts/active",
			slog.String("error", err.Error()))
	}

	// La alerta se genero: las parciales de esta regla y zona dejan de ser
	// candidatas a falso positivo.
	if err := s.ventanas.ResolverParciales(ctx, det.Tipo, zona); err != nil {
		log.Warn("no se pudieron resolver las parciales",
			slog.String("error", err.Error()))
	}

	// Metrica del SLO: tiempo entre el momento del evento disparador y la
	// emision de la alerta. Es lo que permite afirmar y verificar "alerta
	// generada en menos de 2 segundos".
	latencia := time.Since(momento).Seconds()
	s.metrics.CorrelationLatency.WithLabelValues(det.Tipo).Observe(latencia)
	s.metrics.AlertsGenerated.WithLabelValues(det.Tipo, zona).Inc()

	log.Info("alerta generada",
		slog.String("alert_id", alerta.AlertID),
		slog.String("tipo", alerta.Type),
		slog.String("zona", zona),
		slog.Float64("score", alerta.Score),
		slog.Int("evidencia", len(alerta.Evidence)),
		slog.Float64("latencia_s", latencia),
		slog.String("rule_version", alerta.RuleVersion))

	return nil
}

// BarrerFalsosPositivos cuenta las coincidencias parciales que expiraron sin
// completarse. Lo invoca un temporizador desde main.
func (s *Service) BarrerFalsosPositivos(ctx context.Context) {
	expiradas, err := s.ventanas.BarrerExpiradas(ctx, time.Now().UTC())
	if err != nil {
		s.log.Warn("no se pudo barrer las coincidencias parciales",
			slog.String("error", err.Error()))
		return
	}

	for _, p := range expiradas {
		s.metrics.FalsePositives.WithLabelValues(p.Regla, p.Zona).Inc()
		s.log.Info("falso positivo confirmado",
			slog.String("regla", p.Regla),
			slog.String("zona", p.Zona),
			slog.String("disparador", p.Disparador),
			slog.String("faltaba", p.Faltante))
	}
}

// RefrescarVentanas pone el gauge de tamano de ventana al dia con lo que de
// verdad hay en Redis. Lo invoca el mismo temporizador que barre los falsos
// positivos.
//
// Hace falta porque un GaugeVec conserva para siempre toda combinacion de
// etiquetas que haya visto, y el camino normal —fijar el valor al procesar un
// evento— solo escribe zonas que reciben trafico: una zona que deja de
// recibirlo se queda publicando su ultimo valor indefinidamente. El panel
// anuncia "eventos vigentes en la ventana" y estaria mostrando una cifra de
// hace horas, ademas de acumular zonas que ya no existen.
//
// Reset() seguido de repoblar en la misma pasada evita el parpadeo: entre
// ambas operaciones no hay recogida de metricas.
func (s *Service) RefrescarVentanas(ctx context.Context) {
	zonas, err := s.ventanas.ZonasActivas(ctx)
	if err != nil {
		s.log.Warn("no se pudieron listar las zonas con ventana",
			slog.String("error", err.Error()))
		return
	}

	tamanos := make(map[string]int64, len(zonas))
	for _, z := range zonas {
		n, err := s.ventanas.TamanoVentana(ctx, z)
		if err != nil {
			// Una zona que falla no debe impedir refrescar las demas.
			continue
		}
		if n > 0 {
			tamanos[z] = n
		}
	}

	s.metrics.WindowSize.Reset()
	for z, n := range tamanos {
		s.metrics.WindowSize.WithLabelValues(z).Set(float64(n))
	}
}

// ventanaMaxima es el rango que hay que traer de Redis para que ninguna regla
// se quede corta.
func (s *Service) ventanaMaxima() time.Duration {
	maximo := s.reglas.VentanaRobo
	for _, d := range []time.Duration{
		s.reglas.VentanaAccidente,
		s.reglas.VentanaIncendio,
		s.reglas.VentanaVelocidad,
	} {
		if d > maximo {
			maximo = d
		}
	}
	return maximo
}

// contextoDeLimpieza devuelve un contexto que sobrevive a la cancelacion del
// original.
//
// Las tareas de limpieza —liberar una reserva, escribir en la DLQ— hay que
// ejecutarlas precisamente cuando el contexto de la peticion ya murio: si el
// cliente aborta a mitad de vuelo, heredar su cancelacion hace que la limpieza
// falle tambien y deje el sistema en un estado peor que el error original.
//
// Se observo en la prueba de carga: al cancelarse una peticion en curso, la
// publicacion fallaba, la liberacion de la reserva fallaba por el mismo motivo
// y el event_id quedaba retenido en Redis durante 24 horas. El reintento del
// cliente recibia entonces un 409 por un evento que nunca llego a publicarse.
//
// WithoutCancel conserva los valores del contexto (el trace_id, entre ellos)
// pero descarta la cancelacion. El plazo acotado evita que la limpieza se
// quede colgada indefinidamente.
func contextoDeLimpieza(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (s *Service) aDLQ(ctx context.Context, motivo string, detalle any, m *broker.Mensaje) {
	limpieza, cancel := contextoDeLimpieza(ctx)
	defer cancel()

	if err := s.publicador.PublicarDLQ(limpieza, motivo, detalle, m); err != nil {
		obs.LoggerFrom(ctx, s.log).Error("no se pudo escribir en la DLQ",
			slog.String("motivo", motivo),
			slog.String("error", err.Error()))
		return
	}
	s.metrics.DLQPublished.WithLabelValues(motivo).Inc()
}

func detalleDeValidacion(err error) any {
	var verr *canonical.ValidationError
	if errors.As(err, &verr) {
		return verr.Fields
	}
	return err.Error()
}
