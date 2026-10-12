# Guía de demostración

Paso a paso para levantar el sistema y probarlo en vivo. Cada prueba dice **qué
ejecutar**, **qué debe salir** y **qué demuestra**, para poder responder si
alguien pregunta "¿y eso qué prueba?".

> **Terminal:** Git Bash, desde la raíz del proyecto.
> No hace falta `jq`: se usa `python -m json.tool`, que viene con Python.
>
> **Al copiar órdenes, cuidado con el `$`.** Algunas llevan `$(...)` dentro, y
> si al pegar se pierde el `$` inicial, bash responde
> `syntax error near unexpected token` sin explicar nada. Conviene copiar con
> el botón de copiar del bloque, no seleccionando con el ratón.

---

## Paso 0 · Preparar la sesión

Abrir **Docker Desktop** y esperar a que arranque. Después, en Git Bash:

```bash
cd "/c/Users/caxke/Escritorio/Proyectos U/ProyectoArquitecturaII/Ciudad_Inteligente_GO"
```

Cargar las credenciales en la sesión:

```bash
source deploy/credenciales.sh
```

Debe responder algo así:

```
credenciales cargadas:
  KEY          a9ab6d08...
  TOKEN        dfa1bc4c...
  CORRELATOR   http://localhost:8001
```

A partir de aquí, `$KEY`, `$TOKEN` y `$CORRELATOR` están disponibles en esa
terminal. **Si abre una pestaña nueva, hay que volver a cargarlas.**

> **La palabra `source` es obligatoria.** Ejecutarlo como
> `./deploy/credenciales.sh` no sirve: las variables se crean en un proceso
> aparte y se pierden al terminar. El script lo detecta y avisa.

> Si alguna sale **SIN VALOR**, faltan las credenciales en `deploy/.env`. El
> propio script indica cómo generarlas.

---

## Paso 1 · Levantar el sistema

```bash
./make.sh up
```

```bash
./make.sh up-cloud
```

El primero levanta la **capa local**: Kafka, Redis, PostgreSQL, los cinco
microservicios, Prometheus y Grafana. El segundo, la **capa de nube**:
Elasticsearch y Kibana.

Comprobar que todo quedó listo:

```bash
./make.sh estado
```

Debe decir **listo** en todo:

```
Servicios
  ingestor     listo
  correlator   listo
  persister    listo
  indexer      listo
  jobs         listo

Infraestructura
  elasticsearch  listo
  prometheus     listo
  grafana        listo
  kibana         listo

Lo que ve Prometheus
  6 de 6 objetivos activos
```

> **El indexer tarda más que los demás, y es normal.** Necesita Elasticsearch,
> que tarda cerca de minuto y medio en estar listo; hasta entonces el indexer
> se cae y Docker lo reintenta. Si sale `arrancando`, esperar 30 segundos y
> repetir. Solo si sigue así pasados tres minutos hay un problema de verdad, y
> entonces: `docker logs ciudad-indexer | tail -20`.
>
> En el tablero de Grafana esto se ve como el indexer en rojo durante el primer
> minuto. **No es un fallo: es que todavía no ha terminado de arrancar.**

**Qué demuestra:** que el despliegue está separado en dos capas, que es el
modelo híbrido del ADR-001. Se puede señalar que la capa de nube se puede
apagar entera y el sistema sigue detectando incidentes (se prueba en el Paso 8).

---

## Paso 2 · Comprobar que cada servicio responde

```bash
printf "ingestor   -> "; curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8000/events/health
for p in 8001 8002 8003 8004; do printf "puerto %s -> " $p; curl -s -o /dev/null -w "%{http_code}\n" http://localhost:$p/health; done
```

Los cinco deben responder **200**.

| Puerto | Servicio | Ruta de salud |
|---|---|---|
| 8000 | ingestor | `/events/health` |
| **8001** | **correlator** (dentro de Docker escucha en 8080) | `/health` |
| 8002 | persister | `/health` |
| 8003 | indexer | `/health` |
| 8004 | jobs | `/health` |

> El ingestor cuelga todo de `/events`, de ahí que su salud esté en
> `/events/health` y no en `/health`. Pedir `/health` al 8000 devuelve `404`.

> El correlator se publica en 8001 porque 8080 suele estar ocupado. Se cambia
> con `CORRELATOR_HOST_PORT` en `deploy/.env`.

---

