package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Evaluacion de la deteccion contra la verdad de la simulacion.
//
// La simulacion anota que incidentes genero y con que eventos (ver verdad). El
// sistema no ve esa informacion: decide por su cuenta que es una alerta. Este
// archivo contrasta las dos cosas y responde las preguntas que con datos reales
// no tienen respuesta:
//
//   - de los incidentes que ocurrieron, ¿cuantos detecto?
//   - de las alertas que emitio, ¿cuantas eran verdad?
//   - de los pulsadores accionados por error, ¿cuantas falsas alarmas provocaron?
//
// LIMITES, para no venderlo como mas de lo que es. Las cifras miden el sistema
// frente a ESTA simulacion: dependen de las frecuencias y del ruido que se le
// dieron, que son plausibles pero no medidos. Dicen si las reglas hacen lo que
// dicen en un mundo con ruido, no cuanto acertarian en la vida real.

// alertaDB es una alerta leida de la base.
type alertaDB struct {
	ID        string
	Tipo      string
	Zona      string
	Creada    time.Time
	Evidencia []string

	// Avisos son los organismos a los que se aviso de esta alerta, con el estado
	// del aviso, tal como los registro el notificador.
	Avisos map[string]string
}

// esperado dice que tipos de alerta debe producir cada clase de incidente.
var esperado = map[string][]string{
	"robo_con_huida": {"possible_robbery", "vehicle_pursuit"},
	"accidente":      {"accident"},
	"incendio":       {"fire"},
}

// avisosEsperados es a quien debe avisarse por cada tipo de alerta, segun el
// ADR-011. Se escribe aqui y no se lee de la configuracion del notificador: si el
// evaluador usara la misma fuente que el sistema, comprobar que coinciden no
// demostraria nada.
var avisosEsperados = map[string][]string{
	"fire":                    {"ambulancia", "bomberos"},
	"accident":                {"ambulancia", "policia", "transito"},
	"possible_robbery":        {"policia"},
	"traffic_speed_violation": {"transito"},
	"vehicle_pursuit":         {"policia", "transito"},
}

// reales son las clases que SI son un incidente que el sistema debe detectar.
var reales = []string{"robo_con_huida", "accidente", "incendio"}

// ruido son las clases que NO deben provocar ninguna alerta.
var ruido = []string{"panico_falso", "ruido_falso", "altercado"}

func contiene(l []string, x string) bool {
	for _, v := range l {
		if v == x {
			return true
		}
	}
	return false
}

// cumpleLaRegla dice si un incidente genero la evidencia que la regla necesita.
//
// Un incidente puede no ser detectable por construccion: un accidente sin sensor
// acustico cerca solo deja un reporte ciudadano, y ninguna regla lo confirma. Eso
// no es un fallo de la regla, es un limite de la red de sensores, y mezclarlo con
// los fallos reales haria parecer peor al sistema de lo que es. Se cuenta aparte.
func cumpleLaRegla(v verdad, tipoAlerta string) (bool, string) {
	switch tipoAlerta {
	case "accident":
		for _, e := range v.Eventos {
			if e.Tipo == "sensor.acoustic" && (e.Detalle == "explosion" || e.Detalle == "vidrio_roto") {
				return true, ""
			}
		}
		return false, "ningun sensor acustico lo oyo"

	case "fire":
		for _, e := range v.Eventos {
			if e.Tipo == "sensor.acoustic" && (e.Detalle == "explosion" || e.Db > 100) {
				return true, ""
			}
		}
		return false, "ningun sensor acustico lo oyo"

	case "possible_robbery":
		// Panico mas un vehiculo a mas de 80 km/h EN LA MISMA ZONA que el boton.
		for _, e := range v.Eventos {
			if e.Tipo == "sensor.lpr" && e.Vel > 80 && e.Zona == v.Zona {
				return true, ""
			}
		}
		return false, "ningun coche rapido paso por la zona del boton"

	case "vehicle_pursuit":
		// Dos infracciones del mismo vehiculo, una al menos en la zona del boton.
		n, enZona := 0, false
		for _, e := range v.Eventos {
			if e.Tipo == "sensor.lpr" && (e.Vel > 80 || e.Infraccion) {
				n++
				if e.Zona == v.Zona {
					enZona = true
				}
			}
		}
		switch {
		case n < 2:
			return false, "menos de dos infracciones registradas"
		case !enZona:
			return false, "ninguna infraccion en la zona del boton"
		}
		return true, ""
	}
	return false, ""
}

