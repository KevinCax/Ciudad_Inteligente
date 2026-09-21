# ADR-006: Resiliencia y tolerancia a fallos

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (plataforma híbrida)

---

## 1. Contexto

El sistema es una cadena de cinco eslabones —ingestor, Kafka, correlator,
Redis, persister— y cualquiera de ellos puede fallar. Hay que decidir
**explícitamente qué ocurre con un evento cuando algo falla**, porque la
alternativa no es que no pase nada: es que pase algo sin que nadie lo decida.

El sistema Java de referencia ilustra el costo de no decidirlo. Su consumidor
envolvía todo el procesamiento en un `try/catch` que imprimía el error y
seguía:

```java
} catch (Exception e) {
    System.err.println("Evento inválido o error de procesamiento: " + e.getMessage());
}
```

Como el `commit` de offsets era automático, el offset avanzaba igual. El
resultado es que **un evento que fallara desaparecía sin dejar rastro**: no
quedaba en ninguna cola, no había métrica que lo contara y el productor ya
había recibido un `202`. Nadie podía saber después cuántos eventos se
perdieron ni cuáles.

El enunciado lo pide como decisión documentada (sección 4.2): manejo de caídas
del broker, colas de mensajes muertos, reintentos y **pérdida aceptable de
eventos**. Esa última frase es la clave: hay que declarar qué pérdida se acepta,
no fingir que es cero.

### Qué puede fallar

| Fallo | Probabilidad | Consecuencia si no se atiende |
|---|---|---|
| Evento que no cumple el contrato | Alta | Se descarta sin registro |
| Caída del enlace a la nube | Media | Se detiene la escritura del archivo |
| Caída o rebalanceo de Kafka | Media | Se interrumpe el transporte |
| Caída de Redis | Baja | Se pierde la ventana de correlación |
| Caída de PostgreSQL | Baja | Se detiene el archivo histórico |
| Petición abortada por el cliente | Alta bajo carga | Estado inconsistente |

## 2. Opciones consideradas

### Opción A — Descartar y continuar

Lo que hacía el sistema Java: capturar la excepción, registrarla en consola y
seguir con el siguiente mensaje.

**A favor**

- Trivial de implementar.
- El sistema nunca se atasca: siempre avanza.

**En contra**

- **La pérdida es silenciosa e inauditable.** Para un sistema de seguridad
  ciudadana, no poder responder "¿se procesaron todos los eventos de anoche?"
  es descalificante.
- Un error transitorio —la base tardó en responder— se trata igual que un
  error permanente —el evento está mal formado—, cuando requieren respuestas
  opuestas.

### Opción B — Reintentar indefinidamente en el sitio

Ante cualquier fallo, reintentar el mismo mensaje hasta que funcione, sin
confirmar el offset.

**A favor**

- No se pierde nada.
- Muy simple de razonar.

**En contra**

- **Un solo mensaje mal formado bloquea su partición para siempre.** Como la
  clave de partición es la zona, un evento corrupto de la zona 10 detendría
  toda la detección de esa zona indefinidamente. Es el patrón conocido como
  *poison pill*.
- Reintentar algo que nunca va a funcionar consume recursos sin fin.

### Opción C — Clasificar el fallo: DLQ para lo permanente, reintento para lo transitorio

Distinguir entre fallos que no van a resolverse reintentando y fallos que sí.

**A favor**

- Cada clase de fallo recibe la respuesta correcta.
- Nada se pierde en silencio: lo permanente queda en una cola revisable.
- Una partición nunca se bloquea por un mensaje irrecuperable.

**En contra**

- Más código y más conceptos que sostener.
- Hay que clasificar cada error correctamente; una clasificación equivocada
  manda a la DLQ algo que debía reintentarse, o al revés.
- La DLQ solo sirve si alguien la mira: introduce una obligación operativa.

### Opción D — Outbox transaccional

Escribir el evento y su intención de publicación en la misma transacción de
base de datos, y que un proceso aparte drene la tabla hacia Kafka.

**A favor**

- Garantía más fuerte: el evento y su publicación son atómicos.

**En contra**

- Exige que el camino crítico escriba en la base, que es justo lo que
  [ADR-001](001-plataforma-de-despliegue.md) decidió evitar para no depender
  del enlace a internet.