## Paso 3 · La seguridad está activa

Primero **sin credenciales**, que debe fallar:

```bash
curl -s -o /dev/null -w "ingestor sin clave:   %{http_code}\n" -X POST http://localhost:8000/events -H "Content-Type: application/json" -d '{}'
```

```bash
curl -s -o /dev/null -w "correlator sin token: %{http_code}\n" "http://localhost:8001/alerts/active?zone=zona_1"
```

Ambos deben dar **401**.

Y `/metrics` debe seguir **abierto**, porque Prometheus lo consulta desde
dentro de la LAN:

```bash
curl -s -o /dev/null -w "metrics:              %{http_code}\n" http://localhost:8001/metrics
```

**Qué demuestra:** las alertas revelan dónde y cuándo ocurren incidentes; son
información operativa de seguridad, no un dato público.

---

## Paso 4 · Enviar un evento a mano

```bash
curl -s -X POST http://localhost:8000/events \
  -H "Content-Type: application/json" -H "X-API-Key: $KEY" \
  -d "{\"event_version\":\"1.0\",\"event_type\":\"panic.button\",\"event_id\":\"$(python -c 'import uuid;print(uuid.uuid4())')\",\"producer\":\"demo\",\"source\":\"simulated\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"partition_key\":\"zona_1\",\"geo\":{\"zone\":\"zona_1\",\"lat\":14.6407,\"lon\":-90.5133},\"severity\":\"critical\",\"payload\":{\"tipo_de_alerta\":\"panico\"}}" \
  | python -m json.tool
```

Debe responder **`202`** con el `event_id` y el `trace_id`.

### Y que rechaza lo que debe rechazar

Un campo que el contrato no declara:

```bash
curl -s -X POST http://localhost:8000/events \
  -H "Content-Type: application/json" -H "X-API-Key: $KEY" \
  -d "{\"event_version\":\"1.0\",\"event_type\":\"panic.button\",\"event_id\":\"$(python -c 'import uuid;print(uuid.uuid4())')\",\"producer\":\"demo\",\"source\":\"simulated\",\"timestamp\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"partition_key\":\"zona_1\",\"geo\":{\"zone\":\"zona_1\"},\"severity\":\"info\",\"payload\":{},\"campo_inventado\":\"x\"}" \
  | python -m json.tool
```

Debe dar **`400`** nombrando el campo sobrante.

**Qué demuestra:** el ingestor valida el **JSON crudo**, antes de deserializar.
Validando el objeto ya deserializado, el campo desconocido se descartaría en
silencio y `additionalProperties: false` no detectaría nada.

---

## Paso 5 · Las cuatro alertas

```bash
./make.sh load alertas
```

Después, consultar cada zona:

```bash
for z in zona_1 zona_10 zona_12 zona_18; do echo "--- $z"; curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:8001/alerts/active?zone=$z" | python -m json.tool; done
```

| Zona | Alerta esperada | Confianza |
|---|---|---|
| zona_1 | `possible_robbery` | 0.85 |
| zona_10 | `accident` | 0.85 |
| zona_12 | `fire` | 0.95 |
| zona_18 | `traffic_speed_violation` | 0.90 |

**Qué demuestra:** el motor de correlación funciona para los cuatro patrones.
La confianza es un número real, no un entero: si se redondeara, 0.85, 0.90 y
0.95 quedarían las tres como 1 y la gradación entre reglas se perdería.

> Si una zona sale vacía, probablemente la alerta se **suprimió** porque ya
> había una equivalente vigente. Esperar 5 minutos (`ALERT_SUPPRESS_TTL`) o
> usar otra zona.

---

## Paso 6 · El accidente con caída de velocidad

Esta es la prueba que conviene enseñar despacio, porque distingue las dos ramas
de la regla.

