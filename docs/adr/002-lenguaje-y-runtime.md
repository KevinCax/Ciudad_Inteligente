# ADR-002: Lenguaje y runtime de los microservicios — Go sobre Java/Spring Boot

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el hardware on-premise acota los recursos disponibles)

---

## 1. Contexto

Existía un sistema funcionando en Java 17 con Spring Boot 3.5.5: dos
microservicios, ingestor y correlator, con Kafka, Redis y PostgreSQL. Cambiar
de lenguaje una base que ya funciona es una decisión que exige justificación, no
al revés.

El enunciado del proyecto (sección 5) presenta su stack como **punto de
partida, no como imposición**, y admite explícitamente modificarlo o
reemplazarlo siempre que la decisión quede registrada como ADR. Ese permiso
abre la puerta, pero no la justifica.

Dos hechos del contexto pesan sobre la decisión:

**El hardware es limitado y propio.** [ADR-001](001-plataforma-de-despliegue.md)
sitúa la detección en servidores del centro de datos municipal. Sobre esas dos
máquinas conviven los microservicios, Kafka, Redis y el sistema operativo. Cada
megabyte de memoria que consume un servicio es un megabyte que no está
disponible para el broker, y no hay elasticidad: el hardware es el que se
compró.

**El equipo que lo opera es reducido.** No hay una persona dedicada a ajustar
la JVM, diagnosticar pausas del recolector de basura o dimensionar *heaps*.

### El estado real del sistema heredado

Una revisión del código Java encontró defectos que conviene enumerar porque
condicionan la decisión:

- La regla de correlación `accident` era **matemáticamente inalcanzable**:
  filtraba las lecturas de velocidad a las mayores de 80 y después buscaba,
  sobre esa misma lista, una caída por debajo de 40.
- El `score` de las alertas se declaraba `Integer` y se redondeaba, de modo que
  0.85, 0.90 y 0.95 se guardaban los tres como `1`.
- El ingestor respondía `202` antes de confirmar la publicación en Kafka, así
  que un fallo dejaba el evento en la base sin llegar nunca al correlator.
- No existía cola de mensajes muertos: todo lo que fallaba se descartaba en
  silencio.
- `/metrics` devolvía `{"alerts":0,"events":0}` fijo.

Ninguno de estos defectos es culpa de Java. Todos son corregibles en Java. Pero
su número indica que el sistema necesitaba una revisión profunda, y eso abre la
pregunta de si conviene hacerla sobre la base existente o desde cero.

## 2. Opciones consideradas

### Opción A — Mantener Java/Spring Boot y corregir en el sitio

Arreglar los defectos sobre el código existente, sin reescribir.

**A favor**

- El equipo ya conoce el lenguaje y el framework.
- Es la opción más barata en tiempo, y el tiempo es el recurso escaso.
- Spring Boot aporta mucho resuelto: inyección de dependencias, validación,
  abstracciones sobre Kafka, *actuator* para salud y métricas.
- El cliente de Kafka para Java es el más maduro del ecosistema, con productor
  idempotente completo.

**En contra**

- Se conserva la huella de la JVM, que es la restricción real sobre hardware
  propio y limitado.
- Corregir en el sitio tiende a dejar intactas las decisiones que causaron los
  defectos. El filtro que hacía inalcanzable la regla `accident` no era un
  error de sintaxis: era una decisión de diseño equivocada, del tipo que se
  arrastra cuando solo se parchea.
- Las abstracciones de Spring sobre Kafka ocultan justamente lo que este
  sistema necesita controlar: cuándo se confirma un offset y cuándo se
  considera publicado un mensaje.

### Opción B — Reescribir en Java, corrigiendo el diseño

Rehacer los servicios desde cero, pero en el mismo lenguaje.

**A favor**

- Se obtiene el beneficio de revisar cada decisión, que es de donde salieron
  los hallazgos.
- Sin curva de aprendizaje.

**En contra**

- Se paga el costo completo de una reescritura sin obtener ninguna mejora
  operativa: la huella y el tiempo de arranque siguen siendo los de la JVM.

> Esta opción existe en el análisis por una razón: **separa el valor de
> reescribir del valor de cambiar de lenguaje.** Sin ella sería fácil
> atribuirle a Go méritos que en realidad corresponden a la reescritura.

### Opción C — Reescribir en Go

**A favor**

- Binario estático sin runtime externo: imágenes de contenedor un orden de
  magnitud más pequeñas y memoria residente en decenas de megabytes.