// leerAlertas lee de la base todas las alertas, con su evidencia.
func leerAlertas(g *grafana) ([]alertaDB, error) {
	hasta := time.Now()
	desde := hasta.Add(-72 * time.Hour)
	filas, err := g.consultar(
		"SELECT alert_id::text AS id, type, zone, created_at, evidence::text AS evidencia "+
			"FROM alerts ORDER BY created_at", desde, hasta)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las alertas: %w", err)
	}

	var alertas []alertaDB
	for _, f := range filas {
		a := alertaDB{ID: texto(f, "id"), Tipo: texto(f, "type"), Zona: texto(f, "zone")}
		if t, err := time.Parse(time.RFC3339Nano, texto(f, "created_at")); err == nil {
			a.Creada = t
		} else if ms, ok := f["created_at"].(float64); ok {
			a.Creada = time.UnixMilli(int64(ms))
		}
		_ = json.Unmarshal([]byte(texto(f, "evidencia")), &a.Evidencia)
		a.Avisos = map[string]string{}
		alertas = append(alertas, a)
	}

	avisos, err := g.consultar(
		"SELECT alert_id::text AS id, organismo, estado FROM notifications", desde, hasta)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer los avisos: %w", err)
	}
	for _, f := range avisos {
		for i := range alertas {
			if alertas[i].ID == texto(f, "id") {
				alertas[i].Avisos[texto(f, "organismo")] = texto(f, "estado")
			}
		}
	}

	return alertas, nil
}

