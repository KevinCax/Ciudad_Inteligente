package rules

import (
	"testing"
	"time"

	"ciudadinteligente/correlator/internal/canonical"
)

var base = time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)

// ev construye un evento en la ventana, desplazado N segundos desde la base.
func ev(id, tipo string, offsetSeg int, payload map[string]any) Evento {
	return Evento{
		ID:      id,
		Tipo:    tipo,
		Momento: base.Add(time.Duration(offsetSeg) * time.Second),
		Payload: payload,
	}
}

func ventana(eventos ...Evento) Ventana {
	// "Ahora" es el momento del ultimo evento, como ocurre al procesar el
	// evento que dispara la evaluacion.
	ahora := base
	for _, e := range eventos {
		if e.Momento.After(ahora) {
			ahora = e.Momento
		}
	}
	return Ventana{Zona: "zona_4", Ahora: ahora, Eventos: eventos}
}

func buscar(res Resultado, tipo string) *Deteccion {
	for i := range res.Detecciones {
		if res.Detecciones[i].Tipo == tipo {
			return &res.Detecciones[i]
		}
	}
	return nil
}

func buscarParcial(res Resultado, regla string) *Parcial {
	for i := range res.Parciales {
		if res.Parciales[i].Regla == regla {
			return &res.Parciales[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// possible_robbery
// ---------------------------------------------------------------------------

func TestPosibleRoboConLPR(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, map[string]any{"tipo_de_alerta": "panico"}),
		ev("e2", canonical.TypeSensorLPR, 90, map[string]any{"velocidad_estimada": 95.0}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery)
	if det == nil {
		t.Fatal("no se genero la alerta de posible robo")
	}
	if det.Score != 0.85 {
		t.Errorf("score: esperaba 0.85, se obtuvo %v", det.Score)
	}
	if len(det.Evidencia) != 2 {
		t.Errorf("evidencia: esperaba 2 eventos, se obtuvieron %d", len(det.Evidencia))
	}
}

// sensor.speed estaba en el contrato y en los simuladores, pero ninguna regla
// del sistema Java lo consumia: entraba al sistema y no producia inferencia.
// El enunciado describe la correlacion esperada justamente con ese sensor.
func TestPosibleRoboConSensorDeVelocidad(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, map[string]any{"tipo_de_alerta": "panico"}),
		ev("e2", canonical.TypeSensorSpeed, 60, map[string]any{
			"velocidad_detectada": 85.0,
			"sensor_id":           "SPD-017",
		}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) == nil {
		t.Fatal("sensor.speed deberia poder disparar la alerta de posible robo")
	}
}

func TestPosibleRoboNoSeDisparaPorDebajoDelUmbral(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, nil),
		ev("e2", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": 75.0}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) != nil {
		t.Error("75 km/h no supera el umbral de 80, no deberia alertar")
	}
}

func TestPosibleRoboFueraDeVentana(t *testing.T) {
	// El LPR llega 3 minutos despues; la ventana de robo es de 2.
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, nil),
		ev("e2", canonical.TypeSensorLPR, 180, map[string]any{"velocidad_estimada": 95.0}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) != nil {
		t.Error("los eventos estan fuera de la ventana de 2 minutos")
	}
}

// El escenario de falso positivo del enunciado: boton de panico solo, sin
// eventos complementarios.
func TestPanicoSoloEsCandidatoAFalsoPositivo(t *testing.T) {
	v := ventana(ev("e1", canonical.TypePanicButton, 0, nil))

	res := Evaluar(v, ConfigPorDefecto())

	if len(res.Detecciones) != 0 {
		t.Fatalf("no deberia generar alertas, se generaron %d", len(res.Detecciones))
	}
	p := buscarParcial(res, canonical.AlertPossibleRobbery)
	if p == nil {
		t.Fatal("deberia registrarse como coincidencia parcial")
	}
	if p.Faltante != "vehiculo_a_alta_velocidad" {
		t.Errorf("faltante: se obtuvo %q", p.Faltante)
	}
	if !p.Expira.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("la parcial deberia expirar al cerrarse la ventana, expira %v", p.Expira)
	}
}

// ---------------------------------------------------------------------------
// accident: la regla que en el sistema Java era inalcanzable
// ---------------------------------------------------------------------------

