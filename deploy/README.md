# Despliegue

Dos capas separadas, que es como se materializa el modelo híbrido.

| Archivo | Capa | Contiene |
|---|---|---|
| `docker-compose.edge.yml` | On-premise (LAN municipal) | Kafka, Redis, ingestor, correlator, persister, indexer, jobs, Kafka-UI |
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
| Correlator | http://localhost:8001 | `/alerts/active`, `/alerts/db`, `/health`, `/metrics` |
| Persister | http://localhost:8002 | `/health`, `/metrics` |
| Indexer | http://localhost:8003 | `/health`, `/metrics` |
| Jobs | http://localhost:8004 | `/health`, `/metrics`, `/tareas` |
| Kafka | localhost:9092 | Desde el host; dentro de Docker es `kafka:29092` |
| Kafka-UI | http://localhost:8081 | Inspección de topics y mensajes |
| PostgreSQL | localhost:5432 | Solo con `docker-compose.dev.yml` |
| Grafana | http://localhost:3000 | Datasources **y tableros** provisionados automáticamente |
| Prometheus | http://localhost:9090 | Recoge métricas de los tres servicios |
| Elasticsearch | http://localhost:9200 | |
| Kibana | http://localhost:5601 | |

## Recorridos por calle (OSRM)

El mapa de trazabilidad dibuja el recorrido de un incidente siguiendo las
calles en lugar de unir los puntos con rectas. Lo calcula un motor de rutas que
vive en la capa de borde, por el mismo motivo que el correlator: el sistema no
debe depender del enlace a internet, y pedir rutas a un servicio externo
significaria ademas enviarle las coordenadas de cada incidente.

Antes de levantarlo hay que preparar el mapa **una vez**:

```bash
./make.sh rutas-preparar
```

Descarga el extracto de Guatemala de OpenStreetMap (unos 131 MB) y construye
los indices. Tarda varios minutos y es idempotente: si ya estan, no hace nada.
Los datos quedan en `deploy/osrm-data`, que esta en `.gitignore`.

No forma parte de `up` a proposito: meterlo en el arranque haria que levantar el
sistema para una demostracion dependiera de poder descargar 131 MB.

Despues, el servicio arranca con el resto. El recorrido lo calcula `jobs` cada
minuto para las alertas recientes y lo guarda en `alert_routes`.

**Importante al explicarlo:** el sistema NO sabe por donde circulo el vehiculo.
Sabe donde lo vieron las camaras. La linea es el camino mas probable entre esos
puntos, calculado por el motor de rutas; los puntos son lo medido. Ver
[ADR-012](../docs/adr/012-recorridos-por-calle.md).

## Esquema de la base

El esquema vive en `deploy/sql/`, numerado: `001_schema.sql` crea las tablas de
eventos, alertas y agregados; `002_trazabilidad.sql` añade `notifications` y
los índices del rastreo por vehículo.

En desarrollo los aplica el contenedor `postgres-migrate`, que corre en cada
`up`, ejecuta todos los archivos en orden y termina. Los servicios que escriben
en la base esperan a que haya terminado, no solo a que PostgreSQL acepte
conexiones: que la base responda no significa que las tablas existan.

Hace falta porque `docker-entrypoint-initdb.d` **solo se ejecuta la primera vez
que se inicializa el volumen**. Sin este contenedor, añadir una tabla obligaría
a borrar el volumen y perder los datos, que es exactamente lo que no se puede
hacer a mitad de una demostración. Todos los scripts llevan `IF NOT EXISTS`, de
modo que reaplicarlos es inocuo.

Si una migración falla, el arranque se detiene (`ON_ERROR_STOP=1`): vale más
eso que dejar el esquema a medio aplicar y descubrirlo después como una columna
que no existe.