func evaluar(g *grafana, rutaVerdad string) error {
	crudo, err := os.ReadFile(rutaVerdad)
	if err != nil {
		return fmt.Errorf("no se pudo leer la verdad de la simulacion (%s): %w\n"+
			"  se genera al terminar ./make.sh ciudad", rutaVerdad, err)
	}
	var verdades []verdad
	if err := json.Unmarshal(crudo, &verdades); err != nil {
		return fmt.Errorf("verdad ilegible: %w", err)
	}
	if len(verdades) == 0 {
		return fmt.Errorf("la simulacion no genero ningun incidente")
	}

	// Una simulacion controlada trae lo esperado de cada caso: se evalua caso por
	// caso, que es lo que se puede explicar en una demostracion. La aleatoria solo
	// tiene conteos, y se mide por cobertura y precision.
	if verdades[0].Caso != "" {
		alertas, err := leerAlertas(g)
		if err != nil {
			return err
		}
		evaluarCasos(verdades, alertas)
		return nil
	}

	// Que incidente es dueno de cada evento.
	dueno := map[string]int{}
	for i, v := range verdades {
		for _, e := range v.Eventos {
			dueno[e.ID] = i
		}
	}

	alertas, err := leerAlertas(g)
	if err != nil {
		return err
	}

	// Clasificacion de cada alerta.
	detectado := map[int]map[string]bool{} // incidente -> tipo de alerta que lo detecto
	var verdaderas, trafico, falsas int
	causasFalsas := map[string]int{}

	for _, a := range alertas {
		tocados := map[int]bool{}
		for _, id := range a.Evidencia {
			if i, ok := dueno[id]; ok {
				tocados[i] = true
			}
		}

		esVerdadera := false
		for i := range tocados {
			if contiene(esperado[verdades[i].Clase], a.Tipo) {
				esVerdadera = true
				if detectado[i] == nil {
					detectado[i] = map[string]bool{}
				}
				detectado[i][a.Tipo] = true
			}
		}

		switch {
		case esVerdadera:
			verdaderas++
		case len(tocados) == 0 && a.Tipo == "traffic_speed_violation":
			// Tres coches por encima de 80 km/h en la misma zona: es trafico real
			// que la regla describe bien, aunque no sea un incidente simulado.
			trafico++
		default:
			falsas++
			causa := "sin ningun incidente detras"
			if len(tocados) > 0 {
				var clases []string
				for i := range tocados {
					clases = append(clases, verdades[i].Clase)
				}
				sort.Strings(clases)
				causa = fmt.Sprint(clases)
			}
			causasFalsas[a.Tipo+" por "+causa]++
		}
	}

	// ---- Informe ---------------------------------------------------------
	porClase := map[string][]int{}
	for i, v := range verdades {
		porClase[v.Clase] = append(porClase[v.Clase], i)
	}

	fmt.Println("")
	fmt.Println("  Evaluacion de la deteccion frente a la verdad de la simulacion")
	fmt.Printf("  %d incidentes simulados, %d alertas emitidas por el sistema\n\n", len(verdades), len(alertas))

	fmt.Println("  1. De los incidentes que ocurrieron, cuantos detecto")
	fmt.Println("")
	fmt.Printf("     %-30s %9s %9s %9s %10s\n", "incidente -> alerta esperada", "generados", "cumplen*", "detectados", "cobertura")

	type fila struct {
		nombre                  string
		generados, cumplen, det int
		sombra                  int
		motivos                 map[string]int
	}
	var tabla []fila

	for _, clase := range reales {
		for _, tipo := range esperado[clase] {
			f := fila{nombre: clase + " -> " + tipo, motivos: map[string]int{}}
			for _, i := range porClase[clase] {
				f.generados++
				v := verdades[i]
				ok, motivo := cumpleLaRegla(v, tipo)
				if !ok {
					f.motivos[motivo]++
					continue
				}
				f.cumplen++
				if detectado[i][tipo] {
					f.det++
					continue
				}

				// No detectado aunque cumplia la regla. Puede ser un fallo, o la
				// supresion de alertas repetidas: si hay otra del mismo tipo y
				// zona en los cinco minutos que rodean al incidente, el sistema
				// la dio por la misma. Se cuenta aparte porque es el
				// comportamiento previsto y no un fallo.
				suprimida := false
				if tipo != "vehicle_pursuit" { // la persecucion se suprime por vehiculo
					for _, a := range alertas {
						if a.Tipo == tipo && a.Zona == v.Zona {
							d := a.Creada.Sub(v.Inicio)
							if d < 0 {
								d = -d
							}
							if d <= 5*time.Minute {
								suprimida = true
								break
							}
						}
					}
				}
				if suprimida {
					f.sombra++
				}
			}
			tabla = append(tabla, f)
		}
	}

	for _, f := range tabla {
		denom := f.cumplen - f.sombra
		cob := "-"
		if denom > 0 {
			cob = fmt.Sprintf("%.0f %%", 100*float64(f.det)/float64(denom))
		}
		fmt.Printf("     %-30s %9d %9d %9d %10s\n", f.nombre, f.generados, f.cumplen, f.det, cob)
	}
	fmt.Println("")
	fmt.Println("     * cumplen: generaron la evidencia que la regla exige. Los que no la generan no")
	fmt.Println("       son un fallo de la regla sino un limite de la red de sensores:")
	for _, f := range tabla {
		for motivo, n := range f.motivos {
			fmt.Printf("         %-28s %3d  %s\n", f.nombre, n, motivo)
		}
	}
	for _, f := range tabla {
		if f.sombra > 0 {
			fmt.Printf("       %d de %s quedaron cubiertos por una alerta anterior del mismo tipo y\n"+
				"       zona (supresion de repetidas, comportamiento previsto) y no cuentan contra la cobertura.\n",
				f.sombra, f.nombre)
		}
	}

	fmt.Println("")
	fmt.Println("  2. Falsas alarmas: acciones que NO eran un incidente")
	fmt.Println("")
	fmt.Printf("     %-18s %10s %28s\n", "clase", "generados", "que provocaron alguna alerta")
	for _, clase := range ruido {
		provocaron := 0
		for _, i := range porClase[clase] {
			for _, a := range alertas {
				if tocaIncidente(a, verdades[i]) {
					provocaron++
					break
				}
			}
		}
		fmt.Printf("     %-18s %10d %28d\n", clase, len(porClase[clase]), provocaron)
	}

	fmt.Println("")
	fmt.Println("  3. De las alertas que emitio el sistema")
	fmt.Println("")
	fmt.Printf("     %-52s %4d\n", "verdaderas (detras hay un incidente simulado)", verdaderas)
	fmt.Printf("     %-52s %4d\n", "trafico real (exceso de velocidad, no es un incidente)", trafico)
	fmt.Printf("     %-52s %4d\n", "falsas", falsas)
	total := verdaderas + trafico + falsas
	if total > 0 {
		fmt.Printf("     %-52s %3.0f %%\n", "precision (verdaderas + trafico real) / total", 100*float64(verdaderas+trafico)/float64(total))
	}
	if falsas > 0 {
		fmt.Println("")
		fmt.Println("     origen de las falsas:")
		var ks []string
		for k := range causasFalsas {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("       %3d  %s\n", causasFalsas[k], k)
		}
	}
	fmt.Println("")
	fmt.Println("  Las cifras miden el sistema frente a ESTA simulacion, cuyas frecuencias y ruido son")
	fmt.Println("  plausibles pero no medidos. No son la tasa de acierto en la vida real.")
	fmt.Println("")
	return nil
}

