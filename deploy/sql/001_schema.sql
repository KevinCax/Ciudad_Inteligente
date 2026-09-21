-- Esquema de Ciudad Inteligente.
--
-- Se mantiene la forma de las tablas del sistema Java para que ambos puedan
-- escribir sobre la misma base durante la migracion en paralelo y las salidas
-- sean comparables fila a fila.

-- ---------------------------------------------------------------------------
-- events: un registro por evento canonico ingerido
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS events (
  event_id        uuid PRIMARY KEY,
  event_type      text NOT NULL,
  event_version   text NOT NULL,
  producer        text NOT NULL,
  source          text NOT NULL,
  correlation_id  uuid,
  trace_id        uuid,
  partition_key   text NOT NULL,
  ts_utc          timestamptz NOT NULL,
  zone            text,
  geo_lat         numeric,
  geo_lon         numeric,
  severity        text,
  -- El payload llega con la placa ya tokenizada: el ingestor la convierte en
  -- un seudonimo HMAC antes de persistirla, porque esta base esta en la nube.
  payload         jsonb NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_ts    ON events (ts_utc DESC);
CREATE INDEX IF NOT EXISTS idx_events_type  ON events (event_type);
CREATE INDEX IF NOT EXISTS idx_events_zone  ON events (zone);
CREATE INDEX IF NOT EXISTS idx_events_pkey  ON events (partition_key);

-- Los dashboards agrupan por zona y tipo dentro de una ventana de tiempo;
-- un indice compuesto evita recorrer la tabla completa en cada refresco.
CREATE INDEX IF NOT EXISTS idx_events_zone_type_ts ON events (zone, event_type, ts_utc DESC);

-- ---------------------------------------------------------------------------
-- alerts: un registro por alerta correlacionada
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS alerts (
  alert_id        uuid PRIMARY KEY,
  correlation_id  uuid,
  type            text NOT NULL,
  -- numeric, no integer. La entidad Java declaraba score como Integer y lo
  -- redondeaba, de modo que 0.85, 0.90 y 0.95 acababan los tres como 1.
  score           numeric,
  zone            text,
  window_start    timestamptz,
  window_end      timestamptz,
  evidence        jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),

  -- El score es una confianza: fuera de [0,1] indica un error de calculo, y
  -- vale mas rechazar la fila que dejar que contamine los dashboards.
  CONSTRAINT alerts_score_rango CHECK (score IS NULL OR (score >= 0 AND score <= 1))
);

CREATE INDEX IF NOT EXISTS idx_alerts_ts   ON alerts (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_zone ON alerts (zone);
CREATE INDEX IF NOT EXISTS idx_alerts_type ON alerts (type);
CREATE INDEX IF NOT EXISTS idx_alerts_zone_type_ts ON alerts (zone, type, created_at DESC);

-- ---------------------------------------------------------------------------
-- Agregados diarios
--
-- Los calcula el servicio jobs una vez al dia (ADR-009). Su proposito es doble:
-- alimentar tableros sin recorrer la tabla de eventos completa, y conservar la
-- serie historica despues de que la politica de retencion (ADR-007) borre los
-- eventos individuales.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS daily_event_metrics (
  dia          date   NOT NULL,
  zone         text   NOT NULL,
  event_type   text   NOT NULL,
  eventos      bigint NOT NULL,
  calculado_en timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (dia, zone, event_type)
);

CREATE INDEX IF NOT EXISTS idx_daily_event_metrics_dia ON daily_event_metrics (dia DESC);

CREATE TABLE IF NOT EXISTS daily_alert_metrics (
  dia            date   NOT NULL,
  zone           text   NOT NULL,
  type           text   NOT NULL,
  alertas        bigint NOT NULL,
  score_promedio numeric,
  calculado_en   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (dia, zone, type)
);

CREATE INDEX IF NOT EXISTS idx_daily_alert_metrics_dia ON daily_alert_metrics (dia DESC);