// En el sistema Java, la lista de LPR se filtraba a velocidad > 80 y despues
// se buscaba sobre esa misma lista una caida por debajo de 40. Como todos los
// elementos superaban 80, la condicion era imposible y la alerta nunca se
// generaba, pese a estar documentada como funcional.
func TestAccidenteSeGenera(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 120, map[string]any{"tipo_sonido_detectado": "explosion"}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident)
	if det == nil {
		t.Fatal("la alerta de accidente no se genero: es exactamente el bug del sistema Java")
	}
	if det.Score != 0.85 {
		t.Errorf("sin caida de velocidad el score deberia ser 0.85, se obtuvo %v", det.Score)
	}
}

// Reproduce el escenario del simulador send_events_forced_alerts.py: una
// lectura a 90 km/h seguida de otra a 30. En el sistema Java la lectura de 30
// se descartaba por el filtro previo y la caida nunca se detectaba.
func TestAccidenteDetectaLaCaidaDeVelocidad(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 20, map[string]any{"tipo_sonido_detectado": "vidrio_roto"}),
		ev("e3", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": 90.0}),
		ev("e4", canonical.TypeSensorLPR, 40, map[string]any{"velocidad_estimada": 30.0}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident)
	if det == nil {
		t.Fatal("no se genero la alerta de accidente")
	}
	if det.Score != 0.95 {
		t.Errorf("con caida de velocidad el score deberia subir a 0.95, se obtuvo %v", det.Score)
	}

	// Las dos lecturas que evidencian la caida deben estar en la evidencia.
	tiene := map[string]bool{}
	for _, id := range det.Evidencia {
		tiene[id] = true
	}
	if !tiene["e3"] || !tiene["e4"] {
		t.Errorf("la evidencia deberia incluir las lecturas de la caida: %v", det.Evidencia)
	}
}

func TestAccidenteModoEstrictoExigeLaCaida(t *testing.T) {
	cfg := ConfigPorDefecto()
	cfg.AccidenteExigeCaida = true

	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 60, map[string]any{"tipo_sonido_detectado": "explosion"}),
	)

	res := Evaluar(v, cfg)
	if buscar(res, canonical.AlertAccident) != nil {
		t.Error("en modo estricto, sin caida de velocidad no deberia alertar")
	}
	if p := buscarParcial(res, canonical.AlertAccident); p == nil || p.Faltante != "caida_de_velocidad" {
		t.Error("deberia quedar como parcial por falta de caida de velocidad")
	}
}

func TestAccidenteIgnoraSonidosNoRelacionados(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 60, map[string]any{"tipo_sonido_detectado": "disparo"}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident) != nil {
		t.Error("un disparo no es senal de accidente segun la regla")
	}
}

// Una caida que no cruza los umbrales no debe contar: de 90 a 50 no es una
// parada brusca.
func TestCaidaInsuficienteNoSubeElScore(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 20, map[string]any{"tipo_sonido_detectado": "explosion"}),
		ev("e3", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": 90.0}),
		ev("e4", canonical.TypeSensorLPR, 40, map[string]any{"velocidad_estimada": 50.0}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident)
	if det == nil {
		t.Fatal("deberia generarse la alerta base")
	}
	if det.Score != 0.85 {
		t.Errorf("de 90 a 50 no es caida brusca, el score deberia quedar en 0.85, se obtuvo %v", det.Score)
	}
}

// La caida debe evaluarse en orden cronologico, no en el orden en que los
// eventos salen de Redis.
func TestCaidaSeEvaluaEnOrdenCronologico(t *testing.T) {
	// Se insertan desordenados a proposito: primero el lento, luego el rapido.
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "accidente"}),
		ev("e2", canonical.TypeSensorAcustic, 10, map[string]any{"tipo_sonido_detectado": "explosion"}),
		ev("e4", canonical.TypeSensorLPR, 40, map[string]any{"velocidad_estimada": 30.0}),
		ev("e3", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": 90.0}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertAccident)
	if det == nil || det.Score != 0.95 {
		t.Errorf("la caida 90->30 debe detectarse aunque los eventos lleguen desordenados (score %v)", det.Score)
	}
}

// ---------------------------------------------------------------------------
// fire
// ---------------------------------------------------------------------------

func TestIncendioPorExplosion(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "incendio"}),
		ev("e2", canonical.TypeSensorAcustic, 180, map[string]any{"tipo_sonido_detectado": "explosion"}),
	)

	det := buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertFire)
	if det == nil {
		t.Fatal("no se genero la alerta de incendio")
	}
	if det.Score != 0.95 {
		t.Errorf("score: esperaba 0.95, se obtuvo %v", det.Score)
	}
}

