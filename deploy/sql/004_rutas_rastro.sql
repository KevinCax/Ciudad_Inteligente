-- El recorrido cubre el rastro completo, no solo las evidencias de la alerta.
--
-- Idempotente como los anteriores: lo aplica postgres-migrate en cada arranque.
--
-- La alerta se emite en cuanto el patron se completa, y el vehiculo sigue
-- circulando despues. Un recorrido calculado una sola vez, al nacer la alerta,
-- se quedaria corto: terminaria en la ultima evidencia y no en el ultimo
-- contacto. Estas columnas permiten saber CON QUE se calculo, y por tanto
-- cuando hay que recalcularlo.

-- Cuantos puntos (eventos con coordenadas) recorre la ruta. La tarea compara
-- este numero con el rastro actual: si el rastro tiene mas, la ruta esta
-- desactualizada y se rehace.
ALTER TABLE alert_routes ADD COLUMN IF NOT EXISTS puntos int NOT NULL DEFAULT 0;

-- Instante del primer y del ultimo punto. Permiten contrastar la duracion que
-- estima el motor de rutas con el tiempo realmente transcurrido, que es la
-- comprobacion de que el trayecto cabia en ese tiempo.
ALTER TABLE alert_routes ADD COLUMN IF NOT EXISTS desde timestamptz;
ALTER TABLE alert_routes ADD COLUMN IF NOT EXISTS hasta timestamptz;
