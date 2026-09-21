package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// observadorFalso registra lo que el planificador le comunica.
type observadorFalso struct {
	mu       sync.Mutex
	exitos   []string
	fallos   []string
	omitidas []string
}

func (o *observadorFalso) Inicio(string) {}
func (o *observadorFalso) Exito(t string, _ time.Duration, _ int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.exitos = append(o.exitos, t)
}
func (o *observadorFalso) Fallo(t string, _ time.Duration, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fallos = append(o.fallos, t)
}
func (o *observadorFalso) Omitida(t, motivo string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.omitidas = append(o.omitidas, t+":"+motivo)
}

func logSilencioso() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// nuevoConRedis construye un planificador contra un Redis real.
// Si no hay ninguno disponible, la prueba se omite en lugar de fallar: estas
// comprobaciones necesitan el comportamiento atomico de SET NX, que un doble
// en memoria no reproduciria con fidelidad.
func nuevoConRedis(t *testing.T, obs Observador) (*Scheduler, *redis.Client) {
	t.Helper()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("no hay Redis en localhost:6379; se omite la prueba")
	}

	s, err := New(rdb, "UTC", obs, logSilencioso())
	if err != nil {
		t.Fatalf("no se pudo construir el planificador: %v", err)
	}
	return s, rdb
}

// Una hora mal escrita debe impedir el registro, no descubrirse de madrugada.
func TestRegistrarValidaLaHora(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	correr := func(context.Context, Parametros) (Resultado, error) { return Resultado{}, nil }

	invalidas := []string{"25:00", "3:00 am", "0300", "", "03:60"}
	for _, h := range invalidas {
		if err := s.Registrar(Tarea{Nombre: "x", Hora: h, Correr: correr}); err == nil {
			t.Errorf("la hora %q deberia rechazarse", h)
		}
	}

	if err := s.Registrar(Tarea{Nombre: "ok", Hora: "03:00", Correr: correr}); err != nil {
		t.Errorf("03:00 deberia aceptarse: %v", err)
	}
}

func TestRegistrarExigeFuncion(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	if err := s.Registrar(Tarea{Nombre: "sin_funcion", Hora: "03:00"}); err == nil {
		t.Error("una tarea sin funcion de ejecucion deberia rechazarse")
	}
}

