package rules

import (
	"testing"
	"time"

	"ciudadinteligente/correlator/internal/canonical"
)

// evz construye un evento situado en una zona concreta.
//
// Las reglas de zona no necesitan el campo, porque todos los eventos de su
// ventana comparten zona. La de vehiculo si: su razon de ser es seguir a un
// coche que cambia de zona.
func evz(id, tipo, zona string, offsetSeg int, payload map[string]any) Evento {
	e := ev(id, tipo, offsetSeg, payload)
	e.Zona = zona
	return e
}

func ventanaVehiculo(panicos []Evento, eventos ...Evento) VentanaVehiculo {
	ahora := base
	for _, e := range append(append([]Evento{}, eventos...), panicos...) {
		if e.Momento.After(ahora) {
			ahora = e.Momento
		}
	}
	return VentanaVehiculo{
		Token:   "plate_prueba",
		Ahora:   ahora,
		Eventos: eventos,
		Panicos: panicos,
	}
}

func lecturaRoja(id, zona string, offsetSeg int) Evento {
	return evz(id, canonical.TypeSensorLPR, zona, offsetSeg, map[string]any{
		"placa_vehicular": "P555AAA",
		"infraccion":      InfraccionSemaforoRojo,
	})
}

func lecturaRapida(id, zona string, offsetSeg int, vel float64) Evento {
	return evz(id, canonical.TypeSensorLPR, zona, offsetSeg, map[string]any{
		"placa_vehicular":    "P555AAA",
		"velocidad_estimada": vel,
	})
}

func panico(id, zona string, offsetSeg int) Evento {
	return evz(id, canonical.TypePanicButton, zona, offsetSeg, map[string]any{
		"tipo_de_alerta": "panico",
	})
}

// La cadena completa del caso real: alguien pulsa el boton por un robo, el
// mismo coche se salta un semaforo y despues circula a velocidad excesiva, ya
// en otra zona.
func TestPersecucionCadenaCompleta(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_1", 0)},
		lecturaRoja("l1", "zona_1", 120),
		lecturaRapida("l2", "zona_2", 240, 110),
	)

	det := buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit)
	if det == nil {
		t.Fatal("la cadena panico -> semaforo -> velocidad deberia producir una alerta de persecucion")
	}

	// La zona es donde esta el coche AHORA, no donde empezo: es la que
	// necesita quien tiene que interceptarlo.
	if det.Zona != "zona_2" {
		t.Fatalf("la alerta deberia situarse en la ultima zona del vehiculo, no en %q", det.Zona)
	}

	// La evidencia empieza por el ancla para que el incidente se lea en orden
	// causal: primero el hecho, luego la huida.
	if len(det.Evidencia) != 3 || det.Evidencia[0] != "p1" {
		t.Fatalf("la evidencia deberia empezar por el boton de panico: %v", det.Evidencia)
	}
	if det.Inicio != v.Panicos[0].Momento {
		t.Fatalf("la ventana deberia empezar en el panico, empieza en %s", det.Inicio)
	}
}

// Dos infracciones sin un hecho que las origine son un conductor imprudente,
// no una huida. Emitir "persecucion" por eso llenaria el tablero de falsas
// alarmas hasta que el operador dejara de mirarlo.
func TestDosInfraccionesSinPanicoNoDisparan(t *testing.T) {
	v := ventanaVehiculo(nil,
		lecturaRoja("l1", "zona_1", 0),
		lecturaRapida("l2", "zona_2", 120, 110),
	)

	res := EvaluarVehiculo(v, ConfigPorDefecto())
	if buscar(res, canonical.AlertVehiclePursuit) != nil {
		t.Fatal("sin boton de panico no deberia haber alerta de persecucion")
	}

	// Pero si debe quedar registrada como coincidencia parcial: es el material
	// del recuento de falsos positivos.
	p := buscarParcial(res, NombreReglaPersecucion)
	if p == nil {
		t.Fatal("deberia registrarse una coincidencia parcial")
	}
	if p.Faltante != "boton_de_panico_en_la_zona" {
		t.Fatalf("el campo faltante deberia nombrar el ancla, dice %q", p.Faltante)
	}
}

