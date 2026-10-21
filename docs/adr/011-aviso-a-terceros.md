# ADR-011: Aviso a terceros — servicio propio con registro de despacho

**Estado:** aceptado
**Fecha:** 2026-10-03
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-004](004-broker-de-eventos.md) (el aviso se dispara desde `correlated.alerts`), [ADR-005](005-persistencia.md) (el registro vive en PostgreSQL), [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) (idempotencia ante reentrega), [ADR-008](008-observabilidad-y-slo.md) (canal de aviso: son dos huecos distintos, no el mismo)

---

## 1. Contexto

Hasta ahora el sistema respondía "qué ocurrió" y no respondía "a quién se le
dijo". Una alerta de incendio y una alerta de incendio atendida eran el mismo
registro en la base: `alerts` guarda el hecho detectado y nada más.

Esa carencia estaba identificada. El [ADR-008](008-observabilidad-y-slo.md)
la registra como trabajo pendiente —*"las reglas disparan dentro de Prometheus,
pero no hay Alertmanager configurado"*— y el informe final situaba la
integración con sistemas de despacho de emergencias fuera del alcance. Son dos
huecos distintos: aquel es el aviso al equipo que opera el sistema; este es el
aviso al cuerpo que acude al incidente.

El requisito nuevo es el segundo: dado un incendio detectado en la zona 12,
poder responder a quién se avisó, por qué canal, cuándo y con qué resultado. Y
poder responderlo meses después, ante una reclamación.

### Lo que hace que esto no sea un campo más

La tentación inmediata es añadir un campo `avisados` a la alerta. Tres hechos
lo impiden, y conviene enumerarlos porque son los que determinan la decisión:

1. **El contrato de la alerta está cerrado.**
   `contracts/correlated-alert/v1.0/schema.json` declara
   `additionalProperties: false`. Un campo nuevo obliga a versionar el
   contrato y a resincronizar sus copias embebidas en cuatro servicios.

2. **El mapeo de Elasticsearch es estricto.**
   `plantillas/alerts.json` usa `"dynamic": "strict"`: un campo no declarado no
   se ignora, hace fallar la indexación del documento entero.

3. **La relación es uno a muchos y tiene estado.**
   Un incendio avisa a bomberos y a ambulancia. Cada aviso se crea, se intenta,
   puede fallar y se reintenta. La alerta, en cambio, es un hecho inmutable:
   ocurrió, y no cambia. Meter un ciclo de vida dentro de un hecho obliga a
   reescribir el hecho cada vez que cambia el ciclo.

---

## 2. Opciones consideradas

### Opción A — Una vista derivada, sin componente nuevo

Una tabla de equivalencias (tipo de alerta → organismos) que el tablero cruza
con `alerts`. Cero servicios nuevos, cero latencia añadida, una tarde de
trabajo.

Se descartó por lo que **no** puede responder. Una vista derivada dice a quién
*habría que* avisar; no dice si se avisó. No distingue "no se pudo entregar" de
"no se intentó", que es justamente la distinción que necesita una investigación
posterior. Y si mañana se cambia el criterio de despacho, la vista reescribe el
pasado: las alertas de hace un mes aparecerían con los destinatarios de hoy.

Para una demostración habría bastado. Para un registro, no.

### Opción B — El persister deriva y escribe los avisos

Sin servicio nuevo: al archivar una alerta, el persister escribe también las
filas de aviso. Hay registros reales y el coste de infraestructura es nulo.

Se descartó por responsabilidad y por acoplamiento. El trabajo del persister es
archivar lo que ya ocurrió; decidir a quién se avisa es una decisión operativa,
no un archivo. Mezclarlas significa que un cambio en el criterio de despacho
obliga a desplegar el servicio del que depende el historial completo. Además el
persister acumula en lotes de hasta cien mensajes con dos segundos de espera,
que es lo correcto para archivar y lo contrario de lo que conviene a un aviso.

### Opción C — Servicio `notifier` propio

