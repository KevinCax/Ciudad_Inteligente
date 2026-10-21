# ADR-002: Lenguaje y runtime de los microservicios — Go

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el hardware on-premise acota los recursos disponibles)

---

## 1. Contexto

Hay que elegir el lenguaje y el tiempo de ejecución de los seis microservicios. Es de las decisiones más caras de revertir del proyecto: una
vez escrito el sistema, cambiar de lenguaje significa escribirlo otra vez.

El enunciado (sección 5) presenta su stack como **punto de partida, no como
imposición**, y admite explícitamente modificarlo o reemplazarlo siempre que la
decisión quede registrada como ADR.

Dos hechos del contexto pesan sobre la elección, y ambos vienen del cliente
declarado en [ADR-001](001-plataforma-de-despliegue.md):

**El hardware es limitado y propio.** La detección corre en servidores del
centro de datos municipal. Sobre esas dos máquinas conviven los seis microservicios, Kafka, Redis, la observabilidad y el sistema operativo. Cada
megabyte que consume un servicio es un megabyte que no está disponible para el
broker, y **no hay elasticidad**: el hardware es el que se compró.

**El equipo que lo opera es reducido.** No hay una persona dedicada a ajustar
una máquina virtual, diagnosticar pausas del recolector de basura o dimensionar
montones de memoria. Toda especialidad que el sistema exija es una especialidad
que alguien tendrá que adquirir o contratar.

De ahí el criterio: entre lenguajes que resuelven el problema igual de bien
—y son varios—, decide **cuánto cuesta operar el resultado**.

## 2. Opciones consideradas

### Opción A — Java con Spring Boot

Es el stack de referencia implícito del curso y el más extendido en sistemas
empresariales de este tipo.

**A favor**

- **El cliente de Kafka para Java es el más maduro del ecosistema**, con
  productor idempotente completo, que es lo que la guía recomienda.
- Spring Boot aporta mucho resuelto: inyección de dependencias, validación,
  abstracciones sobre Kafka, endpoints de salud y métricas.
- Es lo que más probablemente conozca quien herede el sistema.
- Ecosistema enorme: para cualquier problema hay biblioteca y documentación.

**En contra**

- **La huella de la máquina virtual es la restricción real** sobre hardware
  propio y acotado. Una imagen base de Java ronda los 250 MB antes de añadir la
  aplicación, y el consumo típico de un servicio Spring Boot está en el orden
  de los cientos de megabytes.
- Arranque en segundos, no en milisegundos: reiniciar un contenedor es una
  interrupción perceptible del servicio.
- Ajustar la máquina virtual es una especialidad, y el equipo que operará el
  sistema no la tiene.
- **Las abstracciones de Spring sobre Kafka ocultan justamente lo que este
  sistema necesita controlar**: cuándo se confirma un offset y cuándo se
  considera publicado un mensaje. Son decisiones centrales del
  [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md), y pelearse con una
  abstracción para tomarlas es peor que escribirlas.

### Opción B — Node.js con TypeScript, o Python

**A favor**

- Desarrollo rápido y curva de aprendizaje suave.
- Python encaja bien con el resto del ecosistema de simulación y análisis.
- Huella menor que la de una máquina virtual de Java.

**En contra**

- **Un solo hilo de ejecución efectivo.** La correlación evalúa reglas sobre
  ventanas mientras consume de Kafka y atiende peticiones HTTP; el modelo de un
  solo hilo obliga a rodearlo con procesos, y eso multiplica la huella que se
  quería ahorrar.
- Sigue haciendo falta un intérprete y sus dependencias dentro de la imagen: no
  hay binario autocontenido.
- El tipado dinámico de Python es un riesgo real en un sistema cuyo núcleo es
  un contrato de datos: los errores de forma aparecen en ejecución.

### Opción C — Go

**A favor**

- **Binario estático sin runtime externo**: imágenes de contenedor un orden de
  magnitud más pequeñas y memoria residente en decenas de megabytes.
- Arranque en milisegundos, lo que hace que reiniciar un contenedor sea
  imperceptible en lugar de una interrupción.
