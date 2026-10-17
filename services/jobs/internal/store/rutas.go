package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Estados de una ruta. Coinciden con el CHECK de alert_routes: si divergieran,
// la base rechazaria la fila y el fallo llegaria como un error de escritura sin
// explicacion.
const (
	RutaCalculada = "calculada"
	RutaSinRuta   = "sin_ruta"
	RutaFallida   = "fallida"
)

// Punto es una coordenada por la que paso el vehiculo.
type Punto struct {
	Lat, Lon float64
	Momento  time.Time
}

// AlertaPendiente es una alerta cuyo recorrido hay que calcular o rehacer.
type AlertaPendiente struct {
	AlertID string

	// Aplica indica si tiene sentido trazar un recorrido para esta alerta.
	//
	// Un recorrido es la trayectoria de UN vehiculo que se aleja de un hecho.
	// Solo tiene sentido si la alerta incluye un boton de panico (el hecho) y
	// exactamente un vehiculo identificado. En un incendio las evidencias son
	// sensores distintos en el mismo instante, y en un exceso de velocidad son
	// tres coches distintos: unirlos con una ruta por calles dibuja una linea
	// que ningun vehiculo recorrio.
	Aplica bool

	// Puntos son las coordenadas del incidente EN ORDEN CRONOLOGICO: las
	// evidencias de la alerta y, a continuacion, el rastro del vehiculo hasta
	// que se le pierde el contacto.
	Puntos []Punto
}

// AlertasSinRuta devuelve las alertas recientes cuyo recorrido esta pendiente.
//
// Una alerta esta pendiente si no tiene ruta, si su calculo fallo y quedan
// intentos, o si su ruta es MAS CORTA que el rastro actual del vehiculo. Esto
// ultimo es lo que hace que la linea avance conforme el coche sigue apareciendo
// en camaras: la alerta se emite en cuanto el patron se completa y el vehiculo
// sigue circulando despues, de modo que una ruta calculada una sola vez
// terminaria en la ultima evidencia y no en el ultimo contacto.
//
// El rastro de un vehiculo termina cuando entre dos apariciones pasa mas de
// silencioMin minutos. Es una politica y no una medida: si un coche reaparece
// pasado ese plazo, eso ya no es la misma huida. El tablero usa el mismo valor,
// escrito en su propia consulta; si se cambia aqui, hay que cambiarlo alli.
//
// Las que dieron "sin ruta" solo vuelven si el rastro creció: ese resultado no
// cambia por reintentarlo, pero si aparecen puntos nuevos la pregunta es otra.
func (s *Store) AlertasSinRuta(ctx context.Context, horas, maxIntentos, limite, silencioMin int) ([]AlertaPendiente, error) {
	const sql = `
		WITH candidatas AS (
		  SELECT a.alert_id, a.created_at, a.evidence
		  FROM alerts a
		  WHERE a.created_at > now() - make_interval(hours => $1)
		),
		base AS (
		  SELECT c.alert_id, ev.id::uuid AS event_id
		  FROM candidatas c
		  CROSS JOIN LATERAL jsonb_array_elements_text(c.evidence) AS ev(id)
		),
		arranque AS (
		  -- Donde empieza el recorrido: el boton de panico, que es el hecho del
		  -- que se huye. Sin pulsador, el primer evento de la evidencia.
		  SELECT b.alert_id,
		         coalesce(min(e.ts_utc) FILTER (WHERE e.event_type = 'panic.button'),
		                  min(e.ts_utc)) AS t0
		  FROM base b JOIN events e ON e.event_id = b.event_id
		  GROUP BY b.alert_id
		),
		vehiculos AS (
		  SELECT DISTINCT b.alert_id, e.payload->>'placa_vehicular' AS placa
		  FROM base b JOIN events e ON e.event_id = b.event_id
		  WHERE e.payload ? 'placa_vehicular'
		),
		aplicables AS (
		  SELECT c.alert_id
		  FROM candidatas c
		  WHERE EXISTS (SELECT 1
		                FROM base b JOIN events e ON e.event_id = b.event_id
		                WHERE b.alert_id = c.alert_id AND e.event_type = 'panic.button')
		    AND (SELECT count(*) FROM vehiculos v WHERE v.alert_id = c.alert_id) = 1
		),
		bruto AS (
		  SELECT v.alert_id, v.placa, e.event_id, e.ts_utc,
		         lag(e.ts_utc) OVER (PARTITION BY v.alert_id, v.placa ORDER BY e.ts_utc) AS anterior
		  FROM vehiculos v
		  JOIN arranque r ON r.alert_id = v.alert_id
		  JOIN events e ON e.payload ? 'placa_vehicular'
		               AND e.payload->>'placa_vehicular' = v.placa
		               AND e.ts_utc >= r.t0
		),
		rastro AS (
		  SELECT alert_id, event_id,
		         sum(CASE WHEN anterior IS NULL
		                    OR ts_utc - anterior <= make_interval(mins => $4)
		                  THEN 0 ELSE 1 END)
		           OVER (PARTITION BY alert_id, placa ORDER BY ts_utc) AS tramo
		  FROM bruto
		),
		todos AS (
		  SELECT alert_id, event_id FROM base
		  UNION
		  SELECT alert_id, event_id FROM rastro WHERE tramo = 0
		),
		puntos AS (
		  -- Solo lo que ocurre desde el pulsador en adelante. Un coche leido ANTES
		  -- de que lo pulsen puede ser evidencia de la alerta, pero no forma parte
		  -- del camino de escape: forzar la ruta a pasar por ahi, y despues por el
		  -- pulsador, dibuja un recorrido que nadie hizo.
		  SELECT t.alert_id, e.ts_utc, e.geo_lat::float8 AS lat, e.geo_lon::float8 AS lon
		  FROM todos t
		  JOIN arranque r ON r.alert_id = t.alert_id
		  JOIN events e ON e.event_id = t.event_id
		  WHERE e.geo_lat IS NOT NULL AND e.geo_lon IS NOT NULL
		    AND e.ts_utc >= r.t0
		),
		conteo AS (
		  SELECT alert_id, count(*) AS n FROM puntos GROUP BY alert_id
		),
		pendientes AS (
		  SELECT c.alert_id, c.created_at
		  FROM candidatas c
		  LEFT JOIN alert_routes r ON r.alert_id = c.alert_id
		  LEFT JOIN conteo n ON n.alert_id = c.alert_id
		  WHERE r.alert_id IS NULL
		     OR (r.estado = 'fallida' AND r.intentos < $2)
		     OR (r.estado IN ('calculada', 'sin_ruta')
		         AND r.puntos < coalesce(n.n, 0))
		  ORDER BY c.created_at DESC
		  LIMIT $3
		)
		SELECT p.alert_id::text,
		       (p.alert_id IN (SELECT alert_id FROM aplicables)) AS aplica,
		       pt.ts_utc, pt.lat, pt.lon
		FROM pendientes p
		JOIN puntos pt ON pt.alert_id = p.alert_id
		ORDER BY p.created_at DESC, p.alert_id, pt.ts_utc`

	filas, err := s.pool.Query(ctx, sql, horas, maxIntentos, limite, silencioMin)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron listar las alertas sin ruta: %w", err)
	}
	defer filas.Close()

	// El orden de la consulta agrupa las filas de una misma alerta, de modo
	// que basta recorrerlas una vez y cortar al cambiar de identificador. Un
	// mapa habria perdido el orden de las alertas, que es el que decide cuales
	// se calculan primero cuando hay mas de las que caben en una pasada.
	var out []AlertaPendiente
	for filas.Next() {
		var id string
		var aplica bool
		var p Punto
		if err := filas.Scan(&id, &aplica, &p.Momento, &p.Lat, &p.Lon); err != nil {
			return nil, fmt.Errorf("fila ilegible: %w", err)
		}

		if len(out) == 0 || out[len(out)-1].AlertID != id {
			out = append(out, AlertaPendiente{AlertID: id, Aplica: aplica})
		}
		ultima := &out[len(out)-1]
		ultima.Puntos = append(ultima.Puntos, p)
	}
	if err := filas.Err(); err != nil {
		return nil, fmt.Errorf("no se pudo recorrer el resultado: %w", err)
	}

	return out, nil
}

