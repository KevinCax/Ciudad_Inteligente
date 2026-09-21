# Despliegue

Dos capas separadas, que es como se materializa el modelo híbrido.

| Archivo | Capa | Contiene |
|---|---|---|
| `docker-compose.edge.yml` | On-premise (LAN municipal) | Kafka, Redis, ingestor, correlator, persister, indexer, Kafka-UI |
| `docker-compose.dev.yml` | Añadido de desarrollo | PostgreSQL local, en lugar de la base gestionada |
| `docker-compose.cloud.yml` | Nube | Elasticsearch, Kibana, Grafana, Prometheus |

El camino crítico —evento → alerta— vive íntegramente en la capa edge, y **ningún servicio del camino crítico escribe en la nube**. El único que lo hace es el persister, que archiva por lotes de forma asíncrona.

Si la capa nube se cae, la Municipalidad sigue detectando y generando alertas; el persister acumula retraso y lo recupera cuando el servicio vuelve, leyendo desde el último offset confirmado.

## Arranque

```bash
cp deploy/.env.example deploy/.env
```

Edita `deploy/.env`. Como mínimo, genera la llave de tokenización:

```bash
openssl rand -hex 32
```

Levanta la capa edge con la base local:

```bash
docker compose -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml --env-file deploy/.env up -d --build
```

Y la capa nube:

```bash
docker compose -f deploy/docker-compose.cloud.yml --env-file deploy/.env up -d
```

La red `ciudad_lan` la crea el compose de edge; el de nube se conecta a ella como externa, así que **edge debe levantarse primero**.

## Servicios

| Servicio | URL | Notas |
|---|---|---|
| Ingestor | http://localhost:8000 | `/events`, `/events/health`, `/events/schema`, `/metrics` |
| Correlator | http://localhost:8080 | `/alerts/active`, `/alerts/db`, `/health`, `/metrics` |
| Persister | http://localhost:8002 | `/health`, `/metrics` |
| Indexer | http://localhost:8003 | `/health`, `/metrics` |
| Kafka | localhost:9092 | Desde el host; dentro de Docker es `kafka:29092` |
| Kafka-UI | http://localhost:8081 | Inspección de topics y mensajes |
| PostgreSQL | localhost:5432 | Solo con `docker-compose.dev.yml` |
| Grafana | http://localhost:3000 | Datasources provisionados automáticamente |
| Prometheus | http://localhost:9090 | Recoge métricas de los tres servicios |
| Elasticsearch | http://localhost:9200 | |
| Kibana | http://localhost:5601 | |

## Kafka en modo KRaft

Kafka corre **sin Zookeeper**. Es un componente menos que operar y monitorear, lo que pesa cuando el equipo de sistemas del cliente es pequeño; además Zookeeper quedó deprecado en Kafka 3.x y eliminado en 4.0, así que mantenerlo sería heredar deuda desde el primer día.

Dos listeners: `INTERNAL` (`kafka:29092`) para los servicios dentro de Docker, y `EXTERNAL` (`localhost:9092`) para los simuladores y herramientas que corren en el host.

## Topics

Los crea `kafka-init` al arrancar, y termina.

| Topic | Particiones | Retención | Propósito |
|---|---|---|---|
| `events.standardized` | 3 | 72 h | Eventos canónicos |
| `correlated.alerts` | 3 | 7 d | Alertas correlacionadas |
| `events.dlq` | 3 | 14 d | Errores de validación y publicación |

`events.dlq` no existía en el sistema Java: todo lo que fallaba se descartaba en silencio. Su retención es la más larga porque su propósito es justamente que alguien pueda revisar y reprocesar lo que falló.

## Prueba end-to-end

Comprobar que todo responde:

```bash
curl -s http://localhost:8000/events/health | jq
```

```bash
curl -s http://localhost:8080/health | jq
```

```bash
curl -s http://localhost:8002/health | jq
```

```bash
curl -s http://localhost:8003/health | jq
```

