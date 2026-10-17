// Package rules implementa el motor de correlacion.
//
// Las reglas son funciones puras sobre una foto de la ventana: no tocan Redis,
// Kafka ni la base. Esa separacion es lo que permite probar cada regla con
// eventos sinteticos y sin levantar infraestructura. Esa facilidad para
// probarlas es lo que permite detectar una regla inalcanzable: una cuyas
// condiciones no puedan cumplirse a la vez y que, por tanto, nunca dispare
// aunque este escrita y documentada.
package rules

import (
	"fmt"
	"sort"
	"time"

	"ciudadinteligente/correlator/internal/canonical"
	"ciudadinteligente/correlator/internal/window"
)

// Config son los umbrales del motor.
//
// Van en configuracion y no compilados en el codigo para poder ajustarlos sin
// recompilar y para dejar trazable, via RuleVersion, con que parametros se
// genero cada alerta.
type Config struct {
	VentanaRobo      time.Duration
	VentanaAccidente time.Duration
	VentanaIncendio  time.Duration
	VentanaVelocidad time.Duration

	// VentanaPersecucion es el plazo durante el cual se siguen las
	// apariciones de un mismo vehiculo. Es mayor que las demas porque un
	// vehiculo que huye cruza varias zonas y la cadena se extiende en el
	// tiempo: con dos minutos solo se verian las infracciones de una esquina.
	//
	// NO puede superar CORRELATION_WINDOW_TTL. El ancla de la regla es un
	// boton de panico, que vive en la ventana de ZONA: si esta ventana mirase
	// mas atras que aquella, buscaria un evento que Redis ya podo y la regla
	// dejaria de disparar sin que nada lo indicara. El correlator comprueba
	// la relacion al arrancar y se niega a seguir si no se cumple.
	VentanaPersecucion time.Duration

	// MinInfraccionesPersecucion es cuantas infracciones del mismo vehiculo
	// hacen falta para hablar de huida. Con una sola no hay patron: un coche
	// que se salta un semaforo es una multa, no una persecucion.
	MinInfraccionesPersecucion int

	// VelocidadMaximaPlausible acota, en km/h, la velocidad media EN LINEA
	// RECTA que puede haber llevado un vehiculo entre dos apariciones.
	//
	// No es un limite de velocidad: es un limite de CREDIBILIDAD. Sin el, el
	// motor encadena una lectura de la zona 10 con otra de la zona 6 dos
	// segundos despues, que son 6.57 km y exigirian 11834 km/h. Se vio
	// ocurrir. Un salto asi casi nunca es un coche rapido: es una placa mal
	// leida por la camara, o una placa clonada, que son problemas reales y
	// frecuentes en los lectores automaticos.
	//
	// El valor se midio, no se estimo. Preguntando al callejero real de la
	// capital por los tramos entre zonas, un automovil a flujo libre —sin un
	// solo semaforo en rojo— promedia entre 26 y 34 km/h MEDIDOS EN RECTA,
	// porque las calles no van en linea recta: zona_1 a zona_4 son 1.69 km a
	// vuelo de pajaro y 2.74 km de asfalto. El tope se fija en el doble de esa
	// cota superior, de modo que un vehiculo que huye saltandose las normas
	// cabe de sobra y un salto imposible no.
	//
	// Un tope de 150 km/h, que es lo que habia antes, solo descartaba la
	// teletransportacion: dejaba pasar cadenas que ningun coche podria haber
	// recorrido en una ciudad con trafico.
	VelocidadMaximaPlausible float64

	// UmbralVelocidad es el minimo para considerar un vehiculo "a alta
	// velocidad", tanto en sensor.lpr como en sensor.speed.
	UmbralVelocidad float64

	// MinLPRVelocidad es cuantas lecturas de alta velocidad hacen falta para
	// la alerta de exceso de velocidad.
	MinLPRVelocidad int

	// CaidaDesde y CaidaHasta definen la "caida repentina de velocidad" que
	// refuerza la hipotesis de accidente.
	CaidaDesde float64
	CaidaHasta float64

	// UmbralDecibeles es el nivel que, por si solo, basta como senal acustica
	// de incendio.
	UmbralDecibeles float64

	// AccidenteExigeCaida decide si la caida de velocidad es obligatoria para
	// emitir la alerta de accidente o solo un refuerzo de confianza.
	//
	// La guia la describe como parte de la regla, pero los escenarios de
	// prueba documentados solo envian reporte ciudadano mas sensor acustico y
	// esperan la alerta. Por defecto es opcional y sube el score cuando
	// aparece, que modela mejor la realidad: la caida de velocidad es
	// evidencia adicional, no un requisito para que haya habido un accidente.
	// Ponerlo en true recupera la lectura estricta de la guia.
	AccidenteExigeCaida bool

	Version string
}