- Arranque en milisegundos, lo que hace que reiniciar un contenedor sea
  imperceptible en lugar de una interrupción de servicio.
- Sin recolector de basura que ajustar ni *heap* que dimensionar: un equipo
  reducido no necesita esa especialidad.
- El modelo de concurrencia encaja con la forma del problema. El persister
  acumula lotes mientras vence un temporizador y atiende una señal de apagado:
  eso son tres goroutines y un `select`, no un framework.
- Menos dependencias, y por tanto menos superficie que auditar.

**En contra**

- **Curva de aprendizaje**, con una fecha de entrega fija.
- El ecosistema de Kafka en Go es menos maduro. `segmentio/kafka-go` no ofrece
  productor idempotente completo, que la guía del curso recomienda.
- Hay que escribir a mano lo que Spring daba resuelto: carga de configuración,
  middleware HTTP, registro de métricas.
- **Consume tiempo que podría ir a la documentación**, que pesa más en la
  evaluación que la implementación.

## 3. Decisión

**Se reescriben los microservicios en Go (opción C).**

### Qué aporta Go que no aporta la reescritura

La opción B obliga a ser precisos. Comparando C contra B —es decir,
descontando todo lo que aporta reescribir— lo que Go añade es:

| Aspecto | Medido |
|---|---|
| Tamaño de imagen | 23–29 MB por servicio, completa |
| Memoria residente | 15–19 MB por servicio |
| Tiempo de arranque | 7–50 ms |

Frente a eso, la imagen base que usaba el sistema Java —`eclipse-temurin:17-jre-alpine`,
**257 MB antes de añadir el jar de la aplicación**— y un consumo típico de
Spring Boot en el orden de los cientos de megabytes.

Sobre los dos servidores del centro de datos municipal, con tres servicios más
Kafka, Redis y el sistema operativo, esa diferencia no es cosmética: decide si
la plataforma cabe holgada o al límite.

### Qué aporta la reescritura, independientemente del lenguaje

Conviene atribuirlo donde corresponde. Los hallazgos de fondo —la regla
inalcanzable, el `score` truncado, la pérdida silenciosa de eventos— **salieron
de volver a razonar cada decisión**, y habrían salido igual reescribiendo en
Java. Go no los encontró; reescribir sí.

Presentarlos como mérito del lenguaje sería deshonesto, y se desarma en cuanto
alguien pregunte cómo exactamente.

### El compromiso sobre la idempotencia del productor

La guía recomienda `enable.idempotence=true` en el productor.
`segmentio/kafka-go` no lo implementa por completo; `confluent-kafka-go` sí,
pero depende de librdkafka vía cgo, lo que elimina el binario estático y con él
la imagen mínima, que es una de las razones de elegir Go.

Se optó por `segmentio/kafka-go` con `acks=all` y **idempotencia en el
consumidor**: `SET NX` sobre el identificador de evento en el ingestor y el
correlator, `ON CONFLICT DO NOTHING` en el persister. Es una garantía más
robusta que la del productor, porque protege también contra reprocesos por
rebalanceo o reinicio, no solo contra reintentos de escritura. El detalle está
en [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md).

Es un compromiso real y hay que decirlo así: se renunció a una recomendación
explícita de la guía a cambio de una propiedad de despliegue, y se compensó por
otra vía.

## 4. Consecuencias

### Positivas

- Huella verificada un orden de magnitud menor, sobre hardware que no es
  elástico.
- Reinicios imperceptibles: un despliegue no interrumpe la ingesta.
- Control explícito sobre lo que importa. Confirmar offsets manualmente es una
  línea de código, no una pelea contra la abstracción del framework.
- Imágenes `distroless` sin intérprete de comandos ni gestor de paquetes, lo
  que reduce la superficie de ataque y facilita el ADR de seguridad.
- Menos dependencias directas: cinco librerías por servicio, todas justificables
  una a una.

### Negativas aceptadas

- **Tiempo invertido en reescribir que no fue a la documentación.** Con la
  evaluación pesando más en los ADR y el informe que en la implementación, es
  el costo más serio de esta decisión.
- **Curva de aprendizaje** con fecha fija. Se mitigó manteniendo el diseño
  deliberadamente simple: biblioteca estándar para HTTP, configuración y
  registros, sin framework.
- **Se escribió a mano lo que Spring resolvía.** Carga de configuración,
  middleware, registro de métricas: alrededor de cuatrocientas líneas que
  antes no había que mantener.
