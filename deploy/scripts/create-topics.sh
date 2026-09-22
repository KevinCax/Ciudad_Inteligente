#!/bin/sh
# Crea los topics del sistema. Lo ejecuta kafka-init una sola vez.
set -e

BROKER="kafka:29092"

echo "Creando topics en $BROKER..."

crear() {
  nombre="$1"; particiones="$2"; retencion_ms="$3"; descripcion="$4"
  echo "  - $nombre ($descripcion)"
  kafka-topics --bootstrap-server "$BROKER" \
    --create --if-not-exists \
    --topic "$nombre" \
    --partitions "$particiones" \
    --replication-factor 1 \
    --config "retention.ms=$retencion_ms"
}

# Eventos canonicos. 3 particiones y clave = zona: los eventos de una misma
# zona caen en la misma particion y conservan su orden relativo, que es lo que
# necesita la correlacion por ventana.
crear "events.standardized" 3 259200000 "eventos canonicos, retencion 72h"

# Alertas correlacionadas. Retencion mas larga porque alimentan el analisis
# historico y los dashboards.
crear "correlated.alerts" 3 604800000 "alertas correlacionadas, retencion 7d"

# Dead-letter queue. Sin ella, todo lo que falla se descarta en silencio y no
# hay forma de saber despues que se perdio. Retencion larga porque su proposito
# es que alguien pueda revisar y reprocesar lo que fallo.
crear "events.dlq" 3 1209600000 "errores de validacion y publicacion, retencion 14d"

echo ""
echo "Topics existentes:"
kafka-topics --bootstrap-server "$BROKER" --list

echo ""
echo "Detalle:"
kafka-topics --bootstrap-server "$BROKER" --describe --topic events.standardized
kafka-topics --bootstrap-server "$BROKER" --describe --topic correlated.alerts
kafka-topics --bootstrap-server "$BROKER" --describe --topic events.dlq

echo ""
echo "Topics listos."