- Sin máquina virtual que ajustar ni montón de memoria que dimensionar: un
  equipo reducido no necesita esa especialidad.
- **El modelo de concurrencia encaja con la forma del problema.** El persister
  acumula lotes mientras vence un temporizador y atiende una señal de apagado:
  eso son tres goroutines y un `select`, no un framework.
- Menos dependencias, y por tanto menos superficie que auditar.
- El binario estático permite una imagen `distroless` sin intérprete de
  comandos, lo que reduce lo que un atacante puede hacer dentro del contenedor.

**En contra**

- **Curva de aprendizaje**, con una fecha de entrega fija.
- **El ecosistema de Kafka en Go es menos maduro.** `segmentio/kafka-go` no
  ofrece productor idempotente completo, que la guía recomienda.
- Hay que escribir a mano lo que Spring daría resuelto: carga de configuración,
  middleware HTTP, registro de métricas.
- **Consume tiempo que podría ir a la documentación**, que pesa más en la
  evaluación que la implementación.

## 3. Decisión

**Se adopta Go (opción C).**

### 3.1 Lo que decide es la huella sobre hardware que no crece

El criterio no es la elegancia del lenguaje: es que los servicios conviven con
Kafka y Redis en dos máquinas fijas.

| Aspecto | Medido en este sistema |
|---|---|
| Tamaño de imagen | 22 – 29 MB por servicio, completa |
| Memoria residente | 13 – 18 MB por servicio |
| Tiempo de arranque | 7 – 50 ms |

Frente a eso, una imagen base de Java —`eclipse-temurin:17-jre-alpine`, **257
MB antes de añadir la aplicación**— y un consumo de Spring Boot en el orden de
los cientos de megabytes.

Con seis servicios más Kafka, Redis, Prometheus, Grafana, el motor de rutas y el sistema
operativo sobre dos máquinas, esa diferencia no es cosmética: **decide si la
plataforma cabe holgada o al límite**.

### 3.2 Lo que Go no aporta, y conviene no atribuirle

Un lenguaje no encuentra errores de diseño. Las decisiones que hacen correcto
este sistema —evaluar la caída de velocidad sobre la lista completa, tratar la
confianza como un número real, confirmar el offset después de procesar— salen
de razonar el dominio, y se habrían tomado igual en cualquiera de las tres
opciones.

Atribuirle al lenguaje méritos que corresponden al diseño se desarma en cuanto
alguien pregunte cómo exactamente.

### 3.3 El compromiso sobre la idempotencia del productor

Es la renuncia más seria de esta decisión y conviene decirla sin rodeos.

La guía recomienda `enable.idempotence=true` en el productor.
`segmentio/kafka-go` no lo implementa por completo; `confluent-kafka-go` sí,
pero depende de librdkafka vía cgo, lo que elimina el binario estático y con él
la imagen mínima, que es una de las razones de elegir Go.

Se optó por `segmentio/kafka-go` con `acks=all` e **idempotencia en el
consumidor**: `SET NX` sobre el identificador de evento en el ingestor y el
correlator, `ON CONFLICT DO NOTHING` en el persister. Es una garantía más
amplia que la del productor, porque protege también contra reprocesos por
rebalanceo o reinicio, no solo contra reintentos de escritura. El detalle está
en [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md).

Aun así es un compromiso: se renunció a una recomendación explícita de la guía
a cambio de una propiedad de despliegue, y se compensó por otra vía.

## 4. Consecuencias

### Positivas

- Huella verificada un orden de magnitud menor que la alternativa, sobre
  hardware que no es elástico.
- Reinicios imperceptibles: un despliegue no interrumpe la ingesta.
- Control explícito sobre lo que importa. Confirmar offsets manualmente es una
  línea de código, no una pelea contra la abstracción del framework.
- Imágenes `distroless` sin intérprete de comandos ni gestor de paquetes, lo
  que reduce la superficie de ataque y sostiene el ADR de seguridad.
- Menos dependencias directas: entre tres y siete bibliotecas directas por servicio (jobs usa solo tres), todas
  justificables una a una.

