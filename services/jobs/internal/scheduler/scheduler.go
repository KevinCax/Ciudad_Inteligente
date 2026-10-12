// Package scheduler ejecuta tareas a una hora fija del dia.
//
// Es lo que sustituye a Airflow (ver ADR-009). Deliberadamente no intenta
// parecerse a el: no hay grafos de dependencias ni interfaz web. Hace una sola
// cosa —correr dos tareas nocturnas, una vez cada una, aunque haya varias
// instancias del servicio— y la hace de forma verificable.
//
// Del backfill conserva solo lo minimo util: una ejecucion manual puede pedir
// una fecha concreta, que cubre el caso real —recalcular el resumen del dia que
// fallo— sin la maquinaria de rangos y reintentos de un orquestador.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Resultado resume que hizo una ejecucion.
type Resultado struct {
	// Afectadas son las filas o documentos que la tarea modifico o elimino.
	Afectadas int64
	// Detalle es informacion adicional para el registro.
	Detalle map[string]any
}

// Parametros son los argumentos opcionales de una ejecucion manual.
//
// El planificador no los interpreta: los pasa tal cual a la tarea. Es lo que
// permite un backfill minimo —recalcular el resumen de un dia concreto— sin
// introducir la maquinaria de rangos y dependencias de un orquestador.
type Parametros map[string]string

// Dia devuelve el parametro "dia" como fecha, si viene y es valido.
func (p Parametros) Dia() (time.Time, bool) {
	v, ok := p["dia"]
	if !ok || v == "" {
		return time.Time{}, false
	}
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// Tarea es una unidad de trabajo programada.
type Tarea struct {
	// Nombre identifica la tarea en registros, metricas y cerrojos.
	Nombre string
	// Hora es el momento de ejecucion en formato "HH:MM", hora local del
	// proceso. Se documenta la zona horaria en el despliegue.
	//
	// Se usa para el trabajo pesado que debe correr fuera de horas: agregar el
	// dia, purgar el historico.
	Hora string

	// Cada ejecuta la tarea a intervalos en lugar de a una hora fija.
	//
	// Es excluyente con Hora. Existe para el trabajo que acompana al flujo en
	// vez de cerrarlo: calcular la ruta de las alertas nuevas no tiene sentido
	// a las tres de la madrugada, porque para entonces el operador ya miro el
	// mapa y lo vio vacio.
	//
	// El minimo es un minuto, que es el tic del planificador. Pedir menos no
	// daria error pero tampoco se cumpliria, y una promesa que no se cumple es
	// peor que una restriccion declarada.
	Cada time.Duration
	// Correr ejecuta el trabajo. Los parametros llegan vacios en las
	// ejecuciones programadas.
	Correr func(context.Context, Parametros) (Resultado, error)
}

// Observador recibe el resultado de cada ejecucion, para instrumentarla.
type Observador interface {
	Inicio(tarea string)
	Exito(tarea string, duracion time.Duration, afectadas int64)
	Fallo(tarea string, duracion time.Duration, err error)
	Omitida(tarea string, motivo string)
}

// Scheduler ejecuta tareas a su hora.
type Scheduler struct {
	rdb        *redis.Client
	tareas     []Tarea
	observador Observador
	log        *slog.Logger
	ubicacion  *time.Location

	// ahora es inyectable para poder probar el planificador sin esperar.
	ahora func() time.Time
}

// New construye el planificador.
func New(rdb *redis.Client, tz string, obs Observador, log *slog.Logger) (*Scheduler, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Una zona horaria mal escrita desplazaria todas las ejecuciones sin
		// que nadie lo note hasta que la purga corra a media tarde.
		return nil, fmt.Errorf("zona horaria %q invalida: %w", tz, err)
	}

	return &Scheduler{
		rdb:        rdb,
		observador: obs,
		log:        log,
		ubicacion:  loc,
		ahora:      time.Now,
	}, nil
}