// Un boton de panico al otro lado de la ciudad no tiene relacion con este
// coche. Aceptarlo convertiria la regla en un generador de coincidencias.
func TestPanicoEnZonaAjenaNoAncla(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_18", 0)},
		lecturaRoja("l1", "zona_1", 60),
		lecturaRapida("l2", "zona_2", 120, 110),
	)

	if buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("un panico en una zona que el vehiculo no piso no deberia anclar la regla")
	}
}

// Una sola infraccion es una multa, no una persecucion.
func TestUnaSolaInfraccionNoDispara(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_1", 0)},
		lecturaRoja("l1", "zona_1", 60),
	)

	res := EvaluarVehiculo(v, ConfigPorDefecto())
	if buscar(res, canonical.AlertVehiclePursuit) != nil {
		t.Fatal("con una sola infraccion no deberia haber alerta")
	}
	if buscarParcial(res, NombreReglaPersecucion) != nil {
		t.Fatal("tampoco deberia registrarse parcial: no hay patron de huida que completar")
	}
}

// Lo que queda fuera de la ventana no cuenta. Sin esto, un coche multado dos
// veces en una semana apareceria como persecucion.
func TestInfraccionesFueraDeVentanaNoCuentan(t *testing.T) {
	cfg := ConfigPorDefecto()

	vieja := lecturaRoja("l1", "zona_1", 0)
	reciente := lecturaRapida("l2", "zona_1", int(cfg.VentanaPersecucion.Seconds())+600, 110)

	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_1", int(cfg.VentanaPersecucion.Seconds())+590)},
		vieja, reciente,
	)

	if buscar(EvaluarVehiculo(v, cfg), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("una infraccion anterior a la ventana no deberia contar")
	}
}

// Velocidad por debajo del umbral no es infraccion: un coche leido dos veces
// circulando con normalidad no es una huida.
func TestLecturasNormalesNoSonInfraccion(t *testing.T) {
	cfg := ConfigPorDefecto()

	eventos := []Evento{
		lecturaRapida("l1", "zona_1", 0, 40),
		lecturaRapida("l2", "zona_2", 60, 55),
	}

	if n := len(Infracciones(eventos, base.Add(-time.Minute), cfg)); n != 0 {
		t.Fatalf("no deberia contarse ninguna infraccion, se contaron %d", n)
	}
}

// El semaforo en rojo vale por si solo, sin importar la velocidad: la camara
// ya decidio que hubo cruce indebido y no corresponde al correlator dudarlo.
func TestSemaforoRojoEsInfraccionAunqueVayaLento(t *testing.T) {
	cfg := ConfigPorDefecto()

	lenta := evz("l1", canonical.TypeSensorLPR, "zona_1", 0, map[string]any{
		"placa_vehicular":    "P555AAA",
		"infraccion":         InfraccionSemaforoRojo,
		"velocidad_estimada": 30.0,
	})

	if n := len(Infracciones([]Evento{lenta}, base.Add(-time.Minute), cfg)); n != 1 {
		t.Fatalf("el cruce en rojo deberia contar como infraccion, se contaron %d", n)
	}
}

func TestZonasVisitadasNoRepite(t *testing.T) {
	eventos := []Evento{
		lecturaRoja("l1", "zona_1", 0),
		lecturaRapida("l2", "zona_1", 30, 110),
		lecturaRapida("l3", "zona_2", 60, 110),
	}

	zonas := ZonasVisitadas(eventos, base.Add(-time.Minute))
	if len(zonas) != 2 {
		t.Fatalf("se esperaban 2 zonas distintas, hay %v", zonas)
	}
}

// De varios panicos validos se toma el mas proximo a la primera infraccion.
//
// Es el caso que se vio fallar sobre el sistema en ejecucion: dos incidentes
// solapados en la misma zona, y la huida del segundo anclandose en el pulsador
// del primero. La alerta mezclaba dos incidentes en una sola cadena.
func TestSeAnclaEnElPanicoMasProximoALaHuida(t *testing.T) {
	v := ventanaVehiculo(
		// p1 es el mas antiguo; p2 es el que ocurre justo antes de la huida.
		[]Evento{panico("p1", "zona_1", 10), panico("p2", "zona_1", 110)},
		lecturaRoja("l1", "zona_1", 120),
		lecturaRapida("l2", "zona_1", 180, 110),
	)

	det := buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit)
	if det == nil {
		t.Fatal("deberia haber alerta")
	}
	if det.Evidencia[0] != "p2" {
		t.Fatalf("deberia anclarse en el panico contiguo a la huida, se anclo en %q", det.Evidencia[0])
	}
}