Un sexto microservicio que consume `correlated.alerts`, aplica una matriz de
despacho y registra cada aviso en una tabla nueva.

Cuesta un servicio más que mantener, desplegar y vigilar.

---

## 3. Decisión

**Opción C: un servicio `notifier` independiente, con su propia tabla.**

### 3.1 El argumento, en una línea

Un aviso no es un dato derivado de la alerta: es un acto con consecuencias
fuera del sistema, y los actos se registran cuando ocurren, no se recalculan
después.

### 3.2 Qué hace

Consume `correlated.alerts` con grupo propio, valida contra el contrato,
consulta la matriz de despacho, entrega por el canal configurado y escribe una
fila por organismo en `notifications`.

| Tipo de alerta | Organismos |
|---|---|
| `fire` | bomberos, ambulancia |
| `accident` | ambulancia, policía, tránsito |
| `possible_robbery` | policía |
| `traffic_speed_violation` | tránsito |
| `vehicle_pursuit` | policía, tránsito |

El criterio es deliberadamente generoso en incendio y accidente: el coste de
movilizar una ambulancia que no hacía falta es muy inferior al de no
movilizarla. En las infracciones de tránsito ocurre lo contrario, y por eso
solo interviene tránsito.

La matriz se puede ajustar por entorno (`DISPATCH_FIRE=bomberos,ambulancia`)
sin recompilar, y el servicio registra al arrancar cuál está aplicando: un
cambio por variable de entorno no deja rastro en el repositorio, y sin ese
registro nadie podría explicar después por qué no se avisó a los bomberos.

### 3.3 La idempotencia vive en la base, no en el código

```sql
CONSTRAINT notifications_alerta_organismo UNIQUE (alert_id, organismo)
```

Kafka reentrega tras un rebalanceo o un reinicio. Comprobar en el código si el
aviso ya existe y después escribirlo deja una ventana de carrera por la que dos
instancias avisarían dos veces del mismo incendio. `ON CONFLICT DO NOTHING`
resuelve la carrera dentro de la base, en una sola operación atómica.

Verificado: rebobinar el grupo de consumo al principio del topic y reprocesar
las 1 789 alertas no añadió ninguna fila, y el servicio contabilizó 1 789
duplicados.

### 3.4 No se consume desde el principio del topic

`KAFKA_CONSUME_FROM_START=false`, al revés que en el persister. La diferencia
es intencionada: un grupo de consumo nuevo que arrancara desde el origen
despacharía avisos de todas las alertas históricas —bomberos a un incendio de
la semana pasada—. La restricción `UNIQUE` lo impediría en la práctica, pero
confiar a ella el no despachar emergencias viejas sería dejar la corrección al
azar del orden de los acontecimientos.

### 3.5 La entrega está simulada, y hay que decirlo

El canal por defecto es `simulado`: registra la fila y escribe en el log. **No
existe integración con bomberos, policía ni ambulancias. Nadie acude.**

Describirlo de otra forma sería presentar como funcionalidad algo que no lo es.
El servicio documenta la decisión de despacho y deja la trazabilidad completa,
que es lo que el requisito pedía; la entrega real exige convenios con cada
cuerpo de respuesta, que no es una decisión de arquitectura.

El punto de extensión queda marcado y es demostrable, no hipotético: la
interfaz `Canal` tiene una segunda implementación, `webhook`, que entrega por
HTTP a una URL configurable. Apuntándola a un receptor de pruebas se recorre el
camino completo, incluido el registro de un fallo de entrega.

### 3.6 La métrica que importa

`notifier_alertas_sin_destinatario_total{tipo}`.

Un tipo de alerta sin fila en la matriz no produce ningún error: el sistema
sigue funcionando y sencillamente no avisa a nadie. Es el fallo más caro
posible en este servicio y el único que no se manifiesta solo. Por eso además
se grita al arrancar, que es el momento en que alguien está mirando, y el
tablero de trazabilidad lleva un recuadro de "alertas sin ningún aviso" que
debería ser siempre cero.