// ConfigPorDefecto son los umbrales de la guia del proyecto.
func ConfigPorDefecto() Config {
	return Config{
		VentanaRobo:                2 * time.Minute,
		VentanaAccidente:           5 * time.Minute,
		VentanaIncendio:            5 * time.Minute,
		VentanaVelocidad:           2 * time.Minute,
		VentanaPersecucion:         15 * time.Minute,
		MinInfraccionesPersecucion: 2,
		VelocidadMaximaPlausible:   70,
		UmbralVelocidad:            80,
		MinLPRVelocidad:            3,
		CaidaDesde:                 80,
		CaidaHasta:                 40,
		UmbralDecibeles:            100,
		AccidenteExigeCaida:        false,
		Version:                    "1.0",
	}
}

// Ventana es la foto de los eventos vigentes de una zona.
type Ventana struct {
	Zona    string
	Ahora   time.Time
	Eventos []Evento
}

// Evento es un evento ya normalizado para evaluar reglas.
//
// Zona es redundante en las reglas de zona, donde todos los eventos comparten
// la de la ventana. Hace falta para la ventana de vehiculo, que por definicion
// cruza zonas: sin este campo no se podria saber donde ocurrio cada aparicion
// del coche.
type Evento struct {
	ID      string
	Tipo    string
	Zona    string
	Momento time.Time
	Payload map[string]any

	// Lat y Lon son punteros: nil significa "se desconoce la ubicacion", que
	// no es lo mismo que el punto (0,0). La distincion importa porque las
	// reglas que miden distancia deben abstenerse ante un dato ausente en
	// lugar de calcular con una coordenada inventada.
	Lat *float64
	Lon *float64
}

// Deteccion es una regla que se cumplio.
type Deteccion struct {
	Tipo      string
	Zona      string
	Score     float64
	Evidencia []string
	Inicio    time.Time
	Fin       time.Time
}

// Parcial es una regla que encontro su evento disparador pero no el resto.
//
// Es el material del que salen los falsos positivos: una coincidencia parcial
// cuya ventana expira sin completarse es exactamente el caso que describe el
// enunciado (zona 10, boton de panico sin eventos complementarios).
type Parcial struct {
	Regla      string
	Disparador string
	Faltante   string
	Expira     time.Time
}

// Resultado de evaluar la ventana completa.
type Resultado struct {
	Detecciones []Deteccion
	Parciales   []Parcial
}

// inicioDeLaEvidencia devuelve el instante del evento MAS ANTIGUO que sostiene la
// deteccion.
//
// Cada regla fijaba el inicio de la ventana en el de su disparador: el reporte
// ciudadano en el accidente, el boton de panico en el robo. Pero el disparador no
// tiene por que ser lo primero que ocurre, y con trafico realista casi nunca lo
// es: el sensor acustico oye el golpe y el reporte llega uno o dos minutos
// despues, y un coche pasa rapido por una camara antes de que alguien pulse el
// boton. La alerta declaraba entonces una ventana que EMPEZABA DESPUES de parte
// de su propia evidencia, una contradiccion que cualquiera ve al mirarla.
func inicioDeLaEvidencia(v Ventana, d *Deteccion) time.Time {
	en := make(map[string]bool, len(d.Evidencia))
	for _, id := range d.Evidencia {
		en[id] = true
	}

	inicio := d.Inicio
	for _, e := range v.Eventos {
		if en[e.ID] && e.Momento.Before(inicio) {
			inicio = e.Momento
		}
	}
	return inicio
}