// La victima puede pulsar el boton DESPUES de ver arrancar al coche. Ese caso
// debe seguir produciendo alerta, y la ventana no puede empezar despues de una
// evidencia que ella misma contiene.
func TestPanicoPosteriorAlaPrimeraInfraccionSigueAnclando(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_1", 130)},
		lecturaRoja("l1", "zona_1", 120),
		lecturaRapida("l2", "zona_2", 180, 110),
	)

	det := buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit)
	if det == nil {
		t.Fatal("un boton pulsado justo despues de la fuga deberia anclar igual")
	}
	if det.Inicio != base.Add(120*time.Second) {
		t.Fatalf("la ventana debe empezar en la primera evidencia, empieza en %s", det.Inicio)
	}
}

// ---------------------------------------------------------------------------
// Plausibilidad fisica de la cadena
// ---------------------------------------------------------------------------

// Coordenadas reales de dos zonas alejadas de la capital: 6.57 km entre ellas.
var (
	zona10 = [2]float64{14.5995, -90.5130}
	zona6  = [2]float64{14.6560, -90.4950}
)

func en(e Evento, coord [2]float64) Evento {
	lat, lon := coord[0], coord[1]
	e.Lat, e.Lon = &lat, &lon
	return e
}

// El caso que se vio fallar: un coche en la zona 10 y el mismo dos segundos
// despues en la zona 6. Son 6.57 km, que exigirian casi doce mil km/h.
func TestNoEncadenaZonasInalcanzables(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{en(panico("p1", "zona_10", 0), zona10)},
		en(lecturaRoja("l1", "zona_10", 2), zona10),
		en(lecturaRapida("l2", "zona_6", 4, 110), zona6),
	)

	if buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("no deberia encadenar dos zonas separadas 6.57 km con dos segundos de diferencia")
	}
}

// El mismo recorrido con tiempo realista SI es una huida: lo que se descarta
// es el salto imposible, no la correlacion entre zonas distintas.
//
// Los 700 segundos no son un numero redondo elegido a ojo: es lo que el
// callejero real de la capital dice que se tarda de la zona 10 a la zona 6,
// 7.77 km de asfalto a flujo libre.
func TestSiEncadenaZonasLejanasConTiempoRealista(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{en(panico("p1", "zona_10", 0), zona10)},
		en(lecturaRoja("l1", "zona_10", 10), zona10),
		en(lecturaRapida("l2", "zona_6", 700, 110), zona6),
	)

	det := buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit)
	if det == nil {
		t.Fatal("el trayecto en el tiempo que marca el callejero debe encadenarse")
	}
	if det.Zona != "zona_6" {
		t.Fatalf("la alerta deberia situarse donde esta el vehiculo ahora: %q", det.Zona)
	}
}

// El caso que motivo bajar el umbral de 150 a 70 km/h: medio minuto para
// cruzar 6.57 km.
//
// Fisicamente son 788 km/h, asi que el tope antiguo tambien lo habria
// rechazado. Lo que el tope antiguo NO rechazaba eran los casos intermedios,
// como el de la prueba siguiente.
func TestNoEncadenaMedioMinutoParaCruzarLaCiudad(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{en(panico("p1", "zona_10", 0), zona10)},
		en(lecturaRoja("l1", "zona_10", 10), zona10),
		en(lecturaRapida("l2", "zona_6", 40, 110), zona6),
	)

	if buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("treinta segundos para cruzar 6.57 km de ciudad no es un coche rapido")
	}
}

