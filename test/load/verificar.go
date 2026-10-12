package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Verificacion de coherencia de extremo a extremo.
//
// Lanzar un escenario y ver que "salen alertas" no demuestra gran cosa. Lo que
// hay que comprobar es que lo correlacionado tenga sentido: que las evidencias
// existan y caigan dentro de la ventana que la alerta declara, que un vehiculo
// no haya tenido que volar entre dos lecturas, que a cada alerta le
// corresponda un aviso, y que lo que el tablero MUESTRA sea eso mismo.
//
// Todas las consultas van por la API de Grafana contra el mismo origen de
// datos que usan los paneles. Es deliberado: si se consultara PostgreSQL por
// separado, la verificacion podria pasar mientras el tablero sigue vacio, que
// es exactamente el fallo que ya ocurrio una vez en este proyecto.

// Resultado de una comprobacion.
type comprobacion struct {
	nombre  string
	detalle string
	err     error
	aviso   bool
}

// grafana consulta el origen de datos de PostgreSQL a traves de Grafana.
type grafana struct {
	base string
	auth string
	http *http.Client
}

func nuevoGrafana(base, usuario, clave string) *grafana {
	return &grafana{
		base: strings.TrimSuffix(base, "/"),
		auth: "Basic " + base64.StdEncoding.EncodeToString([]byte(usuario+":"+clave)),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// consultar ejecuta SQL y devuelve las filas como mapas.
func (g *grafana) consultar(sql string, desde, hasta time.Time) ([]map[string]any, error) {
	cuerpo, err := json.Marshal(map[string]any{
		"from": fmt.Sprintf("%d", desde.UnixMilli()),
		"to":   fmt.Sprintf("%d", hasta.UnixMilli()),
		"queries": []map[string]any{{
			"refId":      "A",
			"datasource": map[string]string{"type": "grafana-postgresql-datasource", "uid": "postgres-ciudad"},
			"format":     "table",
			"rawQuery":   true,
			"editorMode": "code",
			"rawSql":     sql,

			// Grafana los manda siempre; omitirlos hace que algunas versiones
			// respondan con una trama vacia en lugar de con un error.
			"intervalMs":    60000,
			"maxDataPoints": 5000,
		}},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, g.base+"/api/ds/query", strings.NewReader(string(cuerpo)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", g.auth)

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no se pudo consultar a Grafana: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	crudo, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Grafana respondio %d: %s", resp.StatusCode, recortar(string(crudo), 120))
	}

	var r struct {
		Results map[string]struct {
			Error  string `json:"error"`
			Frames []struct {
				Schema struct {
					Fields []struct {
						Name string `json:"name"`
					} `json:"fields"`
				} `json:"schema"`
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	if err := json.Unmarshal(crudo, &r); err != nil {
		return nil, fmt.Errorf("respuesta ilegible: %w", err)
	}

	var filas []map[string]any
	for _, res := range r.Results {
		if res.Error != "" {
			return nil, fmt.Errorf("%s", res.Error)
		}
		for _, f := range res.Frames {
			if len(f.Data.Values) == 0 {
				continue
			}
			n := len(f.Data.Values[0])
			for i := 0; i < n; i++ {
				fila := map[string]any{}
				for c, campo := range f.Schema.Fields {
					if c < len(f.Data.Values) && i < len(f.Data.Values[c]) {
						fila[campo.Name] = f.Data.Values[c][i]
					}
				}
				filas = append(filas, fila)
			}
		}
	}
	return filas, nil
}

// numero extrae un valor numerico de una fila, tolerando que llegue como
// cadena: PostgreSQL devuelve numeric como texto a traves de esta API.
func numero(fila map[string]any, clave string) float64 {
	switch v := fila[clave].(type) {
	case float64:
		return v
	case string:
		var f float64
		_, _ = fmt.Sscanf(v, "%g", &f)
		return f
	}
	return 0
}

func texto(fila map[string]any, clave string) string {
	if s, ok := fila[clave].(string); ok {
		return s
	}
	if fila[clave] == nil {
		return ""
	}
	return fmt.Sprintf("%v", fila[clave])
}

func recortar(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// verificar corre todas las comprobaciones y devuelve cuantas fallaron.
func verificar(g *grafana, umbralKmH, umbralSistema float64, ventana time.Duration, rutaTableros string) int {
	hasta := time.Now()
	desde := hasta.Add(-24 * time.Hour)

	var out []comprobacion

	anotar := func(nombre, detalle string, err error) {
		out = append(out, comprobacion{nombre: nombre, detalle: detalle, err: err})
	}
	advertir := func(nombre, detalle string) {
		out = append(out, comprobacion{nombre: nombre, detalle: detalle, aviso: true})
	}

	// --- 1. Hay algo que verificar ------------------------------------
	alertas, err := g.consultar(
		"SELECT type, zone, jsonb_array_length(evidence) AS evidencias FROM alerts", desde, hasta)
	if err != nil {
		anotar("hay alertas", "", err)
		imprimirVerificacion(out)
		return 1
	}
	if len(alertas) == 0 {
		anotar("hay alertas", "", fmt.Errorf("no hay ninguna alerta: lance antes un escenario"))
		imprimirVerificacion(out)
		return 1
	}
	anotar("hay alertas", fmt.Sprintf("%d alertas", len(alertas)), nil)

	// --- 2. Las evidencias existen ------------------------------------
	huerfanas, err := g.consultar(`
		SELECT count(*) AS n
		FROM alerts a
		CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		LEFT JOIN events e ON e.event_id = ev.id::uuid
		WHERE e.event_id IS NULL`, desde, hasta)
	if err != nil {
		anotar("las evidencias existen", "", err)
	} else if n := numero(huerfanas[0], "n"); n > 0 {
		anotar("las evidencias existen", "",
			fmt.Errorf("%.0f evidencias apuntan a eventos que no estan archivados", n))
	} else {
		anotar("las evidencias existen", "ninguna huerfana", nil)
	}

	// --- 3. Las evidencias caen dentro de la ventana declarada ---------
	//
	// Una alerta que dice cubrir de 10:00 a 10:05 y aporta como prueba un
	// evento de las 09:40 se contradice a si misma. Se tolera un segundo de
	// holgura por el redondeo de las marcas de tiempo.
	fuera, err := g.consultar(`
		SELECT a.type, count(*) AS n
		FROM alerts a
		CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		JOIN events e ON e.event_id = ev.id::uuid
		WHERE e.ts_utc < a.window_start - interval '1 second'
		   OR e.ts_utc > a.window_end   + interval '1 second'
		GROUP BY 1`, desde, hasta)
	if err != nil {
		anotar("las evidencias caen en la ventana", "", err)
	} else if len(fuera) > 0 {
		var partes []string
		for _, f := range fuera {
			partes = append(partes, fmt.Sprintf("%s: %.0f", texto(f, "type"), numero(f, "n")))
		}
		anotar("las evidencias caen en la ventana", "",
			fmt.Errorf("hay evidencias fuera de la ventana que declara su alerta (%s)",
				strings.Join(partes, ", ")))
	} else {
		anotar("las evidencias caen en la ventana", "todas dentro", nil)
	}

	// --- 4. Las reglas de zona no cruzan zonas -------------------------
	cruzadas, err := g.consultar(`
		SELECT a.type, count(*) AS n
		FROM alerts a
		CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		JOIN events e ON e.event_id = ev.id::uuid
		WHERE a.type <> 'vehicle_pursuit' AND e.zone IS DISTINCT FROM a.zone
		GROUP BY 1`, desde, hasta)
	if err != nil {
		anotar("las reglas de zona no cruzan zonas", "", err)
	} else if len(cruzadas) > 0 {
		anotar("las reglas de zona no cruzan zonas", "",
			fmt.Errorf("una alerta de zona esta usando evidencias de otra zona"))
	} else {
		anotar("las reglas de zona no cruzan zonas", "correcto", nil)
	}

	// --- 5. Plausibilidad de cada tramo del vehiculo -------------------
	//
	// Esta es la comprobacion central. Entre dos lecturas consecutivas del
	// MISMO vehiculo se calcula la velocidad media en linea recta. Si alguna
	// supera el tope configurado, el motor encadeno algo que ningun coche
	// pudo recorrer.
	tramos, err := g.consultar(`
		WITH lecturas AS (
		  SELECT DISTINCT a.alert_id, e.ts_utc, e.zone,
		         e.geo_lat::float8 AS lat, e.geo_lon::float8 AS lon,
		         e.payload->>'placa_vehicular' AS placa
		  FROM alerts a
		  CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		  JOIN events e ON e.event_id = ev.id::uuid
		  WHERE a.type = 'vehicle_pursuit'
		    AND e.payload ? 'placa_vehicular'
		    AND e.geo_lat IS NOT NULL
		),
		pares AS (
		  SELECT alert_id, placa, zone, ts_utc, lat, lon,
		         lag(ts_utc) OVER (PARTITION BY alert_id, placa ORDER BY ts_utc) AS t0,
		         lag(zone)   OVER (PARTITION BY alert_id, placa ORDER BY ts_utc) AS zona0,
		         lag(lat)    OVER (PARTITION BY alert_id, placa ORDER BY ts_utc) AS lat0,
		         lag(lon)    OVER (PARTITION BY alert_id, placa ORDER BY ts_utc) AS lon0
		  FROM lecturas
		)
		SELECT zona0 || ' -> ' || zone AS tramo,
		       extract(epoch FROM ts_utc - t0) AS segundos,
		       6371*2*asin(sqrt(sin(radians(lat-lat0)/2)^2
		         + cos(radians(lat0))*cos(radians(lat))*sin(radians(lon-lon0)/2)^2)) AS km
		FROM pares
		WHERE t0 IS NOT NULL AND ts_utc > t0`, desde, hasta)

	if err != nil {
		anotar("ningun tramo exige velocidad imposible", "", err)
	} else if len(tramos) == 0 {
		advertir("ningun tramo exige velocidad imposible",
			"no hay alertas de persecucion con varias lecturas: nada que medir")
	} else {
		peor := 0.0
		peorTramo := ""
		malos := 0
		for _, t := range tramos {
			seg := numero(t, "segundos")
			km := numero(t, "km")
			if seg <= 0 {
				continue
			}
			kmh := km / (seg / 3600.0)
			if kmh > peor {
				peor, peorTramo = kmh, fmt.Sprintf("%s, %.2f km en %.0f s", texto(t, "tramo"), km, seg)
			}
			if kmh > umbralKmH {
				malos++
			}
		}
		if malos > 0 {
			anotar("ningun tramo exige velocidad imposible", "",
				fmt.Errorf("%d tramos superan %.0f km/h; el peor es %.0f km/h (%s)",
					malos, umbralKmH, peor, peorTramo))
		} else {
			anotar("ningun tramo exige velocidad imposible",
				fmt.Sprintf("%d tramos, el mas rapido a %.0f km/h (tope %.0f)",
					len(tramos), peor, umbralKmH), nil)
		}
	}

	// --- 5b. El sistema no esta configurado con un tope increible ------
	//
	// La verificacion usa SU PROPIO valor de referencia, no el del sistema.
	// Es deliberado y costo descubrirlo: al leer el umbral del mismo .env que
	// configura el correlator, una configuracion absurda hacia que la
	// comprobacion fuera igual de absurda y pasara sin rechistar. Una
	// verificacion que toma sus expectativas del sistema que verifica no
	// verifica nada.
	if umbralSistema > 0 && umbralSistema > umbralKmH {
		anotar("el tope configurado es creible", "",
			fmt.Errorf("el correlator admite hasta %.0f km/h de media entre lecturas, "+
				"cuando por estas calles un coche a flujo libre no pasa de unos 34",
				umbralSistema))
	} else if umbralSistema > 0 {
		anotar("el tope configurado es creible",
			fmt.Sprintf("%.0f km/h, dentro de la referencia de %.0f", umbralSistema, umbralKmH), nil)
	}

	// --- 6. Ninguna alerta abarca mas que su ventana -------------------
	largas, err := g.consultar(fmt.Sprintf(`
		SELECT type, max(extract(epoch FROM window_end - window_start)) AS seg
		FROM alerts WHERE window_start IS NOT NULL
		GROUP BY 1 HAVING max(extract(epoch FROM window_end - window_start)) > %.0f`,
		ventana.Seconds()+5), desde, hasta)
	if err != nil {
		anotar("ninguna alerta excede su ventana", "", err)
	} else if len(largas) > 0 {
		anotar("ninguna alerta excede su ventana", "",
			fmt.Errorf("%s abarca %.0f s, mas que la ventana de %.0f s",
				texto(largas[0], "type"), numero(largas[0], "seg"), ventana.Seconds()))
	} else {
		anotar("ninguna alerta excede su ventana",
			fmt.Sprintf("tope %s", ventana), nil)
	}

	// --- 7. Toda alerta genero aviso ----------------------------------
	sinAviso, err := g.consultar(`
		SELECT count(*) AS n FROM alerts a
		WHERE NOT EXISTS (SELECT 1 FROM notifications n WHERE n.alert_id = a.alert_id)`,
		desde, hasta)
	if err != nil {
		anotar("toda alerta genero aviso", "", err)
	} else if n := numero(sinAviso[0], "n"); n > 0 {
		anotar("toda alerta genero aviso", "",
			fmt.Errorf("%.0f alertas no avisaron a nadie", n))
	} else {
		anotar("toda alerta genero aviso", "ninguna quedo sin avisar", nil)
	}

	// --- 8. Sin avisos duplicados -------------------------------------
	dup, err := g.consultar(`
		SELECT count(*) AS n FROM (
		  SELECT alert_id, organismo FROM notifications
		  GROUP BY 1,2 HAVING count(*) > 1) d`, desde, hasta)
	if err != nil {
		anotar("sin avisos duplicados", "", err)
	} else if n := numero(dup[0], "n"); n > 0 {
		anotar("sin avisos duplicados", "", fmt.Errorf("%.0f pares repetidos", n))
	} else {
		anotar("sin avisos duplicados", "idempotencia correcta", nil)
	}

	// --- 9. Rutas calculadas ------------------------------------------
	rutas, err := g.consultar(`
		SELECT coalesce(r.estado, 'pendiente') AS estado, count(*) AS n
		FROM alerts a LEFT JOIN alert_routes r ON r.alert_id = a.alert_id
		GROUP BY 1`, desde, hasta)
	if err != nil {
		anotar("recorridos calculados", "", err)
	} else {
		var partes []string
		pendientes := 0.0
		for _, r := range rutas {
			partes = append(partes, fmt.Sprintf("%s: %.0f", texto(r, "estado"), numero(r, "n")))
			if texto(r, "estado") == "pendiente" {
				pendientes = numero(r, "n")
			}
		}
		if pendientes > 0 {
			// No es un fallo: la tarea corre cada minuto y puede no haber
			// pasado todavia. Se avisa para que nadie lo lea como un error.
			advertir("recorridos calculados",
				fmt.Sprintf("%s (la tarea corre cada minuto)", strings.Join(partes, ", ")))
		} else {
			anotar("recorridos calculados", strings.Join(partes, ", "), nil)
		}
	}

	// --- 10. Coherencia entre el recorrido y el tiempo transcurrido ----
	//
	// Si el motor de rutas dice que el trayecto son seis minutos y entre la
	// primera y la ultima lectura pasaron dos, el vehiculo no pudo hacerlo.
	// Es la comprobacion de trafico, mas estricta que la de velocidad en
	// recta, porque usa el callejero real.
	coherencia, err := g.consultar(`
		WITH extremos AS (
		  SELECT a.alert_id, r.duracion_s,
		         min(e.ts_utc) AS primera, max(e.ts_utc) AS ultima
		  FROM alerts a
		  JOIN alert_routes r ON r.alert_id = a.alert_id AND r.estado = 'calculada'
		  CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		  JOIN events e ON e.event_id = ev.id::uuid
		  WHERE a.type = 'vehicle_pursuit' AND r.duracion_s > 0
		  GROUP BY 1, 2
		)
		SELECT alert_id::text, duracion_s,
		       extract(epoch FROM ultima - primera) AS transcurrido
		FROM extremos`, desde, hasta)
	if err != nil {
		anotar("el recorrido cabe en el tiempo transcurrido", "", err)
	} else if len(coherencia) == 0 {
		advertir("el recorrido cabe en el tiempo transcurrido", "sin datos que contrastar todavia")
	} else {
		incoherentes := 0
		peor := ""
		for _, c := range coherencia {
			estimado := numero(c, "duracion_s")
			real := numero(c, "transcurrido")
			// Se admite que un vehiculo que huye vaya al doble de lo que el
			// motor estima para el trafico normal. Por debajo de eso, la
			// cadena describe un trayecto que no da tiempo a hacer.
			if real > 0 && real < estimado/2 {
				incoherentes++
				peor = fmt.Sprintf("el recorrido estimado son %.0f s y solo pasaron %.0f", estimado, real)
			}
		}
		if incoherentes > 0 {
			anotar("el recorrido cabe en el tiempo transcurrido", "",
				fmt.Errorf("%d alertas describen un trayecto que no da tiempo a recorrer (%s)",
					incoherentes, peor))
		} else {
			anotar("el recorrido cabe en el tiempo transcurrido",
				fmt.Sprintf("%d alertas contrastadas", len(coherencia)), nil)
		}
	}

	// --- 11. Lo que vera el tablero -----------------------------------
	out = append(out, verificarTablero(g, rutaTableros, desde, hasta)...)

	return imprimirVerificacion(out)
}

var reVariable = regexp.MustCompile(`\$(zona|placa)\b`)

// verificarTablero ejecuta TODAS las consultas del tablero de trazabilidad.
//
// Es lo que convierte "el sistema correlaciono bien" en "el tablero lo va a
// mostrar". Ya ocurrio una vez que los datos estaban perfectos y los paneles
// aparecian vacios por una propiedad mal puesta en el origen de datos; una
// verificacion que solo mirase la base no lo habria detectado.
func verificarTablero(g *grafana, ruta string, desde, hasta time.Time) []comprobacion {
	crudo, err := os.ReadFile(filepath.Join(ruta, "04-trazabilidad.json"))
	if err != nil {
		return []comprobacion{{nombre: "consultas del tablero", err: err}}
	}

	var tablero struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				RefID  string `json:"refId"`
				RawSQL string `json:"rawSql"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(crudo, &tablero); err != nil {
		return []comprobacion{{nombre: "consultas del tablero", err: err}}
	}

	// El incidente que el tablero mostrara por defecto: el pulsador mas
	// reciente, que es lo que elige la variable al abrirse.
	incidente := ""
	if filas, err := g.consultar(
		"SELECT event_id::text AS id FROM events WHERE event_type = 'panic.button' "+
			"ORDER BY ts_utc DESC LIMIT 1", desde, hasta); err == nil && len(filas) > 0 {
		incidente = texto(filas[0], "id")
	}

	var out []comprobacion
	fallos, vacios := 0, 0

	for _, p := range tablero.Panels {
		for _, t := range p.Targets {
			if t.RawSQL == "" {
				continue
			}
			sql := reVariable.ReplaceAllString(t.RawSQL, "%")
			sql = strings.ReplaceAll(sql, "$incidente", incidente)

			filas, err := g.consultar(sql, desde, hasta)
			switch {
			case err != nil:
				fallos++
				out = append(out, comprobacion{
					nombre: "panel: " + p.Title,
					err:    fmt.Errorf("[%s] %s", t.RefID, recortar(err.Error(), 90)),
				})
			case len(filas) == 0:
				vacios++
				out = append(out, comprobacion{
					nombre:  "panel: " + p.Title,
					detalle: fmt.Sprintf("[%s] sin filas: el panel saldra vacio", t.RefID),
					aviso:   true,
				})
			}
		}
	}

	if fallos == 0 && vacios == 0 {
		out = append(out, comprobacion{
			nombre:  "consultas del tablero",
			detalle: fmt.Sprintf("%d paneles, todos con datos", len(tablero.Panels)),
		})
	}
	return out
}

// imprimirVerificacion presenta el resultado y devuelve cuantas fallaron.
func imprimirVerificacion(cs []comprobacion) int {
	fallos := 0
	fmt.Println("")
	for _, c := range cs {
		switch {
		case c.err != nil:
			fallos++
			fmt.Printf("  FALLA   %-44s %s\n", c.nombre, c.err.Error())
		case c.aviso:
			fmt.Printf("  aviso   %-44s %s\n", c.nombre, c.detalle)
		default:
			fmt.Printf("  ok      %-44s %s\n", c.nombre, c.detalle)
		}
	}
	fmt.Println("")
	if fallos == 0 {
		fmt.Println("  Verificacion superada: lo correlacionado es coherente y el tablero lo muestra.")
	} else {
		fmt.Printf("  %d comprobaciones FALLARON.\n", fallos)
	}
	fmt.Println("")
	return fallos
}