Los cuatro deben devolver `status: "UP"` con sus dependencias en `available`. El indexer necesita Elasticsearch, que esta en la capa nube: hay que levantarla antes con `./make.sh up-cloud`. Fíjate en que el health del ingestor **no menciona PostgreSQL**: ya no depende de la base.

Generar una alerta de posible robo — botón de pánico seguido de un vehículo a alta velocidad en la misma zona:

```bash
curl -s -X POST http://localhost:8000/events -H 'Content-Type: application/json' -d '{"event_version":"1.0","event_type":"panic.button","event_id":"'$(uuidgen)'","producer":"prueba-e2e","source":"simulated","geo":{"zone":"zona_10","lat":14.599,"lon":-90.513},"severity":"critical","payload":{"tipo_de_alerta":"panico","identificador_dispositivo":"BTN-001"}}' | jq
```

```bash
curl -s -X POST http://localhost:8000/events -H 'Content-Type: application/json' -d '{"event_version":"1.0","event_type":"sensor.lpr","event_id":"'$(uuidgen)'","producer":"prueba-e2e","source":"simulated","geo":{"zone":"zona_10","lat":14.599,"lon":-90.513},"severity":"warning","payload":{"placa_vehicular":"P123ABC","velocidad_estimada":95}}' | jq
```

Ambos deben responder `202`. Ninguno lleva `timestamp`, `trace_id`, `correlation_id` ni `partition_key`: el ingestor los completa.

Verificar la alerta:

```bash
curl -s "http://localhost:8080/alerts/active?zone=zona_10" | jq
```

Debe aparecer una alerta `possible_robbery` con `score` **0.85** — no `1`, que es lo que guardaba el sistema Java al truncar el score a entero.

Confirmar que la placa se tokenizó al persistir:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -c "SELECT event_type, payload->>'placa_vehicular' AS placa FROM events WHERE event_type='sensor.lpr' ORDER BY ts_utc DESC LIMIT 1;"
```

Debe mostrar un seudónimo `plate_...`, no `P123ABC`. En Kafka, en cambio, la placa viaja en claro porque el broker está en la LAN: se puede comprobar en Kafka-UI, en el topic `events.standardized`.

El archivado es asíncrono, así que puede tardar unos segundos (hasta `BATCH_TIMEOUT`) en aparecer. Ese retraso se mide en `persister_retraso_archivo_seconds`.

### Comprobar que la detección no depende del enlace

Detén el persister y sigue enviando eventos:

```bash
docker compose -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml stop persister
```

```bash
./make.sh load alertas
```

Las alertas se siguen generando y aparecen en `/alerts/active`. Al volver a levantar el persister, este recupera todo el retraso sin perder un solo evento.

Comprobar la busqueda en Elasticsearch:

```bash
curl -s "http://localhost:9200/events-*/_count" | jq
```

```bash
curl -s "http://localhost:9200/events-*/_search?q=event_type:sensor.lpr&size=1" | jq '.hits.hits[0]._source'
```

La placa debe aparecer tokenizada, igual que en PostgreSQL, y `geo` debe ser un objeto `{lat, lon}` apto para mapas.

Medir el SLO:

```bash
curl -s http://localhost:8080/metrics | grep correlation_latency_seconds_bucket
```

## Apagado

```bash
docker compose -f deploy/docker-compose.cloud.yml down
```

```bash
docker compose -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml down
```

Añadir `-v` elimina también los volúmenes: borra los datos de Kafka, Redis y PostgreSQL.

## Notas

El esquema de `sql/001_schema.sql` se aplica **solo la primera vez** que se inicializa el volumen de PostgreSQL. Para reaplicarlo hay que borrar el volumen con `down -v`.

Las imágenes de los microservicios son `distroless`: no traen shell ni `curl`, de modo que el `healthcheck` a nivel de compose está deshabilitado a propósito. El estado se consulta por sus endpoints `/health`.