```bash
Z=zona_16
T0=$(python -c "import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(seconds=40)).strftime('%Y-%m-%dT%H:%M:%SZ'))")
T1=$(python -c "import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(seconds=30)).strftime('%Y-%m-%dT%H:%M:%SZ'))")
T2=$(python -c "import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(seconds=20)).strftime('%Y-%m-%dT%H:%M:%SZ'))")
T3=$(python -c "import datetime as d;print((d.datetime.now(d.timezone.utc)-d.timedelta(seconds=10)).strftime('%Y-%m-%dT%H:%M:%SZ'))")
ev () { curl -s -o /dev/null -w "%{http_code} " -X POST http://localhost:8000/events -H "Content-Type: application/json" -H "X-API-Key: $KEY" -d "{\"event_version\":\"1.0\",\"event_type\":\"$1\",\"event_id\":\"$(python -c 'import uuid;print(uuid.uuid4())')\",\"producer\":\"demo\",\"source\":\"simulated\",\"timestamp\":\"$3\",\"partition_key\":\"$Z\",\"geo\":{\"zone\":\"$Z\",\"lat\":14.61,\"lon\":-90.51},\"severity\":\"critical\",\"payload\":$2}"; }
ev sensor.lpr      '{"placa_vehicular":"P777BBB","velocidad_estimada":90}' "$T0"
ev sensor.lpr      '{"placa_vehicular":"P777BBB","velocidad_estimada":25}' "$T1"
ev citizen.report  '{"tipo_evento":"accidente","mensaje_descriptivo":"vehiculo volcado","origen":"app"}' "$T2"
ev sensor.acoustic '{"tipo_sonido_detectado":"vidrio_roto","nivel_decibeles":110}' "$T3"
echo ""
```

Cuatro `202`. Esperar unos segundos y consultar:

```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:8001/alerts/active?zone=zona_16" | python -m json.tool
```

Debe salir **`accident` con `score` 0.95 y cuatro evidencias**.

| Caso | Confianza | Evidencias |
|---|---|---|
| Reporte + sonido, sin caída | 0.85 | 2 |
| **Con caída de 90 a 25 km/h** | **0.95** | **4** |

**Qué demuestra:** la caída de velocidad no decide si hay alerta, refuerza la
confianza. Un accidente sigue siendo detectable aunque ningún sensor de
velocidad lo haya registrado.

> ⚠️ **Las marcas de tiempo tienen que ser crecientes.** Si los cuatro eventos
> llevan la misma hora, el motor no puede ver una transición de rápido a lento
> y la alerta sale con 0.85. No es un fallo del sistema: una caída de velocidad
> es, por definición, una secuencia.

---

## Paso 7 · La tabla 2.4 del enunciado, los dos renglones

```bash
./make.sh load enunciado
```

Tarda unos dos minutos: hay que esperar a que cierre la ventana de correlación.

```
Renglon 1 - zona_4:  panico + placa XYZ123 a 85 km/h  -> alerta emitida  [OK]
Renglon 2 - zona_10: boton de panico sin complementos -> ninguna alerta  [OK]
                                                      -> falso positivo confirmado  [OK]
```

**Qué demuestra:** reproduce literalmente el ejemplo del enunciado, incluido el
segundo renglón, que casi nunca se prueba. Lo difícil no es disparar la alerta:
es **no dispararla y aun así dejar constancia**. Sin ese registro, una zona
tranquila y una zona llena de señales incompletas son indistinguibles.

---

## Paso 8 · Resiliencia: apagar un componente

Es la prueba más vistosa de todas. Conviene definir un atajo primero:

```bash
dc () { docker compose -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml --env-file deploy/.env "$@"; }
```

**1.** Contar lo que hay archivado:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -tAc "SELECT count(*) FROM events;"
```

**2.** Apagar el servicio que archiva:

```bash
dc stop persister
```

**3.** Con él apagado, mandar tráfico:

```bash
./make.sh load rafaga
```

Debe reportar **todas las respuestas en `202`**: la ingesta y la detección
siguen funcionando con el archivo caído.

**4.** Contar otra vez — el número **no ha subido**, porque nadie está
archivando:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -tAc "SELECT count(*) FROM events;"
```

**5.** Levantarlo y esperar:

```bash
dc start persister && sleep 25
```

**6.** Contar de nuevo — ahora **sí subió**, y cuadra con todo lo que se envió:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -tAc "SELECT count(*) FROM events;"
```

**Qué demuestra:** es la propiedad central del modelo híbrido. Un fallo en la
capa de archivo no detiene la detección de incidentes; el archivo se pone al
día solo, leyendo desde su último offset confirmado.

---

## Paso 9 · Integridad: nada se pierde

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -tAc "SELECT 'eventos en PostgreSQL: '||count(*) FROM events; SELECT 'alertas en PostgreSQL: '||count(*) FROM alerts;"
```

```bash
curl -s "http://localhost:9200/events-*/_count" | python -m json.tool
curl -s "http://localhost:9200/alerts-*/_count" | python -m json.tool
```