- Complejidad considerable para una garantía que, en este diseño, ya aporta
  Kafka: el broker es la fuente de verdad del transporte y está en la LAN.

## 3. Decisión

**Se adopta la opción C.** El outbox (D) queda descartado por incompatible con
la decisión de plataforma, no por inferior.

### 3.1 Clasificación de fallos

Es el núcleo de la decisión. Todo error se clasifica en una de dos familias, y
la familia determina la respuesta:

| Familia | Ejemplos | Respuesta | Offset |
|---|---|---|---|
| **Permanente** | JSON ilegible, no cumple el contrato, UUID mal formado | A la DLQ y seguir | **Se confirma** |
| **Transitorio** | Kafka no responde, Redis caído, PostgreSQL inaccesible | Reintentar | **No se confirma** |

El razonamiento: un evento mal formado **seguirá estando mal formado** dentro de
una hora, así que reintentarlo es desperdiciar recursos y bloquear la partición.
Una base que no responde **probablemente sí responderá**, así que descartar el
evento sería tirar información recuperable.

En el ingestor, que atiende peticiones HTTP, la misma clasificación decide el
código de estado: `400` para lo permanente —corrige y reenvía— y `503` para lo
transitorio —reintenta más tarde. El sistema Java respondía `400` a todo, de
modo que el cliente no podía distinguir un caso del otro.

### 3.2 Confirmación manual de offsets

Los consumidores desactivan el `commit` automático (`CommitInterval: 0`) y
confirman **después** de procesar:

```
leer → procesar → confirmar
```

Si el procesamiento falla por causa transitoria, no se confirma y el mensaje se
vuelve a leer. Es lo que convierte "no perder eventos" en una propiedad del
diseño y no en una aspiración.

### 3.3 Dead-letter queue

El topic `events.dlq` (3 particiones, **retención de 14 días**, la más larga de
los tres) recibe todo lo que no se pudo procesar de forma permanente. Cada
mensaje lleva:

- el **payload original intacto**, para poder reprocesarlo una vez corregida la
  causa;
- el **motivo** estructurado, para agrupar por tipo de fallo en los tableros;
- el **`trace_id`**, para cruzarlo con los registros de los servicios;
- la **partición y el offset** de origen, para localizarlo en Kafka.

La retención es larga a propósito: su función es que alguien pueda revisar y
reprocesar, y eso no ocurre en una hora.

### 3.4 Idempotencia en las tres etapas

Reintentar solo es seguro si reprocesar es inofensivo. Cada servicio lo
garantiza a su manera:

| Servicio | Mecanismo | Nota |
|---|---|---|
| Ingestor | `SET NX EX` sobre `ingest:seen:{event_id}` | Una operación atómica |
| Correlator | `SET NX EX` sobre `corr:seen:{event_id}` | Ídem |
| Persister | `INSERT ... ON CONFLICT DO NOTHING` | Lo resuelve la clave primaria |

El sistema Java hacía `hasKey` seguido de `set`: entre ambas llamadas cabe otra
instancia procesando el mismo evento, de modo que la idempotencia se rompía
justo al escalar horizontalmente, que es cuando más hace falta.

Esta propiedad es la que permite que el persister arranque por defecto desde el
offset más antiguo: reprocesar un evento ya archivado solo suma un duplicado a
las métricas, mientras que no archivarlo deja un hueco permanente en el
histórico. Ante la duda, prefiere releer de más.

### 3.5 Publicación confirmada

El productor usa `acks=all` de forma **síncrona**: el `202` al cliente
significa que Kafka confirmó la recepción en todas las réplicas. El sistema
Java publicaba de forma asíncrona y respondía `202` sin esperar; si la
publicación fallaba, el cliente ya se había llevado un "aceptado" y el evento
no llegaba nunca al correlator.

### 3.6 Las rutas de limpieza no heredan la cancelación

Esta regla no estaba prevista: apareció al ejecutar la prueba de carga y vale
la pena documentarla porque es un error fácil de cometer.

Cuando el cliente aborta una petición en vuelo, su contexto se cancela. Si las
tareas de limpieza —liberar la reserva del `event_id`, escribir en la DLQ—
heredan ese contexto, **fallan también**, y dejan el sistema peor que el error
original: el `event_id` quedaba reservado 24 horas y el reintento del cliente
recibía un `409` por un evento que nunca se publicó.