**En producción la base es gestionada y no está en ningún compose**, así que no
hay quien ejecute los scripts. Hay que aplicarlos a mano al desplegar una
versión que cambie el esquema:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f deploy/sql/001_schema.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f deploy/sql/002_trazabilidad.sql
```

Son idempotentes: ejecutarlos sobre una base ya poblada no cambia nada.

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

`events.dlq` es la red de seguridad: sin ella, todo lo que falla se descarta en silencio y no queda forma de saber después qué se perdió. Su retención es la más larga porque su propósito es justamente que alguien pueda revisar y reprocesar lo que falló.

## Prueba end-to-end

Comprobar que todo responde:

```bash
curl -s http://localhost:8000/events/health | python -m json.tool
```

```bash
curl -s http://localhost:8001/health | python -m json.tool
```

```bash
curl -s http://localhost:8002/health | python -m json.tool
```

```bash
curl -s http://localhost:8003/health | python -m json.tool
```

Los cuatro deben devolver `status: "UP"` con sus dependencias en `available`. El indexer necesita Elasticsearch, que esta en la capa nube: hay que levantarla antes con `./make.sh up-cloud`. Fíjate en que el health del ingestor **no menciona PostgreSQL**: ya no depende de la base.

Generar una alerta de posible robo — botón de pánico seguido de un vehículo a alta velocidad en la misma zona:

```bash
curl -s -X POST http://localhost:8000/events -H 'Content-Type: application/json' -d '{"event_version":"1.0","event_type":"panic.button","event_id":"'$(uuidgen)'","producer":"prueba-e2e","source":"simulated","geo":{"zone":"zona_10","lat":14.599,"lon":-90.513},"severity":"critical","payload":{"tipo_de_alerta":"panico","identificador_dispositivo":"BTN-001"}}' | python -m json.tool
```

```bash
curl -s -X POST http://localhost:8000/events -H 'Content-Type: application/json' -d '{"event_version":"1.0","event_type":"sensor.lpr","event_id":"'$(uuidgen)'","producer":"prueba-e2e","source":"simulated","geo":{"zone":"zona_10","lat":14.599,"lon":-90.513},"severity":"warning","payload":{"placa_vehicular":"P123ABC","velocidad_estimada":95}}' | python -m json.tool
```

Ambos deben responder `202`. Ninguno lleva `timestamp`, `trace_id`, `correlation_id` ni `partition_key`: el ingestor los completa.

Verificar la alerta:

```bash
curl -s "http://localhost:8001/alerts/active?zone=zona_10" | python -m json.tool
```

Debe aparecer una alerta `possible_robbery` con `score` **0.85** — un número real, no un entero: la gradación de confianza entre 0.85, 0.90 y 0.95 es lo que distingue una regla de otra.

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
curl -s "http://localhost:9200/events-*/_count" | python -m json.tool
```

```bash
curl -s "http://localhost:9200/events-*/_search?q=event_type:sensor.lpr&size=1" | python -m json.tool '.hits.hits[0]._source'
```

La placa debe aparecer tokenizada, igual que en PostgreSQL, y `geo` debe ser un objeto `{lat, lon}` apto para mapas.

Comprobar las tareas periodicas:

```bash
curl -s http://localhost:8004/tareas | python -m json.tool
```

Lanzar los agregados de hoy a mano, sin esperar a las 03:00:

```bash
curl -s -X POST "http://localhost:8004/tareas/agregados_diarios/ejecutar?dia=$(date -u +%F)" | python -m json.tool
```

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -c "SELECT zone, type, alertas, round(score_promedio,2) FROM daily_alert_metrics WHERE dia = current_date ORDER BY alertas DESC LIMIT 5;"
```

Los agregados deben cuadrar con los datos crudos, y repetir la ejecucion no debe duplicar filas.

Medir el SLO:

```bash
curl -s http://localhost:8001/metrics | grep correlation_latency_seconds_bucket
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

## Tableros de Grafana

Los tres tableros se provisionan solos al arrancar: no hay que importar nada a
mano. Aparecen en la carpeta **Ciudad Inteligente**.

| Tablero | Origen | Para qué sirve |
|---|---|---|
| Operación en tiempo real | Prometheus | Es el tablero de la demostración: ritmo de ingesta por tipo de sensor, alertas por tipo, rechazos, ventanas de correlación y estado de los cinco servicios |
| Incidentes y mapa de calor | PostgreSQL | Mapa de calor georreferenciado, lista de incidentes y alertas críticas. Cubre los tres casos de visualización del enunciado |
| SLO y presupuesto de error | Prometheus | Cumplimiento a 30 días, presupuesto consumido y percentiles de latencia con el umbral de 2 s marcado |

Los orígenes de datos llevan **uid explícito** (`prometheus-ciudad`,
`postgres-ciudad`, `elasticsearch-ciudad`). Sin él, Grafana generaría uno al
azar en cada instalación y los tableros llegarían vacíos a la demostración.

El JSON de los tableros se genera desde `deploy/grafana/dashboards.py`, no se
edita a mano: un tablero mal formado no falla al arrancar, simplemente no
aparece.

### Si un panel sale vacío

| Síntoma | Causa probable |
|---|---|
| Todos los paneles de un tablero, vacíos | El origen de datos no conecta. Revisar `deploy/.env` |
| Solo los de PostgreSQL | La base está en la nube; comprobar `GRAFANA_PG_HOST` |
| El mapa sin puntos | No hay eventos con coordenadas en el rango de tiempo elegido |
| El SLO en blanco | Aún no se ha generado ninguna alerta: lanzar `./make.sh load alertas` |
