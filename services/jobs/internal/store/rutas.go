package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Estados de una ruta. Coinciden con el CHECK de alert_routes: si divergieran,
// la base rechazaria la fila y el fallo llegaria como un error de escritura sin
// explicacion.
const (
	RutaCalculada = "calculada"
	RutaSinRuta   = "sin_ruta"
	RutaFallida   = "fallida"
)

// AlertaPendiente es una alerta a la que todavia no se le calculo el recorrido.
type AlertaPendiente struct {
	AlertID string

	// Puntos son las coordenadas de sus evidencias, en orden cronologico.
	Puntos [][2]float64 // [lat, lon]
}

// AlertasSinRuta devuelve las alertas recientes que esperan calculo.
//
// Incluye las que fallaron y no agotaron los reintentos: un fallo del motor de
// rutas suele ser que todavia estaba arrancando, y descartarlas para siempre
// por eso dejaria huecos permanentes en el mapa. Las que dieron "sin ruta" NO
// vuelven: ese resultado no va a cambiar por reintentarlo.
func (s *Store) AlertasSinRuta(ctx context.Context, horas, maxIntentos, limite int) ([]AlertaPendiente, error) {
	const sql = `
		WITH pendientes AS (
		  SELECT a.alert_id, a.created_at
		  FROM alerts a
		  LEFT JOIN alert_routes r ON r.alert_id = a.alert_id
		  WHERE a.created_at > now() - make_interval(hours => $1)
		    AND (r.alert_id IS NULL
		         OR (r.estado = 'fallida' AND r.intentos < $2))
		  ORDER BY a.created_at DESC
		  LIMIT $3
		)
		SELECT p.alert_id::text, e.geo_lat::float8, e.geo_lon::float8
		FROM pendientes p
		JOIN alerts a ON a.alert_id = p.alert_id
		CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id)
		JOIN events e ON e.event_id = ev.id::uuid
		WHERE e.geo_lat IS NOT NULL AND e.geo_lon IS NOT NULL
		ORDER BY p.created_at DESC, p.alert_id, e.ts_utc`

	filas, err := s.pool.Query(ctx, sql, horas, maxIntentos, limite)
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
		var lat, lon float64
		if err := filas.Scan(&id, &lat, &lon); err != nil {
			return nil, fmt.Errorf("fila ilegible: %w", err)
		}

		if len(out) == 0 || out[len(out)-1].AlertID != id {
			out = append(out, AlertaPendiente{AlertID: id})
		}
		ultima := &out[len(out)-1]
		ultima.Puntos = append(ultima.Puntos, [2]float64{lat, lon})
	}
	if err := filas.Err(); err != nil {
		return nil, fmt.Errorf("no se pudo recorrer el resultado: %w", err)
	}

	return out, nil
}

// GuardarRuta registra el resultado del calculo, sea cual sea.
//
// Tambien se guardan los fallos y los "sin ruta". Si solo se guardaran los
// exitos, la tarea volveria a intentar lo mismo cada minuto para siempre, y
// nadie podria distinguir una alerta cuyo recorrido no se ha calculado todavia
// de otra para la que no existe.
func (s *Store) GuardarRuta(ctx context.Context, alertID, estado string,
	distanciaM, duracionS float64, geometria any, detalle string) error {

	var geo []byte
	if geometria != nil {
		b, err := json.Marshal(geometria)
		if err != nil {
			return fmt.Errorf("no se pudo serializar la geometria: %w", err)
		}
		geo = b
	}

	const sql = `
		INSERT INTO alert_routes (
			alert_id, estado, distancia_m, duracion_s, geometria,
			intentos, detalle, calculado_en)
		VALUES ($1, $2, $3, $4, $5, 1, $6, now())
		ON CONFLICT (alert_id) DO UPDATE SET
			estado       = EXCLUDED.estado,
			distancia_m  = EXCLUDED.distancia_m,
			duracion_s   = EXCLUDED.duracion_s,
			geometria    = EXCLUDED.geometria,
			intentos     = alert_routes.intentos + 1,
			detalle      = EXCLUDED.detalle,
			calculado_en = now()`

	var dist, dur any
	if estado == RutaCalculada {
		dist, dur = distanciaM, duracionS
	}

	var det any
	if detalle != "" {
		det = detalle
	}

	if _, err := s.pool.Exec(ctx, sql, alertID, estado, dist, dur, geo, det); err != nil {
		return fmt.Errorf("no se pudo guardar la ruta: %w", err)
	}
	return nil
}
