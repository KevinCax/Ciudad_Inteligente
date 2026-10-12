-- Rutas por calle de las alertas, para dibujar el recorrido en el mapa.
--
-- Idempotente como los anteriores: todo lleva IF NOT EXISTS y lo aplica
-- postgres-migrate en cada arranque.

-- ---------------------------------------------------------------------------
-- alert_routes: la geometria del recorrido estimado de una alerta
-- ---------------------------------------------------------------------------
--
-- IMPORTANTE, y conviene que conste en el esquema y no solo en un tablero: el
-- sistema NO sabe por donde circulo el vehiculo. Sabe que una camara lo vio en
-- varios puntos. Esta ruta es el camino por calles que un motor de rutas
-- considera mas probable entre esos puntos: es una ESTIMACION, no una medida.
-- Los puntos medidos son los de events.geo_lat/geo_lon.
--
-- La geometria se guarda como un array JSON de pares [lon, lat] —el orden de
-- GeoJSON, no el humano— en una sola fila por alerta, en lugar de una fila por
-- vertice. Un recorrido urbano tiene cientos de vertices y desgranarlos en
-- filas multiplicaria por cien el tamano de la tabla sin que nadie consulte
-- jamas un vertice suelto: siempre se lee la ruta entera.
CREATE TABLE IF NOT EXISTS alert_routes (
  alert_id     uuid PRIMARY KEY,

  -- calculada: hay geometria. sin_ruta: el motor no encontro camino (puntos
  -- fuera del mapa, o aislados). fallida: el motor no respondio; se reintenta.
  estado       text NOT NULL,

  -- Metros y segundos que el motor estima para el recorrido. Sirven para
  -- contrastar: si la ruta estimada son 8 km y entre las dos lecturas pasaron
  -- 90 segundos, el recorrido propuesto no es creible aunque exista.
  distancia_m  double precision,
  duracion_s   double precision,

  geometria    jsonb,
  intentos     int NOT NULL DEFAULT 0,
  detalle      text,
  calculado_en timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT alert_routes_estado_valido
    CHECK (estado IN ('calculada', 'sin_ruta', 'fallida'))
);

-- La tarea periodica busca alertas sin ruta: el indice es por estado y fecha,
-- que es justo como las pide.
CREATE INDEX IF NOT EXISTS idx_alert_routes_estado
  ON alert_routes (estado, calculado_en DESC);