Los recuentos de PostgreSQL y Elasticsearch **deben coincidir**.

**Qué demuestra:** lo que el sistema acepta con un `202`, termina archivado en
los dos destinos. No hay pérdida silenciosa.

---

## Paso 10 · Privacidad: la placa no sale en claro

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -tAc "SELECT payload->>'placa_vehicular' FROM events WHERE payload ? 'placa_vehicular' ORDER BY ts_utc DESC LIMIT 3;"
```

Deben salir seudónimos del tipo `plate_d923aba731ede85a...`, **nunca** una
placa legible.

**Qué demuestra:** la placa se convierte en seudónimo HMAC-SHA256 justo antes
de cruzar hacia la nube. La llave se custodia dentro de la LAN y no se versiona,
de modo que quien obtuviera la base completa no podría recuperar una sola placa.

---

## Paso 11 · Picos de carga

```bash
./make.sh load rafaga
```

Calentamiento, pico de **100 eventos/s durante 10 segundos**, y recuperación.

Debe reportar **todas las respuestas en `202`** y un percentil 99 de pocos
milisegundos.

**Qué demuestra:** cubre el objetivo de la sección 2.3 del enunciado, evaluar
la reacción ante picos. Y sostiene la decisión de no adoptar Kubernetes: una
sola instancia de cada servicio absorbe el doble del volumen esperado.

---

## Paso 11b · Cien incidentes a la vez

```bash
./make.sh load tormenta 100
```

Lanza cien incidentes **en paralelo**, uno por zona, repartidos entre los
cuatro tipos de alerta. El número se puede cambiar: `./make.sh load tormenta 250`.

```
  Resultado
    incidentes lanzados   100
    alertas emitidas      100
    alertas suprimidas    50

    possible_robbery          25 de 25
    accident                  25 de 25
    fire                      25 de 25
    traffic_speed_violation   25 de 25
```

**Qué demuestra:** el motor correlaciona cien incidentes simultáneos sin perder
ninguno, y sigue cumpliendo el SLO — medido durante la tormenta: **100 % bajo
los 2 segundos, media de 697 ms**.

### Las dos cifras miden cosas distintas

Es la pregunta que seguramente le hagan al ver "50 suprimidas":

- **100 emitidas** — una por zona. Ningún incidente se perdió.
- **50 suprimidas** — son **re-disparos dentro del mismo incidente**, no
  incidentes perdidos. Una secuencia de cuatro eventos dispara su regla al
  segundo evento; los dos siguientes vuelven a cumplirla y se suprimen. Para
  eso existe la supresión: que un incidente en curso genere *un* aviso y no una
  cadena de avisos idénticos.

### Por qué zonas distintas

Si se lanzaran los cien incidentes **en la misma zona**, el sistema emitiría
**una** alerta y suprimiría noventa y nueve. Sería correcto, pero no demuestra
capacidad: demuestra supresión. Por eso el escenario reparte una zona por
incidente.

> Cada ejecución usa zonas con un sufijo nuevo. Sin eso, repetir la tormenta
> antes de que expire `ALERT_SUPPRESS_TTL` (5 min) daría **cero alertas
> emitidas**, que parece una avería y es justo lo contrario.

---

## Paso 12 · El SLO, medido bien

El objetivo es que **el 99 % de las alertas se genere en menos de 2 segundos**.

```bash
curl -s http://localhost:8001/metrics | grep correlation_latency_seconds_count
```

Para medirlo de verdad hay que hacerlo **por diferencia**: tomar el valor,
generar tráfico y volver a tomarlo.

> ⚠️ **No leer el acumulado y presentarlo como resultado.** Si se reinició un
> servicio, o si se enviaron eventos con marcas de tiempo del pasado, el
> promedio queda contaminado: la latencia se mide desde la marca del evento.
> Este proyecto tuvo que retractar tres mediciones por eso.

En Grafana, el tablero **SLO y presupuesto de error** ya lo muestra calculado.

---

## Paso 12b · La cadena de un vehículo, y a quién se avisó

Es el paso que responde dos preguntas que el resto de la demostración no
responde: *¿qué evento se correlaciona con cuál?* y *¿a quién avisamos?*

```bash
./make.sh load persecucion
```

Reproduce una secuencia concreta: alguien pulsa el botón de pánico por un robo
en la zona 1; dos segundos después una cámara de semáforo capta una placa
cruzando en rojo en esa misma zona; luego la misma placa aparece a 112 km/h en
la zona 4, y a 118 en la zona 9.

Lo que hay que señalar es **por qué las reglas de zona no pueden ver esto**: en
la ventana de la zona 1 no existe la lectura de la zona 4, y en la de la zona 4
no existe el botón de pánico. Por separado son un robo y una multa. La regla
`vehicle_pursuit` las enlaza por la matrícula.

Comprobar la cadena completa, en orden causal:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -c "SELECT e.ts_utc::time AS hora, e.zone, e.event_type, coalesce(e.payload->>'infraccion','-') AS infraccion, coalesce(e.payload->>'velocidad_estimada','-') AS velocidad, left(coalesce(e.payload->>'placa_vehicular','-'),14) AS vehiculo FROM alerts a CROSS JOIN LATERAL jsonb_array_elements_text(a.evidence) AS ev(id) JOIN events e ON e.event_id = ev.id::uuid WHERE a.type='vehicle_pursuit' ORDER BY e.ts_utc"
```