// El caso intermedio, que es el que de verdad justifica el cambio.
//
// 6.57 km en 200 segundos son 118 km/h en linea recta: por debajo del tope
// antiguo de 150, de modo que el motor lo daba por bueno. Pero por esas calles
// el trayecto son 7.77 km, lo que exigiria 140 km/h de media sorteando
// semaforos. No es creible, y ahora se rechaza.
func TestNoEncadenaVelocidadImposibleEnCiudadAunqueSeaFisicamentePosible(t *testing.T) {
	v := ventanaVehiculo(
		[]Evento{en(panico("p1", "zona_10", 0), zona10)},
		en(lecturaRoja("l1", "zona_10", 10), zona10),
		en(lecturaRapida("l2", "zona_6", 200, 110), zona6),
	)

	if buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("118 km/h de media en linea recta por una ciudad con semaforos no es creible")
	}
}

// Una lectura imposible no debe arrastrar a las que vienen despues: se
// descarta ella y la cadena continua desde la ultima aparicion valida.
func TestLecturaFalsaNoRompeLaCadena(t *testing.T) {
	cfg := ConfigPorDefecto()

	apariciones := []Evento{
		en(lecturaRoja("l1", "zona_10", 0), zona10),
		en(lecturaRapida("falsa", "zona_6", 2, 110), zona6),  // imposible
		en(lecturaRapida("l2", "zona_10", 120, 110), zona10), // sigue en la zona: posible
	}

	cadena := CadenaPlausible(apariciones, cfg)
	if len(cadena) != 2 {
		t.Fatalf("se esperaban 2 apariciones validas, hay %d", len(cadena))
	}
	if cadena[0].ID != "l1" || cadena[1].ID != "l2" {
		t.Fatalf("se descarto la aparicion equivocada: %v", []string{cadena[0].ID, cadena[1].ID})
	}
}

// Sin coordenadas no hay nada que juzgar: descartar por falta de dato
// convertiria un hueco de informacion en una correlacion perdida.
func TestSinCoordenadasNoSeDescarta(t *testing.T) {
	apariciones := []Evento{
		lecturaRoja("l1", "zona_10", 0),
		lecturaRapida("l2", "zona_6", 2, 110),
	}

	if n := len(CadenaPlausible(apariciones, ConfigPorDefecto())); n != 2 {
		t.Fatalf("sin coordenadas deberian conservarse las 2, quedan %d", n)
	}
}

func TestDistanciaEntreZonasConocidas(t *testing.T) {
	a := en(lecturaRoja("a", "zona_10", 0), zona10)
	b := en(lecturaRoja("b", "zona_6", 0), zona6)

	km, ok := DistanciaKm(a, b)
	if !ok {
		t.Fatal("deberia poder calcularse")
	}
	// Valor de referencia calculado aparte: 6.57 km.
	if km < 6.4 || km > 6.8 {
		t.Fatalf("la distancia zona_10 - zona_6 deberia rondar 6.57 km, dio %.2f", km)
	}
}

// ---------------------------------------------------------------------------
// Casos que solo aparecieron con trafico realista
// ---------------------------------------------------------------------------

// Un coche rapido leido a 1.7 km del boton, un minuto ANTES de que lo pulsen, no
// puede estar huyendo de el. Se vio como un robo con huida: la regla solo
// comparaba zonas, y una zona mide varios kilometros.
func TestRoboRechazaVehiculoQueNoPudoEstarEnElBoton(t *testing.T) {
	boton := [2]float64{14.5927, -90.4888}
	camara := [2]float64{14.6076, -90.4868} // ~1.7 km

	v := ventana(
		en(ev("cam", canonical.TypeSensorLPR, 0, map[string]any{"velocidad_estimada": 95.0}), camara),
		en(ev("btn", canonical.TypePanicButton, 59, map[string]any{"tipo_de_alerta": "panico"}), boton),
	)

	res := Evaluar(v, ConfigPorDefecto())
	if buscar(res, canonical.AlertPossibleRobbery) != nil {
		t.Fatal("un coche a 1.7 km un minuto antes del pulsador no puede estar huyendo de el")
	}
	if buscarParcial(res, canonical.AlertPossibleRobbery) == nil {
		t.Fatal("deberia quedar como coincidencia parcial: es un pulsador sin complemento")
	}
}