- **Sin productor idempotente**, según lo explicado arriba.
- **El ecosistema obliga a elegir con cuidado.** La biblioteca de validación
  contra JSON Schema con soporte de *draft 2020-12* —el que exige el contrato
  canónico— tenía una sola opción viable.

### Riesgo de continuidad

Si el equipo que mantenga el sistema después sabe Java y no Go, esta decisión
les impone un costo. Se mitiga con tres cosas: el código está comentado
explicando el porqué de cada decisión no evidente, no usa framework alguno —de
modo que lo que hay que entender es Go y no un ecosistema— y los contratos son
JSON Schema, independientes del lenguaje: reescribir un servicio en otro
lenguaje no obliga a tocar los demás.

## 5. Evidencia medida

Medido el 21 de septiembre de 2026 sobre el sistema en ejecución.

### Huella

| Servicio | Imagen | Memoria residente | Goroutines |
|---|---|---|---|
| ingestor | 23.3 MB | 15.0 MB | 9 |
| correlator | 28.9 MB | 19.1 MB | 14 |
| persister | 26.7 MB | 17.9 MB | 19 |

Referencia: `eclipse-temurin:17-jre-alpine`, la imagen base del sistema Java,
ocupa **257 MB sin incluir la aplicación**.

### Arranque

Desde que el proceso carga su configuración hasta que acepta conexiones:

| Servicio | Tiempo |
|---|---|
| ingestor | 7 ms |
| correlator | 50 ms |
| persister | 28 ms |

### Comportamiento bajo carga

Ráfaga de 100 eventos por segundo durante 10 segundos: 1 192 peticiones,
**todas con respuesta `202`**, percentil 99 de 19,7 ms. Cero discrepancia entre
los 2 403 eventos transportados por Kafka y los 2 403 archivados en PostgreSQL.

Latencia extremo a extremo evento → alerta: media de 44 ms, **100 % por debajo
del objetivo de 2 segundos**.

> Estas cifras corresponden a un entorno de laboratorio sobre una sola máquina.
> Sirven para comparar órdenes de magnitud y para verificar que el sistema
> cumple su objetivo, no como proyección de capacidad en producción.

## 6. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Reescribir los programas del sistema en un lenguaje de programación distinto,
más liviano, en lugar de seguir corrigiendo los existentes.

### Por qué

Los programas nuevos ocupan **alrededor de diez veces menos** espacio y memoria
que los anteriores. Como el sistema va a funcionar en equipos comprados por la
Municipalidad, y esos equipos no se pueden ampliar sobre la marcha, consumir
menos significa que la misma inversión en hardware alcanza para más, o que
alcanza con menos equipos.

Además arrancan en milisegundos en vez de segundos, lo que hace que actualizar
el sistema no interrumpa el servicio de forma perceptible.

Hay un segundo beneficio, y conviene atribuirlo con precisión: **rehacer los
programas obligó a revisar cada decisión**, y ahí aparecieron varios defectos
del sistema anterior —entre ellos una regla de detección de accidentes que
nunca podía activarse. Ese beneficio viene de rehacerlos, no del lenguaje
elegido; se habría obtenido igual rehaciéndolos en el lenguaje original.

### Qué costó

**Tiempo.** Rehacer los programas consumió esfuerzo que podría haberse dedicado
a otras partes del proyecto. Es la contrapartida honesta de esta decisión.

También supone que quien mantenga el sistema en el futuro debe conocer este
lenguaje. Se atenuó manteniendo los programas deliberadamente simples y
documentados, y usando formatos de datos independientes del lenguaje, de modo
que cualquier componente pueda reescribirse en el futuro sin afectar a los
demás.

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| El personal futuro no conoce el lenguaje | Media | Medio | Código documentado, sin framework, contratos independientes del lenguaje |
| Herramientas menos maduras que las del lenguaje anterior | Baja | Bajo | Verificado en funcionamiento; las carencias se compensaron por otra vía |
| El tiempo invertido reste a otros entregables | Alta | Medio | Riesgo materializado y asumido |

---

## Referencias

- Enunciado del proyecto final, secciones 4 y 5 — el stack es punto de partida,
  no imposición.
- [ADR-001](001-plataforma-de-despliegue.md) — el hardware on-premise es la
  restricción que da peso a la huella.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — idempotencia en el
  consumidor como compensación del productor no idempotente.
- `../../Ciudad_Inteligente/` — sistema Java de referencia, conservado sin
  modificar.