// El cerrojo garantiza una ejecucion al dia aunque el minuto se revise varias
// veces, que es lo que ocurre si el proceso se reinicia dentro del mismo
// minuto o si hay dos instancias.
func TestSoloSeEjecutaUnaVezAlDia(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	var veces int
	fijo := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	s.ahora = func() time.Time { return fijo }

	nombre := "prueba_una_vez_" + time.Now().Format("150405.000000")
	defer rdb.Del(context.Background(), "jobs:ejecutada:"+nombre+":2026-09-21")

	if err := s.Registrar(Tarea{
		Nombre: nombre,
		Hora:   "03:00",
		Correr: func(context.Context, Parametros) (Resultado, error) {
			veces++
			return Resultado{Afectadas: 1}, nil
		},
	}); err != nil {
		t.Fatalf("no se pudo registrar: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s.revisar(ctx)
	}

	if veces != 1 {
		t.Errorf("la tarea debio correr una sola vez, corrio %d", veces)
	}
	if len(obs.omitidas) != 4 {
		t.Errorf("se esperaban 4 omisiones, hubo %d: %v", len(obs.omitidas), obs.omitidas)
	}
}

// Un fallo debe liberar el cerrojo: si se dejara puesto, un error transitorio
// perderia la ejecucion del dia entero.
func TestUnFalloLiberaElCerrojo(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	var veces int
	fijo := time.Date(2026, 9, 21, 3, 0, 0, 0, time.UTC)
	s.ahora = func() time.Time { return fijo }

	nombre := "prueba_fallo_" + time.Now().Format("150405.000000")
	defer rdb.Del(context.Background(), "jobs:ejecutada:"+nombre+":2026-09-21")

	if err := s.Registrar(Tarea{
		Nombre: nombre,
		Hora:   "03:00",
		Correr: func(context.Context, Parametros) (Resultado, error) {
			veces++
			if veces == 1 {
				return Resultado{}, errors.New("fallo transitorio")
			}
			return Resultado{Afectadas: 7}, nil
		},
	}); err != nil {
		t.Fatalf("no se pudo registrar: %v", err)
	}

	ctx := context.Background()
	s.revisar(ctx) // falla y libera
	s.revisar(ctx) // puede reintentar el mismo dia

	if veces != 2 {
		t.Errorf("tras un fallo deberia poder reintentarse; corrio %d veces", veces)
	}
	if len(obs.fallos) != 1 || len(obs.exitos) != 1 {
		t.Errorf("se esperaba 1 fallo y 1 exito, hubo %d y %d", len(obs.fallos), len(obs.exitos))
	}
}

// Fuera de su hora, la tarea no corre.
func TestNoSeEjecutaFueraDeHora(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	var veces int
	s.ahora = func() time.Time { return time.Date(2026, 9, 21, 14, 30, 0, 0, time.UTC) }

	if err := s.Registrar(Tarea{
		Nombre: "nocturna",
		Hora:   "03:00",
		Correr: func(context.Context, Parametros) (Resultado, error) { veces++; return Resultado{}, nil },
	}); err != nil {
		t.Fatalf("no se pudo registrar: %v", err)
	}

	s.revisar(context.Background())

	if veces != 0 {
		t.Errorf("no deberia correr a las 14:30 una tarea programada a las 03:00")
	}
}

// La ejecucion manual ignora el horario y el cerrojo: quien la invoca esta
// pidiendo explicitamente que corra.
func TestEjecucionManualIgnoraHorarioYCerrojo(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	var veces int
	s.ahora = func() time.Time { return time.Date(2026, 9, 21, 14, 30, 0, 0, time.UTC) }

	if err := s.Registrar(Tarea{
		Nombre: "manual",
		Hora:   "03:00",
		Correr: func(context.Context, Parametros) (Resultado, error) {
			veces++
			return Resultado{Afectadas: 42}, nil
		},
	}); err != nil {
		t.Fatalf("no se pudo registrar: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		res, err := s.EjecutarAhora(ctx, "manual", nil)
		if err != nil {
			t.Fatalf("la ejecucion manual fallo: %v", err)
		}
		if res.Afectadas != 42 {
			t.Errorf("se esperaban 42 filas afectadas, se obtuvieron %d", res.Afectadas)
		}
	}

	if veces != 3 {
		t.Errorf("la ejecucion manual deberia correr siempre; corrio %d de 3", veces)
	}
}

func TestEjecucionManualDeTareaInexistente(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	if _, err := s.EjecutarAhora(context.Background(), "no_existe", nil); err == nil {
		t.Error("se esperaba error al ejecutar una tarea inexistente")
	}
}

// Una zona horaria mal escrita desplazaria todas las ejecuciones sin que nadie
// lo note hasta que la purga corra a media tarde.
func TestZonaHorariaInvalidaImpideConstruir(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	defer rdb.Close()

	if _, err := New(rdb, "Marte/Olympus", &observadorFalso{}, logSilencioso()); err == nil {
		t.Error("una zona horaria invalida deberia impedir construir el planificador")
	}
}

// El parametro "dia" es el backfill minimo: permite recalcular una fecha
// concreta en lugar de siempre la del dia anterior.
func TestParametroDia(t *testing.T) {
	casos := map[string]bool{
		"2026-09-21": true,
		"2026-02-29": false, // 2026 no es bisiesto
		"21/09/2026": false,
		"2026-9-21":  false,
		"":           false,
	}

	for valor, valido := range casos {
		p := Parametros{}
		if valor != "" {
			p["dia"] = valor
		}
		d, ok := p.Dia()
		if ok != valido {
			t.Errorf("%q: se esperaba valido=%v, se obtuvo %v", valor, valido, ok)
			continue
		}
		if ok && d.Format("2006-01-02") != valor {
			t.Errorf("%q: se parseo como %s", valor, d.Format("2006-01-02"))
		}
	}

	if _, ok := (Parametros(nil)).Dia(); ok {
		t.Error("unos parametros nulos no deberian producir fecha")
	}
}

// Los parametros deben llegar intactos a la tarea.
func TestParametrosLleganALaTarea(t *testing.T) {
	obs := &observadorFalso{}
	s, rdb := nuevoConRedis(t, obs)
	defer rdb.Close()

	var recibido Parametros
	if err := s.Registrar(Tarea{
		Nombre: "con_params",
		Hora:   "03:00",
		Correr: func(_ context.Context, p Parametros) (Resultado, error) {
			recibido = p
			return Resultado{}, nil
		},
	}); err != nil {
		t.Fatalf("no se pudo registrar: %v", err)
	}

	if _, err := s.EjecutarAhora(context.Background(), "con_params",
		Parametros{"dia": "2026-09-15"}); err != nil {
		t.Fatalf("la ejecucion manual fallo: %v", err)
	}

	if recibido["dia"] != "2026-09-15" {
		t.Errorf("la tarea recibio %v", recibido)
	}
}