// El mismo coche, a 200 m del boton, si es creible.
func TestRoboAceptaVehiculoCercanoAlBoton(t *testing.T) {
	boton := [2]float64{14.5927, -90.4888}
	camara := [2]float64{14.5935, -90.4890} // ~100 m

	v := ventana(
		en(ev("btn", canonical.TypePanicButton, 0, map[string]any{"tipo_de_alerta": "panico"}), boton),
		en(ev("cam", canonical.TypeSensorLPR, 40, map[string]any{"velocidad_estimada": 95.0}), camara),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) == nil {
		t.Fatal("un coche rapido junto al boton, justo despues de pulsarlo, es un robo con huida")
	}
}

// Si el coche tuvo tiempo de llegar, tambien cuenta: la victima puede pulsar el
// boton despues de ver arrancar el coche.
func TestRoboAceptaVehiculoPrevioSiLeDioTiempoDeLlegar(t *testing.T) {
	boton := [2]float64{14.5927, -90.4888}
	camara := [2]float64{14.5990, -90.4888} // ~700 m, a 100 s: 25 km/h

	v := ventana(
		en(ev("cam", canonical.TypeSensorLPR, 0, map[string]any{"velocidad_estimada": 95.0}), camara),
		en(ev("btn", canonical.TypePanicButton, 100, map[string]any{"tipo_de_alerta": "panico"}), boton),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) == nil {
		t.Fatal("700 m en 100 segundos son 25 km/h: perfectamente posible")
	}
}

// El sensor acustico oye el golpe y el reporte llega despues. La ventana de la
// alerta debe empezar en lo PRIMERO que ocurrio, no en el reporte: si no, declara
// un inicio posterior a parte de su propia evidencia.
func TestLaVentanaCubreLaEvidenciaAnteriorAlDisparador(t *testing.T) {
	v := ventana(
		ev("ac", canonical.TypeSensorAcustic, 0, map[string]any{"tipo_sonido_detectado": "vidrio_roto"}),
		ev("rep", canonical.TypeCitizenReport, 100, map[string]any{"tipo_evento": "accidente"}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident)
	if det == nil {
		t.Fatal("deberia haber alerta de accidente")
	}
	if !det.Inicio.Equal(base) {
		t.Fatalf("la ventana debe empezar con el sensor acustico (%s), empieza en %s", base, det.Inicio)
	}
}

func TestCompatiblesSinCoordenadasNoDescarta(t *testing.T) {
	a := ev("a", canonical.TypeSensorLPR, 0, nil)
	b := ev("b", canonical.TypePanicButton, 30, nil)
	if !Compatibles(a, b, 70) {
		t.Fatal("sin coordenadas no hay nada que juzgar: ante la duda se acepta")
	}
}

// Un coche leido siete minutos ANTES del pulsador, a 6.7 km, no es una
// persecucion de ese pulsador. Se vio como una de las ocho "persecuciones" de una
// simulacion que solo habia generado cuatro huidas.
func TestNoAnclaEnUnPulsadorPosteriorEnMuchoTiempo(t *testing.T) {
	cfg := ConfigPorDefecto()
	v := ventanaVehiculo(
		[]Evento{panico("p1", "zona_1", 437)}, // 7 min despues de la primera infraccion
		lecturaRoja("l1", "zona_1", 0),
		lecturaRapida("l2", "zona_1", 30, 110),
	)

	if buscar(EvaluarVehiculo(v, cfg), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("un pulsador 7 minutos despues de la primera infraccion no ancla la persecucion")
	}
}

func TestNoAnclaSiElVehiculoNoPudoEstarEnElPulsador(t *testing.T) {
	boton := [2]float64{14.5927, -90.4888}
	lejos := [2]float64{14.6560, -90.4950} // ~7 km

	v := ventanaVehiculo(
		[]Evento{en(panico("p1", "zona_1", 40), boton)},
		en(lecturaRoja("l1", "zona_1", 0), lejos),
		en(lecturaRapida("l2", "zona_1", 20, 110), lejos),
	)

	if buscar(EvaluarVehiculo(v, ConfigPorDefecto()), canonical.AlertVehiclePursuit) != nil {
		t.Fatal("un coche a 7 km del pulsador 40 segundos antes no puede estar huyendo de el")
	}
}