---

## 4. Consecuencias

### Positivas

- Se puede responder, meses después, a quién se avisó de un incidente, cuándo
  y con qué resultado.
- Cambiar el criterio de despacho no reescribe el pasado: los avisos ya
  emitidos conservan el organismo que recibió cada uno.
- Un fallo de entrega queda registrado como tal, distinguible de un aviso que
  nunca se intentó.
- El contrato de la alerta no se toca, de modo que los consumidores existentes
  no se enteran del cambio.

### Negativas aceptadas

- **Un servicio más.** Seis en lugar de cinco: una imagen más que construir, un
  objetivo más que vigilar en Prometheus, un proceso más que puede caerse.
- **La entrega es «al menos una vez» y el registro no la protege.** El servicio
  entrega por el canal y después escribe la fila. Si la escritura falla por una causa
  transitoria (por ejemplo, la base no responde durante un corte del enlace), no se
  confirma el offset y la alerta se reprocesa: se vuelve a entregar, y el `UNIQUE`
  solo evita la fila duplicada, no el aviso duplicado. Con el canal simulado es
  inocuo. Con un canal real, un cuerpo de respuesta podría recibir el mismo aviso
  varias veces. La corrección es registrar el aviso como `pendiente` antes de
  entregar y actualizarlo después (el patrón outbox), y debe hacerse antes de
  conectar un canal real.
- **La entrega no es real.** El sistema sabe a quién habría que avisar y lo
  registra; no hay nadie al otro lado. Es la limitación principal y no se
  disimula en ningún tablero.
- **No hay reintento automático.** Un aviso fallido queda en estado `fallido` y
  ahí se queda. Reintentarlo exige una política —cuántas veces, con qué
  espera, hasta cuándo— que depende de cada cuerpo de respuesta y que no tiene
  sentido inventar antes de que exista el convenio.
- **No hay acuse de recibo.** La tabla no tiene columna para él, porque ningún
  organismo responde todavía. Sí existen `intentos` y `enviado_en`.

---

## 5. Impacto en el negocio

### Qué se decidió

Que cada aviso a un cuerpo de respuesta quede registrado como un hecho propio,
con su destinatario, su canal, su hora y su resultado.

### Por qué

Porque un sistema que detecta incidentes pero no puede demostrar que avisó es
indefendible ante una reclamación. Si un vecino pregunta por qué los bomberos
no llegaron, la Municipalidad necesita poder responder si el sistema avisó, a
qué hora, y si la entrega falló. Sin este registro, la respuesta sería "el
sistema detectó el incendio", que no es la pregunta.

### Qué se pierde, dicho con claridad

El sistema **no llama a los bomberos**. Decide a quién habría que llamar, lo
anota y lo muestra. Conectar esa decisión con el teléfono de una central de
emergencias requiere un convenio con cada cuerpo, un protocolo acordado y una
certificación de disponibilidad que no se improvisan.

### Costo

Cero costo adicional de licencias o servicios. El servicio comparte la base de
datos y el broker que ya existen; su consumo es despreciable porque las alertas
son órdenes de magnitud menos numerosas que los eventos.

### Si el sistema crece

La tabla `notifications` se purga a los dos años, con el evento que la originó
([ADR-007](007-privacidad-y-datos-personales.md) §4.3), y crece con las alertas, no con los eventos: a razón de
dos o tres filas por alerta, su volumen es irrelevante frente a `events`.
Cuando exista integración real, el reintento tiene ya su columna (`intentos`) y falta el bucle; el acuse
necesitará una columna nueva.

---

## Referencias

- `services/notifier/` — implementación completa
- `deploy/sql/002_trazabilidad.sql` — tabla `notifications` y sus restricciones
- `deploy/grafana/provisioning/dashboards/04-trazabilidad.json` — tablero
- [ADR-008](008-observabilidad-y-slo.md) §"Lo que falta" — el otro canal, el del equipo que opera el sistema, que sigue pendiente