// Evaluar corre todas las reglas sobre la ventana.
func Evaluar(v Ventana, cfg Config) Resultado {
	var res Resultado

	for _, evaluar := range []func(Ventana, Config) (*Deteccion, *Parcial){
		reglaPosibleRobo,
		reglaAccidente,
		reglaIncendio,
		reglaExcesoVelocidad,
	} {
		det, parcial := evaluar(v, cfg)
		if det != nil {
			// La zona la fija el motor y no cada regla: es una propiedad de
			// la ventana evaluada, no de la regla concreta.
			det.Zona = v.Zona
			det.Inicio = inicioDeLaEvidencia(v, det)
			res.Detecciones = append(res.Detecciones, *det)
		}
		if parcial != nil {
			res.Parciales = append(res.Parciales, *parcial)
		}
	}

	return res
}

// reglaPosibleRobo: boton de panico mas un vehiculo huyendo a alta velocidad.
//
// Acepta la senal de velocidad tanto de sensor.lpr como de sensor.speed. El
// enunciado describe la correlacion esperada con los tres sensores (boton de
// panico + lectura LPR + sensor de velocidad), de modo que consultar solo las
// camaras dejaria sensor.speed como una senal que entra al sistema, se
// almacena y no produce ninguna inferencia.
func reglaPosibleRobo(v Ventana, cfg Config) (*Deteccion, *Parcial) {
	desde := v.Ahora.Add(-cfg.VentanaRobo)

	panicos := enVentana(v, canonical.TypePanicButton, desde)
	if len(panicos) == 0 {
		return nil, nil
	}

	todosPanicos := panicos

	// Solo cuentan los vehiculos que PUDIERON estar en el boton cuando se pulso.
	//
	// Comparar solo la zona no basta, y se vio fallar con trafico realista: una
	// zona mide varios kilometros, de modo que un coche rapido leido a 1.7 km del
	// boton un minuto ANTES de que lo pulsen —un conductor cualquiera, y un
	// pulsador accidental— se daba por un robo con huida. Un coche que no pudo
	// estar en el boton no puede estar huyendo de el.
	panicos, rapidos := emparejarRobo(todosPanicos, vehiculosRapidos(v, desde, cfg.UmbralVelocidad), cfg)
	if len(rapidos) == 0 {
		// Boton de panico sin evento complementario: candidato a falso
		// positivo si la ventana expira asi.
		return nil, &Parcial{
			Regla:      canonical.AlertPossibleRobbery,
			Disparador: todosPanicos[0].ID,
			Faltante:   "vehiculo_a_alta_velocidad",
			Expira:     todosPanicos[0].Momento.Add(cfg.VentanaRobo),
		}
	}

	ev := ids(panicos, rapidos)
	return &Deteccion{
		Tipo:      canonical.AlertPossibleRobbery,
		Score:     0.85,
		Evidencia: ev,
		Inicio:    panicos[0].Momento,
		Fin:       v.Ahora,
	}, nil
}

// emparejarRobo se queda con los botones y los vehiculos rapidos que pudieron
// estar relacionados entre si.
func emparejarRobo(panicos, rapidos []Evento, cfg Config) (ps, rs []Evento) {
	paniquea := make(map[string]bool, len(panicos))
	corre := make(map[string]bool, len(rapidos))

	for _, p := range panicos {
		for _, r := range rapidos {
			if Compatibles(p, r, cfg.VelocidadMaximaPlausible) {
				paniquea[p.ID], corre[r.ID] = true, true
			}
		}
	}

	for _, p := range panicos {
		if paniquea[p.ID] {
			ps = append(ps, p)
		}
	}
	for _, r := range rapidos {
		if corre[r.ID] {
			rs = append(rs, r)
		}
	}
	return ps, rs
}