Y los avisos que esa alerta generó:

```bash
docker exec ciudad-postgres psql -U ciudad -d ciudades -c "SELECT alert_type, organismo, canal, estado, creado_en::time FROM notifications WHERE alert_type='vehicle_pursuit' ORDER BY creado_en DESC LIMIT 10"
```

**Decirlo con claridad si preguntan:** el canal es `simulado`. El sistema
decide a qué organismos corresponde cada alerta y lo registra con su hora y su
resultado, pero **no existe integración con bomberos, policía ni ambulancias:
nadie acude**. La trazabilidad está completa; la entrega, no. Presentarlo de
otra forma es lo único que puede hundir esta parte de la defensa.

Dos detalles que suelen preguntarse:

- **La matrícula que aparece no es la matrícula.** Es un seudónimo HMAC. Pero
  es *estable*, de modo que el mismo vehículo produce siempre el mismo
  identificador y se puede seguir sin que la placa en claro salga de la LAN.
- **El rastreo caduca.** A los 90 días el trabajo de retención elimina el campo
  y el vehículo deja de ser seguible. Es el plazo que acota la vigilancia, no
  una limitación técnica.

---

## Paso 12c · Verificar que lo mostrado es coherente

Despues de cualquier escenario:

```bash
./make.sh verificar
```

Comprueba once cosas y devuelve codigo distinto de cero si alguna falla, de
modo que sirve igual en una demostracion que encadenado en CI.

Lo que mira, y por que cada una:

| Comprobacion | Que detecta |
|---|---|
| Las evidencias existen y caen dentro de la ventana que la alerta declara | Una alerta que se contradice a si misma |
| Las reglas de zona no usan evidencias de otra zona | Correlaciones imposibles por construccion |
| Ningun tramo exige mas de 70 km/h de media entre dos lecturas | Un vehiculo que habria tenido que volar |
| El tope configurado es creible | Que alguien haya relajado el umbral del motor |
| El recorrido por calles cabe en el tiempo transcurrido | Lo mismo, pero contra el callejero real |
| Toda alerta genero aviso, y sin duplicados | Emergencias de las que no se avisa a nadie |
| Las once consultas del tablero devuelven datos | Que el tablero vaya a salir vacio |

Dos detalles que conviene saber al explicarlo:

- **Las consultas van por la API de Grafana**, no directamente a PostgreSQL.
  Es deliberado: ya ocurrio una vez que los datos estaban perfectos y los
  paneles aparecian vacios por una propiedad mal puesta en el origen de datos.
  Una verificacion que solo mirase la base no lo habria detectado.
- **La referencia de velocidad es propia de la verificacion**, no se lee de la
  configuracion del sistema. Al principio si se leia, y eso hacia que un
  correlator mal configurado pasara la prueba sin rechistar: una verificacion
  que toma sus expectativas del sistema que verifica no verifica nada.

---

## Paso 13 · Los tableros

| Herramienta | Dirección | Qué enseñar |
|---|---|---|
| **Grafana** | http://localhost:3000 | Los cuatro tableros, en la carpeta *Ciudad Inteligente* |
| Kafka-UI | http://localhost:8081 | Los topics, y que `events.dlq` está vacío |
| Kibana | http://localhost:5601 | Búsqueda histórica |
| Prometheus | http://localhost:9090 | Las 14 reglas de alerta, en *Alerts* |