func TestIncendioPorNivelDeDecibeles(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "incendio"}),
		ev("e2", canonical.TypeSensorAcustic, 60, map[string]any{
			"tipo_sonido_detectado": "vidrio_roto",
			"nivel_decibeles":       112.0,
		}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertFire) == nil {
		t.Error("112 dB supera el umbral de 100 y deberia bastar como senal acustica")
	}
}

func TestIncendioNoSeDisparaConRuidoBajo(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeCitizenReport, 0, map[string]any{"tipo_evento": "incendio"}),
		ev("e2", canonical.TypeSensorAcustic, 60, map[string]any{
			"tipo_sonido_detectado": "vidrio_roto",
			"nivel_decibeles":       80.0,
		}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertFire) != nil {
		t.Error("80 dB no supera el umbral y vidrio_roto no es senal de incendio")
	}
}

// ---------------------------------------------------------------------------
// traffic_speed_violation
// ---------------------------------------------------------------------------

func TestExcesoDeVelocidadRequiereTresLecturas(t *testing.T) {
	dos := ventana(
		ev("e1", canonical.TypeSensorLPR, 0, map[string]any{"velocidad_estimada": 90.0}),
		ev("e2", canonical.TypeSensorLPR, 10, map[string]any{"velocidad_estimada": 95.0}),
	)
	if buscar(Evaluar(dos, ConfigPorDefecto()), canonical.AlertTrafficSpeedViolation) != nil {
		t.Error("con dos lecturas no deberia alertar")
	}

	tres := ventana(
		ev("e1", canonical.TypeSensorLPR, 0, map[string]any{"velocidad_estimada": 90.0}),
		ev("e2", canonical.TypeSensorLPR, 10, map[string]any{"velocidad_estimada": 95.0}),
		ev("e3", canonical.TypeSensorLPR, 20, map[string]any{"velocidad_estimada": 100.0}),
	)
	det := buscar(Evaluar(tres, ConfigPorDefecto()), canonical.AlertTrafficSpeedViolation)
	if det == nil {
		t.Fatal("con tres lecturas deberia alertar")
	}
	if det.Score != 0.90 {
		t.Errorf("score: esperaba 0.90, se obtuvo %v", det.Score)
	}
	if len(det.Evidencia) != 3 {
		t.Errorf("evidencia: esperaba 3 eventos, se obtuvieron %d", len(det.Evidencia))
	}
}

// ---------------------------------------------------------------------------
// utilidades
// ---------------------------------------------------------------------------

// Los simuladores no son consistentes: unos envian la velocidad como numero y
// otros como cadena.
func TestVelocidadComoCadena(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, nil),
		ev("e2", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": "95"}),
	)

	if buscar(Evaluar(v, ConfigPorDefecto()), canonical.AlertPossibleRobbery) == nil {
		t.Error("la velocidad enviada como cadena deberia interpretarse igual")
	}
}

func TestPayloadNuloNoRompeLaEvaluacion(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypeSensorLPR, 0, nil),
		ev("e2", canonical.TypeCitizenReport, 10, nil),
		ev("e3", canonical.TypeSensorAcustic, 20, nil),
	)

	// No debe entrar en panico ni generar alertas con payloads vacios.
	if res := Evaluar(v, ConfigPorDefecto()); len(res.Detecciones) != 0 {
		t.Errorf("no deberia generar alertas con payloads vacios: %v", res.Detecciones)
	}
}

// Una ventana vacia no debe producir nada.
func TestVentanaVacia(t *testing.T) {
	res := Evaluar(Ventana{Zona: "zona_1", Ahora: base}, ConfigPorDefecto())
	if len(res.Detecciones) != 0 || len(res.Parciales) != 0 {
		t.Error("una ventana vacia no deberia producir detecciones ni parciales")
	}
}

// Varias reglas pueden cumplirse a la vez sobre la misma ventana.
func TestVariasReglasSimultaneas(t *testing.T) {
	v := ventana(
		ev("e1", canonical.TypePanicButton, 0, nil),
		ev("e2", canonical.TypeSensorLPR, 10, map[string]any{"velocidad_estimada": 90.0}),
		ev("e3", canonical.TypeSensorLPR, 20, map[string]any{"velocidad_estimada": 95.0}),
		ev("e4", canonical.TypeSensorLPR, 30, map[string]any{"velocidad_estimada": 100.0}),
	)

	res := Evaluar(v, ConfigPorDefecto())
	if buscar(res, canonical.AlertPossibleRobbery) == nil {
		t.Error("deberia detectar posible robo")
	}
	if buscar(res, canonical.AlertTrafficSpeedViolation) == nil {
		t.Error("deberia detectar exceso de velocidad")
	}
}