// Calculo es lo que se guarda de un intento de calcular un recorrido.
type Calculo struct {
	AlertID    string
	Estado     string
	DistanciaM float64
	DuracionS  float64
	Geometria  any
	Detalle    string

	// Puntos, Desde y Hasta describen CON QUE se calculo: cuantos puntos y que
	// intervalo de tiempo abarcan. Sirven para saber cuando la ruta se quedo
	// atras respecto del rastro, y para contrastar la duracion estimada con el
	// tiempo realmente transcurrido.
	Puntos int
	Desde  time.Time
	Hasta  time.Time
}

// GuardarRuta registra el resultado del calculo, sea cual sea.
//
// Tambien se guardan los fallos y los "sin ruta". Si solo se guardaran los
// exitos, la tarea volveria a intentar lo mismo cada minuto para siempre, y
// nadie podria distinguir una alerta cuyo recorrido no se ha calculado todavia
// de otra para la que no existe.
func (s *Store) GuardarRuta(ctx context.Context, c Calculo) error {
	var geo []byte
	if c.Geometria != nil {
		b, err := json.Marshal(c.Geometria)
		if err != nil {
			return fmt.Errorf("no se pudo serializar la geometria: %w", err)
		}
		geo = b
	}

	// El contador de intentos solo sube cuando el calculo FALLA. Si subiera
	// tambien con cada recalculo correcto, una ruta que se ha ido alargando
	// conforme el vehiculo seguia apareciendo acabaria con "tres intentos
	// agotados" y, si el siguiente recalculo fallara, no se reintentaria
	// nunca.
	const sql = `
		INSERT INTO alert_routes (
			alert_id, estado, distancia_m, duracion_s, geometria,
			intentos, detalle, puntos, desde, hasta, calculado_en)
		VALUES ($1, $2, $3, $4, $5,
		        CASE WHEN $2 = 'fallida' THEN 1 ELSE 0 END,
		        $6, $7, $8, $9, now())
		ON CONFLICT (alert_id) DO UPDATE SET
			estado       = EXCLUDED.estado,
			distancia_m  = EXCLUDED.distancia_m,
			duracion_s   = EXCLUDED.duracion_s,
			geometria    = EXCLUDED.geometria,
			intentos     = CASE WHEN EXCLUDED.estado = 'fallida'
			                    THEN alert_routes.intentos + 1 ELSE 0 END,
			detalle      = EXCLUDED.detalle,
			puntos       = EXCLUDED.puntos,
			desde        = EXCLUDED.desde,
			hasta        = EXCLUDED.hasta,
			calculado_en = now()`

	var dist, dur any
	if c.Estado == RutaCalculada {
		dist, dur = c.DistanciaM, c.DuracionS
	}

	var det any
	if c.Detalle != "" {
		det = c.Detalle
	}

	var desde, hasta any
	if !c.Desde.IsZero() {
		desde, hasta = c.Desde, c.Hasta
	}

	if _, err := s.pool.Exec(ctx, sql,
		c.AlertID, c.Estado, dist, dur, geo, det, c.Puntos, desde, hasta); err != nil {
		return fmt.Errorf("no se pudo guardar la ruta: %w", err)
	}
	return nil
}