// Registrar anade una tarea. Valida el formato de la hora al registrarla, no
// al ejecutarla: un error de configuracion debe impedir el arranque, no
// descubrirse a las tres de la madrugada.
func (s *Scheduler) Registrar(t Tarea) error {
	segunHora := t.Hora != ""
	segunIntervalo := t.Cada > 0

	if segunHora == segunIntervalo {
		return fmt.Errorf("la tarea %q debe indicar Hora o Cada, y solo una de las dos", t.Nombre)
	}
	if segunHora {
		if _, err := time.Parse("15:04", t.Hora); err != nil {
			return fmt.Errorf("la tarea %q tiene una hora invalida %q: use HH:MM", t.Nombre, t.Hora)
		}
	}
	if segunIntervalo && t.Cada < time.Minute {
		return fmt.Errorf("la tarea %q pide ejecutarse cada %s, pero el planificador tiene un tic de un minuto", t.Nombre, t.Cada)
	}
	if t.Correr == nil {
		return fmt.Errorf("la tarea %q no tiene funcion de ejecucion", t.Nombre)
	}
	s.tareas = append(s.tareas, t)
	return nil
}

// Tareas devuelve las tareas registradas, para el endpoint de estado.
func (s *Scheduler) Tareas() []Tarea { return s.tareas }

// Correr bloquea hasta que se cancele el contexto.
//
// Comprueba cada minuto si alguna tarea toca. No intenta calcular el proximo
// instante exacto de ejecucion: un tic por minuto es barato, tolera cambios de
// horario de verano y evita la clase de errores aritmeticos que aparecen al
// sumar duraciones sobre husos horarios.
func (s *Scheduler) Correr(ctx context.Context) {
	s.log.Info("planificador iniciado",
		slog.Int("tareas", len(s.tareas)),
		slog.String("zona_horaria", s.ubicacion.String()))

	for _, t := range s.tareas {
		if t.Cada > 0 {
			s.log.Info("tarea programada",
				slog.String("tarea", t.Nombre),
				slog.String("cada", t.Cada.String()))
			continue
		}
		s.log.Info("tarea programada",
			slog.String("tarea", t.Nombre),
			slog.String("hora", t.Hora))
	}

	tic := time.NewTicker(time.Minute)
	defer tic.Stop()

	// Se comprueba tambien al arrancar: si el servicio estuvo caido a la hora
	// prevista y se levanta dentro del mismo minuto, la tarea aun se ejecuta.
	s.revisar(ctx)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("planificador detenido")
			return
		case <-tic.C:
			s.revisar(ctx)
		}
	}
}

// revisar ejecuta las tareas cuya hora coincide con el minuto actual.
func (s *Scheduler) revisar(ctx context.Context) {
	ahora := s.ahora().In(s.ubicacion)
	actual := ahora.Format("15:04")

	for _, t := range s.tareas {
		// Las tareas por intervalo se consultan en cada tic: quien decide si
		// toca es el cerrojo, cuya clave cambia con la ventana.
		if t.Cada > 0 {
			s.ejecutar(ctx, t, ahora)
			continue
		}
		if t.Hora != actual {
			continue
		}
		s.ejecutar(ctx, t, ahora)
	}
}

// cerrojo devuelve la clave y la vigencia del cerrojo de una tarea.
//
// Para las tareas diarias la clave lleva la fecha y dura casi un dia: asi la
// purga corre una sola vez aunque el proceso se reinicie dentro del mismo
// minuto. Para las tareas por intervalo la clave lleva el NUMERO DE VENTANA y
// dura lo que el intervalo, de modo que el propio cerrojo es el que marca el
// ritmo: el planificador consulta cada minuto y solo entra quien toma la
// ventana. Sin esto haria falta guardar en memoria la ultima ejecucion, que se
// perderia en cada reinicio y no serviria con varias instancias.
func (s *Scheduler) cerrojo(t Tarea, ahora time.Time) (string, time.Duration) {
	if t.Cada > 0 {
		ventana := ahora.Unix() / int64(t.Cada.Seconds())
		return fmt.Sprintf("jobs:ejecutada:%s:v%d", t.Nombre, ventana), t.Cada
	}
	return fmt.Sprintf("jobs:ejecutada:%s:%s", t.Nombre, ahora.Format("2006-01-02")), 23 * time.Hour
}