La limpieza hay que ejecutarla precisamente cuando el contexto de la petición
ya murió. Se usa `context.WithoutCancel`, que conserva los valores —el
`trace_id` entre ellos— pero descarta la cancelación, con un plazo acotado para
que no se quede colgada.

### 3.7 Matriz de comportamiento ante fallos

| Componente caído | Ingesta | Detección | Archivo | Respuesta al cliente |
|---|---|---|---|---|
| Persister | Continúa | Continúa | Se detiene y acumula | `202` |
| PostgreSQL | Continúa | Continúa | Se detiene y acumula | `202` |
| Enlace a la nube | Continúa | Continúa | Se detiene y acumula | `202` |
| Elasticsearch / Grafana | Continúa | Continúa | Continúa | `202` |
| Redis | **Se detiene** | Se degrada | Continúa | `503` |
| Kafka | **Se detiene** | Se detiene | Se detiene | `503` |

Las dos últimas filas son las dependencias duras, y ambas están **en la LAN**:
el sistema solo se detiene por causas que la Municipalidad controla
directamente, nunca por un proveedor externo o un enlace de terceros.

## 4. Pérdida aceptable declarada

El enunciado pide declararla, y conviene hacerlo sin eufemismos.

### No se acepta perder

- **Un evento que recibió `202`.** El `202` es un compromiso: significa que
  Kafka lo confirmó y que va a procesarse. Si se pierde después, es un defecto.
- **Una alerta generada.** Se publica en Kafka antes de considerarse emitida.

### Sí se acepta

- **Eventos que no cumplen el contrato.** No se pierden —quedan 14 días en la
  DLQ— pero no se procesan. Si nadie revisa la DLQ, se pierden al expirar: es
  una pérdida *operativa*, no técnica, y por eso hay una métrica por motivo.
- **Duplicados.** Se prefiere procesar dos veces a no procesar. La idempotencia
  los absorbe.
- **Retraso en el archivo histórico.** Durante una caída del enlace, los
  tableros muestran datos atrasados. Para consulta operativa inmediata están
  los endpoints del correlator, que leen de Redis.
- **La ventana de correlación ante una caída de Redis.** Las correlaciones en
  curso se pierden y hay que esperar a que se reconstruyan con eventos nuevos.
  Redis usa `appendonly`, lo que acota el daño a un reinicio, pero no lo
  elimina.
- **Eventos rechazados con `503`.** El cliente recibe una instrucción explícita
  de reintentar. Si no lo hace, la pérdida es suya y es visible en su lado.

## 5. Consecuencias

### Positivas

- Ninguna pérdida es silenciosa: todo fallo queda en una métrica, en un
  registro y, si es permanente, en la DLQ con su payload original.
- Ninguna partición se bloquea por un mensaje irrecuperable.
- El cliente puede distinguir "corrige el evento" de "reintenta más tarde".
- El sistema sobrevive a la caída de cualquier componente de la nube sin dejar
  de detectar incidentes.

### Negativas aceptadas

- **La DLQ impone una obligación operativa.** Si nadie la revisa, es un
  cementerio con retención. Hay que definir quién la mira y con qué frecuencia,
  y eso es trabajo recurrente para un equipo municipal reducido.
- **Confirmar offsets después de procesar implica entrega "al menos una vez".**
  Habrá duplicados tras un rebalanceo o un reinicio. Es el precio de no perder
  nada, y se paga con idempotencia.
- **Clasificar mal un error tiene consecuencias.** Tratar como permanente algo
  transitorio descarta información recuperable. La clasificación vive en un
  solo lugar por servicio, para que revisarla sea fácil.
- **Más código que mantener.** La alternativa simple —descartar y seguir— es
  varias veces más corta.

## 6. Evidencia medida

Esta decisión no se sostiene en argumentos: se verificó ejecutando el sistema
el 21 de septiembre de 2026.

### Integridad bajo carga

Ráfaga de 100 eventos por segundo durante 10 segundos, con fases de
calentamiento y recuperación:

| Métrica | Resultado |
|---|---|
| Peticiones | 1 192, **todas `202`** |
| Eventos en Kafka | 2 403 |
| Eventos en PostgreSQL | **2 403** |
| Alertas en Kafka | 40 |
| Alertas en PostgreSQL | **40** |

Cero discrepancia entre lo transportado y lo archivado.

### Recuperación tras una caída

Se detuvo el persister —equivalente a perder el enlace a la nube— y se
siguieron enviando eventos:

- El ingestor se mantuvo en `UP`: sus dependencias son Redis y Kafka, ambas
  en la LAN.
- Se siguieron generando alertas en las cuatro zonas de prueba.
- El archivo histórico quedó congelado en 2 392 eventos.

Al restablecerlo, recuperó **exactamente los 11 eventos pendientes** (2 392 →
2 403) y el *lag* del grupo de consumo volvió a cero. Sin pérdida y sin
intervención manual.

La recuperación no es instantánea: el rebalanceo del grupo de Kafka tarda
decenas de segundos. Conviene saberlo para no confundir un rebalanceo en curso
con un servicio atascado.

### El fallo que encontró la prueba

La prueba de carga depositó un mensaje en la DLQ con motivo
`kafka_no_disponible` y detalle `context canceled`. Investigarlo destapó el
problema de la sección 3.6. La corrección se verificó reproduciendo el mismo
escenario:

| Momento | `event_id` | Reserva en Redis |
|---|---|---|
| 14:51:48, antes de corregir | `f090f8a4…` | **retenida** 24 h |
| 14:57:43, después de corregir | `d578df2c…` | **liberada** |

Mismo escenario de fallo, resultado opuesto. Merece señalarse que el defecto no
lo encontró ninguna revisión de código ni ningún test unitario: lo encontró
**ejecutar el sistema bajo carga y molestarse en investigar un único mensaje en
la DLQ**. Esa es, en sí misma, la justificación de tener una DLQ.

## 7. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Que **ningún evento se pierda sin dejar constancia**. Cuando algo falla, el
sistema distingue entre dos situaciones y actúa distinto en cada una:

- Si el problema es del evento —viene mal formado—, se aparta en una bandeja
  de revisión donde queda guardado catorce días, con toda su información
  original.
- Si el problema es del sistema —un servicio no responde—, el evento se
  reintenta hasta que funcione.

### Por qué importa

Una municipalidad tiene que poder responder a la pregunta *"¿se procesaron
todos los eventos de anoche?"*. Con este diseño, la respuesta es verificable:
o el evento está archivado, o está en la bandeja de revisión con el motivo por
el que no se pudo procesar. No hay una tercera posibilidad.

Además, si se cae el servicio de internet contratado, **la ciudad sigue
detectando incidentes**. Lo verificamos apagando deliberadamente esa parte del
sistema: se siguieron generando alertas y, al restablecerlo, recuperó por sí
solo los once eventos pendientes sin que nadie interviniera.

### Qué exige a cambio

Una persona debe **revisar periódicamente la bandeja de eventos rechazados**.
El sistema deja constancia de todo, pero no puede decidir por sí mismo qué
hacer con un evento mal formado: eso requiere criterio humano. Sin esa
revisión, la bandeja se convierte en un archivo que nadie lee y la información
se pierde igual, solo que catorce días más tarde.

Se recomienda una revisión semanal y una alerta automática si la bandeja
supera un umbral de mensajes.

### Costo y tiempo

No añade costo de infraestructura: la bandeja de revisión ocupa el mismo
sistema de mensajería que ya se usa. El costo es de **tiempo de personal**:
aproximadamente una hora semanal de revisión, que puede automatizarse en buena
medida cuando se identifiquen los motivos de rechazo más frecuentes.

La implementación está terminada y verificada.

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — resiliencia y tolerancia a fallos.
- [ADR-001](001-plataforma-de-despliegue.md) — la plataforma híbrida es la que
  descarta el outbox transaccional.
- [deploy/README.md](../../deploy/README.md) — procedimiento para reproducir la
  prueba de caída y recuperación.
- `services/*/internal/broker/kafka.go` — confirmación manual de offsets y
  publicación en la DLQ.
- `services/ingestor/internal/ingest/service.go` — clasificación de fallos y
  contexto de limpieza.