### Prometheus: qué mirar

Al abrirlo, la pestaña *Query* dice **"No data queried yet"**. Eso es normal:
Prometheus no consulta nada hasta que se escribe una expresión. No está roto.

Las dos pantallas que **no exigen escribir nada** y lucen bien en una demo:

- **Status → Target health** — los siete objetivos en verde. Demuestra que
  Prometheus está recogiendo métricas de los cinco microservicios.
- **Alerts** — las 14 reglas, cada una con su expresión. Demuestra que el
  sistema avisa sin que nadie mire un tablero, que es lo que el enunciado pide
  "más allá de tener dashboards en Grafana".

Y si quieren ver consultas en vivo, estas devuelven datos (pegar en la caja y
pulsar **Execute**):

| Consulta | Qué enseña |
|---|---|
| `up` | Los seis objetivos; `1` es activo |
| `sum by (type) (correlator_alerts_generated_total)` | Cuántas alertas de cada tipo se han emitido |
| `sum by (event_type) (ingestor_events_ingested_total)` | Eventos recibidos por tipo de sensor |
| `sum(rate(ingestor_events_ingested_total[5m]))` | Ritmo de ingesta, eventos por segundo |
| `correlator_window_events` | Tamaño de la ventana de correlación de cada zona |
| `histogram_quantile(0.99, sum(rate(correlator_correlation_latency_seconds_bucket[5m])) by (le))` | **El SLO**: percentil 99 de la latencia de detección |
| `indexer_documentos_indexados_total` | Documentos enviados a Elasticsearch |

> Una consulta puede salir **vacía sin que nada esté mal**: las métricas con
> etiquetas no existen hasta su primer uso. `correlator_false_positive_candidates_total`
> no devuelve nada hasta que se confirme el primer falso positivo, y los
> contadores se reinician con el servicio. Si sale vacía, generar tráfico con
> `./make.sh load alertas` y repetir.

En Grafana, el orden que mejor se sigue:

1. **Operación en tiempo real** — ingesta por tipo de sensor y alertas por tipo.
2. **Incidentes y mapa de calor** — el mapa sobre la Ciudad de Guatemala y la
   lista de incidentes con su confianza y sus evidencias.
3. **SLO y presupuesto de error** — el cumplimiento y cuánto margen queda.

---

## Si algo falla

| Síntoma | Causa probable | Qué hacer |
|---|---|---|
| `bind: address already in use` | Otro programa ocupa el puerto | Cambiar `CORRELATOR_HOST_PORT` en `deploy/.env` |
| Todos los `curl` dan `401` | Las variables se perdieron, o es otra pestaña | `source deploy/credenciales.sh` |
| `syntax error near unexpected token` | Al pegar se perdió el `$` de `$(...)` | Copiar de nuevo la orden completa |
| `Hay que cargarlo en la sesion` | Se ejecutó sin `source` | `source deploy/credenciales.sh` |
| El indexer sale `arrancando` o rojo en Grafana | Espera a Elasticsearch (~90 s) | Esperar y repetir `./make.sh estado` |
| El indexer sigue caído tras 3 min | Falta la capa de nube | `./make.sh up-cloud` |
| Una zona no genera alerta | Supresión activa | Esperar 5 min o usar otra zona |
| El accidente sale 0.85 y esperaba 0.95 | Marcas de tiempo iguales | Separarlas en el tiempo (Paso 6) |
| Los tableros salen vacíos | Falta tráfico, o el rango de tiempo | Correr un escenario y revisar el selector de tiempo |
| Prometheus dice `No data queried yet` | No se ha escrito ninguna consulta | Es normal: escribir `up` y pulsar *Execute* |
| Una consulta de Prometheus sale vacía | La métrica aún no se ha usado | `./make.sh load alertas` y repetir |
| `docker: command not found` | Docker Desktop apagado | Abrirlo y esperar |

Para empezar de cero:

```bash
./make.sh down && ./make.sh up && ./make.sh up-cloud
```

---

## Guion corto, si hay poco tiempo

```bash
./make.sh up && ./make.sh up-cloud     # 1. levantar
./make.sh load enunciado               # 2. los dos renglones del enunciado
./make.sh load rafaga                  # 3. picos de carga
```

Y abrir Grafana en http://localhost:3000 para cerrar con los tableros.