// ejecutar toma el cerrojo y corre la tarea.
//
// El cerrojo cumple dos funciones a la vez: impide que dos instancias del
// servicio ejecuten la misma purga simultaneamente, y garantiza que la tarea
// corra una sola vez al dia aunque el proceso se reinicie dentro del mismo
// minuto. Por eso la clave incluye la fecha y el TTL dura casi un dia entero.
func (s *Scheduler) ejecutar(ctx context.Context, t Tarea, ahora time.Time) {
	clave, ttl := s.cerrojo(t, ahora)

	tomado, err := s.rdb.SetNX(ctx, clave, ahora.Format(time.RFC3339), ttl).Result()
	if err != nil {
		s.log.Error("no se pudo consultar el cerrojo de la tarea",
			slog.String("tarea", t.Nombre),
			slog.String("error", err.Error()))
		s.observador.Omitida(t.Nombre, "cerrojo_inaccesible")
		return
	}
	if !tomado {
		// Otra instancia la ejecuto, o ya corrio en esta ventana. No es error.
		s.log.Debug("la tarea ya se ejecuto en esta ventana", slog.String("tarea", t.Nombre))
		s.observador.Omitida(t.Nombre, "ya_ejecutada")
		return
	}

	s.log.Info("ejecutando tarea", slog.String("tarea", t.Nombre))
	s.observador.Inicio(t.Nombre)

	inicio := time.Now()
	res, err := t.Correr(ctx, nil)
	duracion := time.Since(inicio)

	if err != nil {
		s.observador.Fallo(t.Nombre, duracion, err)
		s.log.Error("la tarea fallo",
			slog.String("tarea", t.Nombre),
			slog.Float64("duracion_s", duracion.Seconds()),
			slog.String("error", err.Error()))

		// Se libera el cerrojo para que un reintento manual, o la siguiente
		// instancia, pueda volver a intentarlo hoy mismo. Si se dejara puesto,
		// un fallo transitorio perderia la ejecucion del dia entero.
		if err := s.rdb.Del(context.WithoutCancel(ctx), clave).Err(); err != nil {
			s.log.Warn("no se pudo liberar el cerrojo tras el fallo",
				slog.String("tarea", t.Nombre),
				slog.String("error", err.Error()))
		}
		return
	}

	s.observador.Exito(t.Nombre, duracion, res.Afectadas)
	s.log.Info("tarea completada",
		slog.String("tarea", t.Nombre),
		slog.Float64("duracion_s", duracion.Seconds()),
		slog.Int64("afectadas", res.Afectadas),
		slog.Any("detalle", res.Detalle))
}

// EjecutarAhora corre una tarea por su nombre, saltandose el horario.
//
// Existe para poder reprocesar manualmente lo que fallo, que es la unica
// funcion de Airflow que se echa de menos de verdad. No toma el cerrojo diario:
// quien la invoca esta pidiendo explicitamente que corra.
func (s *Scheduler) EjecutarAhora(ctx context.Context, nombre string, params Parametros) (Resultado, error) {
	for _, t := range s.tareas {
		if t.Nombre != nombre {
			continue
		}

		s.log.Info("ejecucion manual solicitada",
			slog.String("tarea", nombre),
			slog.Any("parametros", params))
		s.observador.Inicio(nombre)

		inicio := time.Now()
		res, err := t.Correr(ctx, params)
		duracion := time.Since(inicio)

		if err != nil {
			s.observador.Fallo(nombre, duracion, err)
			return res, err
		}
		s.observador.Exito(nombre, duracion, res.Afectadas)
		return res, nil
	}
	return Resultado{}, fmt.Errorf("no existe la tarea %q", nombre)
}