// reglaAccidente: reporte ciudadano de accidente mas senal acustica.
//
// El orden de los filtros es lo delicado de esta regla, y la trampa es sutil.
// Si la lista de lecturas LPR se filtrara primero a las de velocidad mayor que
// 80 y despues se buscara sobre esa misma lista una caida por debajo de 40, la
// condicion no podria cumplirse jamas: todos los elementos superarian 80. La
// regla quedaria escrita, probada en apariencia y muerta en la practica.
//
// La correccion es conservar TODAS las lecturas de velocidad de la ventana,
// ordenarlas por momento y buscar la transicion entre lecturas consecutivas.
func reglaAccidente(v Ventana, cfg Config) (*Deteccion, *Parcial) {
	desde := v.Ahora.Add(-cfg.VentanaAccidente)

	reportes := reportesCiudadanos(v, desde, "accidente")
	if len(reportes) == 0 {
		return nil, nil
	}

	acusticos := acusticosDe(v, desde, "explosion", "vidrio_roto")
	if len(acusticos) == 0 {
		return nil, &Parcial{
			Regla:      canonical.AlertAccident,
			Disparador: reportes[0].ID,
			Faltante:   "sensor_acustico",
			Expira:     reportes[0].Momento.Add(cfg.VentanaAccidente),
		}
	}

	caida, evidenciaCaida := detectarCaidaDeVelocidad(v, desde, cfg)

	if cfg.AccidenteExigeCaida && !caida {
		return nil, &Parcial{
			Regla:      canonical.AlertAccident,
			Disparador: reportes[0].ID,
			Faltante:   "caida_de_velocidad",
			Expira:     reportes[0].Momento.Add(cfg.VentanaAccidente),
		}
	}

	// La caida de velocidad no condiciona la alerta, pero si la confianza.
	score := 0.85
	evidencia := ids(reportes, acusticos)
	if caida {
		score = 0.95
		evidencia = append(evidencia, evidenciaCaida...)
	}

	return &Deteccion{
		Tipo:      canonical.AlertAccident,
		Score:     score,
		Evidencia: evidencia,
		Inicio:    reportes[0].Momento,
		Fin:       v.Ahora,
	}, nil
}

// reglaIncendio: reporte ciudadano de incendio mas explosion o ruido intenso.
func reglaIncendio(v Ventana, cfg Config) (*Deteccion, *Parcial) {
	desde := v.Ahora.Add(-cfg.VentanaIncendio)

	reportes := reportesCiudadanos(v, desde, "incendio")
	if len(reportes) == 0 {
		return nil, nil
	}

	var acusticos []Evento
	for _, e := range enVentana(v, canonical.TypeSensorAcustic, desde) {
		if texto(e.Payload, "tipo_sonido_detectado") == "explosion" {
			acusticos = append(acusticos, e)
			continue
		}
		if db, ok := numero(e.Payload, "nivel_decibeles"); ok && db > cfg.UmbralDecibeles {
			acusticos = append(acusticos, e)
		}
	}

	if len(acusticos) == 0 {
		return nil, &Parcial{
			Regla:      canonical.AlertFire,
			Disparador: reportes[0].ID,
			Faltante:   "sensor_acustico",
			Expira:     reportes[0].Momento.Add(cfg.VentanaIncendio),
		}
	}

	return &Deteccion{
		Tipo:      canonical.AlertFire,
		Score:     0.95,
		Evidencia: ids(reportes, acusticos),
		Inicio:    reportes[0].Momento,
		Fin:       v.Ahora,
	}, nil
}

// reglaExcesoVelocidad: varias lecturas de alta velocidad en la misma zona.
func reglaExcesoVelocidad(v Ventana, cfg Config) (*Deteccion, *Parcial) {
	desde := v.Ahora.Add(-cfg.VentanaVelocidad)

	rapidos := vehiculosRapidos(v, desde, cfg.UmbralVelocidad)
	if len(rapidos) < cfg.MinLPRVelocidad {
		return nil, nil
	}

	return &Deteccion{
		Tipo:      canonical.AlertTrafficSpeedViolation,
		Score:     0.90,
		Evidencia: ids(rapidos),
		Inicio:    rapidos[0].Momento,
		Fin:       v.Ahora,
	}, nil
}

// detectarCaidaDeVelocidad busca una transicion brusca entre dos lecturas
// consecutivas de velocidad en la ventana.
//
// Conserva todas las lecturas, no solo las rapidas. Filtrarlas antes de
// buscar la transicion es justo lo que haria imposible detectarla: una caida
// va de una lectura rapida a una lenta, y la lenta desaparece con el filtro.
func detectarCaidaDeVelocidad(v Ventana, desde time.Time, cfg Config) (bool, []string) {
	lecturas := todasLasLecturasDeVelocidad(v, desde)
	if len(lecturas) < 2 {
		return false, nil
	}

	sort.Slice(lecturas, func(i, j int) bool {
		return lecturas[i].momento.Before(lecturas[j].momento)
	})

	for i := 1; i < len(lecturas); i++ {
		anterior, actual := lecturas[i-1], lecturas[i]
		if anterior.velocidad > cfg.CaidaDesde && actual.velocidad < cfg.CaidaHasta {
			return true, []string{anterior.id, actual.id}
		}
	}
	return false, nil
}