### Negativas aceptadas

- **Tiempo invertido en escribir lo que un framework habría dado resuelto.**
  Carga de configuración, middleware, registro de métricas: alrededor de
  cuatrocientas líneas que hay que mantener.
- **Curva de aprendizaje** con fecha fija. Se mitigó manteniendo el diseño
  deliberadamente simple: biblioteca estándar para HTTP, configuración y
  registros, sin framework.
- **Sin productor idempotente**, según lo explicado arriba.
- **El ecosistema obliga a elegir con cuidado.** La biblioteca de validación
  contra JSON Schema con soporte de *draft 2020-12* —el que exige el contrato
  canónico— tenía una sola opción viable.

### Riesgo de continuidad

Si el equipo que mantenga el sistema sabe Java y no Go, esta decisión le impone
un costo. Se mitiga con tres cosas: el código está comentado explicando el
porqué de cada decisión no evidente, no usa framework alguno —de modo que lo
que hay que entender es el lenguaje y no un ecosistema— y los contratos son
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

Remedida el 21 de octubre de 2026, con el notifier ya incorporado:

| Servicio | Imagen | Memoria residente |
|---|---|---|
| ingestor | 23.3 MB | 15.6 MB |
| correlator | 29.0 MB | 18.0 MB |
| persister | 26.7 MB | 15.6 MB |
| indexer | 22.4 MB | 13.2 MB |
| notifier | 27.3 MB | 15.6 MB |
| jobs | 25.3 MB | 13.0 MB |

El arranque y el comportamiento bajo carga de más abajo no se repitieron con el
notifier: corresponden a la medición de septiembre.

Referencia de contraste: `eclipse-temurin:17-jre-alpine`, una imagen base
habitual para servicios Java, ocupa **257 MB sin incluir la aplicación**.

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

Construir los programas del sistema en un lenguaje de programación **liviano y
autocontenido**, en lugar del más habitual en este tipo de sistemas.

### Por qué

Los programas resultantes ocupan **alrededor de diez veces menos** espacio y
memoria que los de la alternativa evaluada. Como el sistema va a funcionar en
equipos comprados por la Municipalidad, y esos equipos no se pueden ampliar
sobre la marcha, consumir menos significa que la misma inversión en hardware
alcanza para más, o que alcanza con menos equipos.

Además arrancan en milisegundos en lugar de segundos, lo que hace que
actualizar el sistema no interrumpa el servicio de forma perceptible.

La tercera razón es de personal: la alternativa habitual requiere ajustes
especializados de rendimiento que exigen una persona con esa formación. El
equipo de sistemas de la Municipalidad es reducido, y esta opción no lo
necesita.

### Qué costó

**Tiempo.** Este lenguaje trae menos cosas resueltas de fábrica, de modo que
hubo que escribir a mano piezas que la alternativa habría aportado. Es la
contrapartida honesta de la decisión.

También supone que quien mantenga el sistema en el futuro debe conocer este
lenguaje. Se atenuó manteniendo los programas deliberadamente simples y
documentados, y usando formatos de datos independientes del lenguaje, de modo
que cualquier componente pueda reescribirse en el futuro sin afectar a los
demás.

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| El personal futuro no conoce el lenguaje | Media | Medio | Código documentado, sin framework, contratos independientes del lenguaje |
| Herramientas menos maduras que las de la alternativa | Baja | Bajo | Verificado en funcionamiento; las carencias se compensaron por otra vía |
| El tiempo invertido reste a otros entregables | Alta | Medio | Riesgo asumido de forma consciente |

---

## Referencias

- Enunciado del proyecto final, secciones 4 y 5 — el stack es punto de partida,
  no imposición.
- [ADR-001](001-plataforma-de-despliegue.md) — el hardware on-premise es la
  restricción que da peso a la huella.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — idempotencia en el
  consumidor como compensación del productor no idempotente.
- [ADR-010](010-contenerizacion-y-orquestacion.md) — el binario estático es lo
  que permite la imagen mínima.
