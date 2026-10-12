-- Trazabilidad: a quien se aviso de cada alerta, y rastreo por vehiculo.
--
-- Este archivo se aplica despues de 001_schema.sql. Ambos son idempotentes
-- (todo lleva IF NOT EXISTS), de modo que volver a ejecutarlos sobre una base
-- ya poblada no cambia nada. Esa propiedad es la que permite aplicarlos en
-- cada arranque en lugar de solo con el volumen vacio.

-- ---------------------------------------------------------------------------
-- notifications: un registro por organismo avisado de una alerta
-- ---------------------------------------------------------------------------
--
-- Es una tabla aparte y no un campo de alerts por tres razones. El contrato de
-- la alerta tiene additionalProperties:false y el mapeo de Elasticsearch es
-- strict, asi que un campo nuevo rompe ambos. Una alerta avisa a varios
-- organismos, de modo que la relacion es uno a muchos. Y el aviso tiene ciclo
-- de vida propio —se crea, se envia, falla, se reintenta— mientras que la
-- alerta es un hecho inmutable.
CREATE TABLE IF NOT EXISTS notifications (
  notification_id uuid PRIMARY KEY,
  alert_id        uuid NOT NULL,
  alert_type      text NOT NULL,
  zone            text,
  organismo       text NOT NULL,
  canal           text NOT NULL,
  estado          text NOT NULL,
  intentos        int  NOT NULL DEFAULT 0,
  creado_en       timestamptz NOT NULL DEFAULT now(),
  enviado_en      timestamptz,
  detalle         text,

  -- La idempotencia del notificador descansa aqui, no en el codigo. Kafka
  -- reentrega tras un rebalanceo o un reinicio, y sin esta restriccion la
  -- misma alerta generaria avisos duplicados a bomberos cada vez. Con ella,
  -- un ON CONFLICT DO NOTHING convierte la reentrega en un no-op.
  CONSTRAINT notifications_alerta_organismo UNIQUE (alert_id, organismo),

  CONSTRAINT notifications_estado_valido
    CHECK (estado IN ('pendiente', 'entregado', 'fallido'))
);

CREATE INDEX IF NOT EXISTS idx_notifications_alert     ON notifications (alert_id);
CREATE INDEX IF NOT EXISTS idx_notifications_organismo ON notifications (organismo);
CREATE INDEX IF NOT EXISTS idx_notifications_creado    ON notifications (creado_en DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_estado    ON notifications (estado);

-- ---------------------------------------------------------------------------
-- Rastreo por vehiculo
-- ---------------------------------------------------------------------------
--
-- El seudonimo de la placa vive dentro del jsonb del payload, de modo que
-- seguir a un vehiculo obliga a recorrer la tabla entera salvo que exista un
-- indice sobre la expresion. Es parcial porque solo los eventos de lectura de
-- placa tienen el campo: los demas no deben ocupar espacio en el indice.
--
-- El rastreo alcanza RETENCION_DIAS_PLACA dias (90 por defecto): pasado ese
-- plazo el job de retencion elimina placa_vehicular del payload y el vehiculo
-- deja de ser rastreable. Es intencionado (ADR-007), no una limitacion.
CREATE INDEX IF NOT EXISTS idx_events_placa
  ON events ((payload->>'placa_vehicular'))
  WHERE payload ? 'placa_vehicular';

-- Las cadenas de eventos se reconstruyen desde alerts.evidence, que es un
-- array jsonb de event_id. Sin indice GIN, cruzar una alerta con sus
-- evidencias obliga a un recorrido completo de alerts por cada consulta.
CREATE INDEX IF NOT EXISTS idx_alerts_evidence
  ON alerts USING gin (evidence);