type lectura struct {
	id        string
	momento   time.Time
	velocidad float64
}

// todasLasLecturasDeVelocidad reune las lecturas de sensor.lpr y sensor.speed,
// que usan nombres de campo distintos para la misma magnitud.
func todasLasLecturasDeVelocidad(v Ventana, desde time.Time) []lectura {
	var out []lectura
	for _, e := range v.Eventos {
		if e.Momento.Before(desde) {
			continue
		}
		switch e.Tipo {
		case canonical.TypeSensorLPR:
			if vel, ok := numero(e.Payload, "velocidad_estimada"); ok {
				out = append(out, lectura{e.ID, e.Momento, vel})
			}
		case canonical.TypeSensorSpeed:
			if vel, ok := numero(e.Payload, "velocidad_detectada"); ok {
				out = append(out, lectura{e.ID, e.Momento, vel})
			}
		}
	}
	return out
}

// vehiculosRapidos devuelve las lecturas por encima del umbral, ordenadas.
func vehiculosRapidos(v Ventana, desde time.Time, umbral float64) []Evento {
	var out []Evento
	for _, e := range v.Eventos {
		if e.Momento.Before(desde) {
			continue
		}
		var vel float64
		var ok bool
		switch e.Tipo {
		case canonical.TypeSensorLPR:
			vel, ok = numero(e.Payload, "velocidad_estimada")
		case canonical.TypeSensorSpeed:
			vel, ok = numero(e.Payload, "velocidad_detectada")
		}
		if ok && vel > umbral {
			out = append(out, e)
		}
	}
	ordenar(out)
	return out
}

func reportesCiudadanos(v Ventana, desde time.Time, tipo string) []Evento {
	var out []Evento
	for _, e := range enVentana(v, canonical.TypeCitizenReport, desde) {
		if texto(e.Payload, "tipo_evento") == tipo {
			out = append(out, e)
		}
	}
	return out
}

func acusticosDe(v Ventana, desde time.Time, sonidos ...string) []Evento {
	permitido := make(map[string]bool, len(sonidos))
	for _, s := range sonidos {
		permitido[s] = true
	}

	var out []Evento
	for _, e := range enVentana(v, canonical.TypeSensorAcustic, desde) {
		if permitido[texto(e.Payload, "tipo_sonido_detectado")] {
			out = append(out, e)
		}
	}
	return out
}

func enVentana(v Ventana, tipo string, desde time.Time) []Evento {
	var out []Evento
	for _, e := range v.Eventos {
		if e.Tipo == tipo && !e.Momento.Before(desde) {
			out = append(out, e)
		}
	}
	ordenar(out)
	return out
}

func ordenar(es []Evento) {
	sort.Slice(es, func(i, j int) bool { return es[i].Momento.Before(es[j].Momento) })
}

// ids extrae los event_id de varios grupos, sin repetir.
func ids(grupos ...[]Evento) []string {
	visto := map[string]bool{}
	var out []string
	for _, g := range grupos {
		for _, e := range g {
			if !visto[e.ID] {
				visto[e.ID] = true
				out = append(out, e.ID)
			}
		}
	}
	return out
}

func texto(payload map[string]any, clave string) string {
	if payload == nil {
		return ""
	}
	s, _ := payload[clave].(string)
	return s
}

// numero lee un campo numerico del payload tolerando que venga como string.
// Los simuladores no son consistentes entre si.
func numero(payload map[string]any, clave string) (float64, bool) {
	if payload == nil {
		return 0, false
	}
	switch val := payload[clave].(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(val, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// DesdeResumenes convierte los resumenes de Redis en eventos evaluables,
// descartando los que tengan un timestamp ilegible.
func DesdeResumenes(rs []window.Resumen) []Evento {
	out := make([]Evento, 0, len(rs))
	for _, r := range rs {
		momento, err := r.Momento()
		if err != nil {
			continue
		}
		out = append(out, Evento{
			ID:      r.EventID,
			Tipo:    r.EventType,
			Zona:    r.Zone,
			Momento: momento,
			Payload: r.Payload,
			Lat:     r.Lat,
			Lon:     r.Lon,
		})
	}
	return out
}