// tocaIncidente dice si una alerta usa algun evento de un incidente.
func tocaIncidente(a alertaDB, v verdad) bool {
	ids := make(map[string]bool, len(v.Eventos))
	for _, e := range v.Eventos {
		ids[e.ID] = true
	}
	for _, id := range a.Evidencia {
		if ids[id] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Evaluacion caso por caso (simulacion controlada)
// ---------------------------------------------------------------------------

func conjunto(l []string) map[string]bool {
	m := make(map[string]bool, len(l))
	for _, x := range l {
		m[x] = true
	}
	return m
}

func lista(m map[string]bool) string {
	if len(m) == 0 {
		return "(ninguna)"
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out := ""
	for i, k := range ks {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}

// evaluarCasos contrasta, caso por caso, lo que el sistema produjo con lo que
// debia producir por diseno.
//
// "Por diseno" es importante. Un caso como "huida por calles sin camaras" espera
// NINGUNA alerta: no porque no sea un robo, sino porque sin sensores no hay nada
// que correlacionar, y eso es un limite del sistema, no un fallo. El resultado OK
// quiere decir que el sistema hizo lo que sus reglas dicen que hace; la columna de
// notas dice cuando eso es, ademas, una limitacion que conviene conocer.
func evaluarCasos(verdades []verdad, alertas []alertaDB) {
	dueno := map[string]int{}
	for i, v := range verdades {
		for _, e := range v.Eventos {
			dueno[e.ID] = i
		}
	}

	obtenido := make([]map[string]bool, len(verdades))
	for i := range obtenido {
		obtenido[i] = map[string]bool{}
	}
	atribuidas := map[string]bool{}
	// avisados[i][tipo] son los organismos a los que se aviso por la alerta de ese
	// tipo del caso i, y entregados cuantos avisos salieron bien.
	avisados := make([]map[string]map[string]bool, len(verdades))
	for i := range avisados {
		avisados[i] = map[string]map[string]bool{}
	}
	for _, a := range alertas {
		contada := map[int]bool{}
		for _, id := range a.Evidencia {
			if i, ok := dueno[id]; ok && !contada[i] {
				contada[i] = true
				obtenido[i][a.Tipo] = true
				atribuidas[a.ID] = true
				if avisados[i][a.Tipo] == nil {
					avisados[i][a.Tipo] = map[string]bool{}
				}
				for org, est := range a.Avisos {
					avisados[i][a.Tipo][org] = est == "entregado"
				}
			}
		}
	}

	fmt.Println("")
	fmt.Println("  Resultado de la simulacion controlada")
	fmt.Println("")
	fmt.Printf("  %-3s %-54s %-34s %-34s %s\n", "", "caso", "debe producir", "produjo", "resultado")

	ok, fallos, limites := 0, 0, 0
	for i, v := range verdades {
		esp := conjunto(v.Esperado)
		got := obtenido[i]

		var falta, sobra []string
		for k := range esp {
			if !got[k] {
				falta = append(falta, k)
			}
		}
		for k := range got {
			if !esp[k] {
				sobra = append(sobra, k)
			}
		}
		sort.Strings(falta)
		sort.Strings(sobra)

		res := "OK"
		switch {
		case len(falta) > 0:
			res = fmt.Sprintf("FALLA: falta %v", falta)
			fallos++
		case len(sobra) > 0:
			res = fmt.Sprintf("FALLA: sobra %v", sobra)
			fallos++
		default:
			ok++
		}
		if v.Limite != "" && res == "OK" {
			limites++
		}

		marca := " "
		if v.Limite != "" {
			marca = "*"
		}
		fmt.Printf("  %-3s %-54s %-34s %-34s %s\n", marca+"", v.Caso, lista(esp), lista(got), res)
	}

	fmt.Println("")
	fmt.Printf("  %d casos: %d correctos, %d fallos.  * = limite o comportamiento previsto (%d)\n", len(verdades), ok, fallos, limites)

	// A quien se aviso, por cada alerta que produjo cada caso.
	fmt.Println("")
	fmt.Println("  A quien se aviso (referencia: ADR-011)")
	fmt.Println("")
	fmt.Printf("  %-54s %-24s %-30s %-30s %s\n", "caso", "alerta", "debe avisar a", "aviso a", "resultado")
	avOK, avFallos := 0, 0
	for i, v := range verdades {
		tipos := make([]string, 0, len(avisados[i]))
		for t := range avisados[i] {
			tipos = append(tipos, t)
		}
		sort.Strings(tipos)
		for _, t := range tipos {
			debe := conjunto(avisosEsperados[t])
			hecho := map[string]bool{}
			sinEntregar := []string{}
			for org, entregado := range avisados[i][t] {
				hecho[org] = true
				if !entregado {
					sinEntregar = append(sinEntregar, org)
				}
			}
			res := "OK"
			switch {
			case lista(debe) != lista(hecho):
				res = "FALLA: no coincide"
				avFallos++
			case len(sinEntregar) > 0:
				res = fmt.Sprintf("FALLA: sin entregar %v", sinEntregar)
				avFallos++
			default:
				avOK++
			}
			fmt.Printf("  %-54s %-24s %-30s %-30s %s\n", v.Caso, t, lista(debe), lista(hecho), res)
		}
	}
	fmt.Println("")
	fmt.Printf("  %d alertas con aviso correcto, %d con fallos.  Un caso sin alerta no tiene avisos que comprobar.\n", avOK, avFallos)

	fmt.Println("")
	fmt.Println("  Lo que significa cada caso:")
	for _, v := range verdades {
		fmt.Printf("    - %s\n        %s\n", v.Caso, v.Nota)
		if v.Limite != "" {
			fmt.Printf("        -> %s\n", v.Limite)
		}
	}

	var extra []string
	for _, a := range alertas {
		if !atribuidas[a.ID] {
			extra = append(extra, a.Tipo+" en "+a.Zona)
		}
	}
	fmt.Println("")
	if len(extra) == 0 {
		fmt.Println("  Alertas que no pertenecen a ningun caso (trafico de fondo): ninguna")
	} else {
		sort.Strings(extra)
		fmt.Printf("  Alertas que no pertenecen a ningun caso (trafico de fondo): %d\n", len(extra))
		for _, e := range extra {
			fmt.Printf("    %s\n", e)
		}
	}
	fmt.Println("")
}
