# ADR-002: Lenguaje y runtime de los microservicios — Go

**Estado:** aceptado (razonamiento revisado el 2026-10-28)
**Fecha:** 2026-09-21
**Decide:** el equipo
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el hardware on-premise acota los recursos disponibles), [ADR-003](003-organizacion-del-repositorio.md) (el contrato es un schema, no una biblioteca: un servicio puede reescribirse en otro lenguaje)

---

> **Nota de revisión.** Este ADR se revisó después de presentar el proyecto. La
> versión original justificaba Go con argumentos técnicos: la huella de los
> programas y el control sobre Kafka. En la presentación se objetó que Java ya
> hace lo que hicimos en Go, y la objeción es correcta en lo esencial. Esta
> versión dice cuál fue la razón real de la decisión y deja los argumentos
> técnicos donde corresponden: ventajas que comprobamos al usar el lenguaje, no
> la causa de haberlo elegido.

## 1. Contexto

Hay que elegir el lenguaje y el tiempo de ejecución de los seis microservicios.
Es una de las decisiones más caras de revertir del proyecto: una vez escrito el
sistema, cambiar de lenguaje significa escribirlo otra vez. El enunciado
(sección 5) presenta su stack como punto de partida y no como imposición, y
admite modificarlo siempre que la decisión quede registrada como ADR.

Dos hechos pesan sobre la elección.

**Varios lenguajes habrían servido.** El volumen estimado es de 20 a 100 eventos
por segundo ([ADR-001](001-plataforma-de-despliegue.md)). Java con Spring Boot,
Python, Node.js o Go pueden ingerir esos eventos, correlacionarlos en ventanas,
consumir de Kafka y escribir en PostgreSQL con holgura. Ninguna de las opciones
queda descartada por no poder hacer el trabajo.

**Ninguno de nosotros había programado antes en Go.** Los otros lenguajes que
se consideraron sí los habíamos usado. Elegir entre ellos habría sido, para el
equipo, el camino conocido.

Como la capacidad técnica no separa a las opciones, lo que decide es otra
pregunta: además del sistema, qué queremos llevarnos de este proyecto.

## 2. Opciones consideradas

### Opción A — Java con Spring Boot

Es el stack más extendido en sistemas empresariales de este tipo.

**A favor**

- **El cliente de Kafka para Java es el más maduro del ecosistema**, con
  productor idempotente completo, que es lo que la guía recomienda.
- Spring Kafka permite confirmar los offsets manualmente, de modo que el control
  que este sistema necesita (confirmar después de procesar) también está
  disponible allí.
- Spring Boot aporta resueltos la inyección de dependencias, la validación, las
  abstracciones sobre Kafka y los puntos de salud y métricas.
- Menor curva de aprendizaje para el equipo y más personal capaz de mantenerlo.

**En contra**

- Una imagen base de Java ronda los 250 MB antes de añadir la aplicación, y un
  servicio Spring Boot consume del orden de cientos de megabytes. La diferencia
  con Go es real, pero a este volumen y sobre el hardware del laboratorio **no
  es decisiva**.
- Arranca en segundos y no en milisegundos.
- No representa ningún reto de aprendizaje para el equipo.

### Opción B — Node.js con TypeScript, o Python

**A favor**

- Desarrollo rápido y curva de aprendizaje suave.
- Python encaja bien con el análisis de datos.
- Huella menor que la de una máquina virtual de Java.

**En contra**

- Sigue haciendo falta un intérprete y sus dependencias dentro de la imagen: no
  hay binario autocontenido.
- El tipado dinámico es un riesgo en un sistema cuyo núcleo es un contrato de
  datos: los errores de forma aparecen en ejecución.
- Tampoco supone un reto de aprendizaje.

### Opción C — Go

**A favor**

- **Es un lenguaje nuevo para todo el equipo**, y con él se aprende a trabajar
  con concurrencia, binarios estáticos y herramientas de infraestructura.
- Binario estático sin runtime externo, imágenes pequeñas y arranque en
  milisegundos.
- Un modelo de concurrencia (goroutines y `select`) que encaja con la forma de
  los servicios que consumen de Kafka.
- Biblioteca estándar suficiente para HTTP, configuración y registros, sin
  framework.
- Herramientas de serie (`go test`, `go vet`, `gofmt`), que dan a todo el equipo
  las mismas reglas de formato y de verificación.

**En contra**

- **Curva de aprendizaje**, con una fecha de entrega fija.
- **El ecosistema de Kafka en Go es menos maduro.** `segmentio/kafka-go` no
  ofrece productor idempotente completo.
- Hay que escribir a mano lo que Spring daría resuelto: carga de configuración,
  middleware HTTP, registro de métricas.
- Menos personal conoce el lenguaje que el que conoce Java.

## 3. Decisión

**Se adopta Go (opción C), por decisión del equipo.**

### 3.1 La razón: un reto que quisimos tomar

Decidimos usar Go porque ninguno de nosotros había programado en ese lenguaje y
el proyecto era una buena oportunidad para aprenderlo. Otros lenguajes nos
habrían dado un camino más corto y con menos tropiezos, y lo sabíamos: elegimos
el más difícil a propósito.

Un proyecto de curso produce dos cosas, el sistema y lo que el equipo aprende al
construirlo. Quisimos que lo segundo incluyera un lenguaje nuevo, y uno que se
usa mucho en infraestructura: están escritos en Go varios de los programas que
usamos en este mismo proyecto, como Prometheus, Grafana y Docker. Programar en
el lenguaje de las herramientas que se operan ayuda a entender cómo funcionan.

Esta razón se escribe así porque es la verdadera. Y conviene decir hasta dónde
llega: es una razón válida para un proyecto académico. No lo sería, por sí sola,
para un cliente que paga por un sistema; ahí la elección natural habría sido el
lenguaje que el equipo ya domina, y es lo que sugerimos si otro equipo recibe
este sistema (ver 4).

### 3.2 Lo que reconocemos: otros lenguajes habrían podido hacerlo

Java con Spring Boot habría cubierto lo mismo que hicimos en Go. Tiene un
cliente de Kafka con productor idempotente completo, permite confirmar offsets
manualmente y trae resuelta buena parte de lo que escribimos a mano. Las
decisiones que hacen correcto este sistema (evaluar la caída de velocidad sobre
la lista completa, confirmar el offset después de procesar, tratar la confianza
como un número real) salen de razonar el dominio y se habrían tomado igual en
cualquiera de las opciones.

Tampoco la huella decide. La diferencia entre un binario de 25 MB y una imagen
Java existe, y se mide más abajo, pero con seis servicios y un volumen de
decenas de eventos por segundo ninguna de las dos opciones deja al sistema sin
recursos.

### 3.3 Ventajas que encontramos al usarlo

No fueron la causa de elegir Go, pero hacen que la elección sea razonable de
defender, y las comprobamos en el sistema en ejecución:

- **Un binario por servicio, sin runtime externo.** Imágenes de 22 a 29 MB,
  memoria residente de 13 a 18 MB y arranque en milisegundos (ver 5). Reiniciar
  un contenedor no interrumpe la ingesta de forma perceptible.
- **Concurrencia que encaja con el problema.** El persister acumula un lote
  mientras vence un temporizador y atiende una señal de apagado: tres goroutines
  y un `select`, no un framework.
- **Pocas dependencias.** Entre tres y siete bibliotecas directas por servicio
  (`jobs` usa solo tres), todas justificables una a una, lo que reduce la
  superficie que auditar.
- **Las mismas herramientas para todos.** `gofmt`, `go vet` y las pruebas
  automáticas corren dentro de `make check`, de modo que el código de cualquier
  integrante pasa por las mismas comprobaciones antes de aceptarse.
- **Imágenes `distroless`.** El binario estático permite una imagen sin
  intérprete de comandos, lo que reduce lo que se puede hacer dentro del
  contenedor ([ADR-010](010-contenerizacion-y-orquestacion.md)).
- **Un lenguaje pequeño.** Hay poco que aprender antes de escribir código útil,
  lo que facilita que todo el equipo pueda aportar aun partiendo de cero.

### 3.4 El compromiso sobre la idempotencia del productor

Es la renuncia más seria de esta decisión y conviene decirla sin rodeos.

La guía recomienda `enable.idempotence=true` en el productor.
`segmentio/kafka-go` no lo implementa por completo; `confluent-kafka-go` sí,
pero depende de librdkafka vía cgo, lo que elimina el binario estático y con él
la imagen mínima, una de las ventajas de Go.

Se optó por `segmentio/kafka-go` con `acks=all` e **idempotencia en el
consumidor**: `SET NX` sobre el identificador de evento en el ingestor y el
correlator, `ON CONFLICT DO NOTHING` en el persister. Es una garantía más
amplia que la del productor, porque protege también contra reprocesos por
rebalanceo o reinicio, no solo contra reintentos de escritura. El detalle está
en [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md).

Aun así es un compromiso: con Java no habría hecho falta compensarlo.

### 3.5 Cómo acotamos el riesgo de aprender sobre la marcha

Elegir un lenguaje que no se conoce tiene un riesgo evidente, y lo tratamos
como tal:

- **Sin framework.** Biblioteca estándar para HTTP, configuración y registros,
  de modo que lo que hay que entender es el lenguaje y no un ecosistema.
- **Verificación automática.** Formato, `vet`, pruebas y comprobación de
  contratos corren juntos en `make check`.
- **Verificar contra el sistema en ejecución**, no solo con pruebas unitarias.
  Así aparecieron defectos que ningún compilador marca, como una limpieza que
  heredaba un contexto cancelado ([ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) §3.6).
- **Cada decisión no evidente, comentada en el código** y registrada en un ADR.

## 4. Consecuencias

### Positivas

- **Aprendizaje compartido.** Ninguno partía con ventaja en el lenguaje, lo que
  nivela al equipo y hace que el conocimiento sea de todos.
- Familiaridad con el lenguaje en el que están hechas varias herramientas de
  infraestructura que usamos.
- Las ventajas técnicas del apartado 3.3: imágenes pequeñas, reinicios
  imperceptibles y pocas dependencias.
- Control explícito sobre lo que importa: confirmar offsets manualmente es una
  línea de código.

### Negativas aceptadas

- **Tiempo.** Aprender el lenguaje y escribir lo que un framework habría dado
  resuelto (carga de configuración, middleware, registro de métricas: alrededor
  de cuatrocientas líneas que hay que mantener) consumió tiempo que pudo ir a la
  documentación, que pesa más en la evaluación que la implementación.
- **Más riesgo que con un lenguaje conocido.** Se acotó como se explica en 3.5,
  pero no desaparece.
- **Sin productor idempotente**, según lo explicado en 3.4.
- **El ecosistema obliga a elegir con cuidado.** La biblioteca de validación
  contra JSON Schema con soporte de *draft 2020-12*, el que exige el contrato
  canónico, tenía una sola opción viable.
- **La razón es del equipo, no del cliente.** La decisión sirve al objetivo de
  aprender, no al de minimizar el riesgo de una institución que heredara el
  sistema.

### Riesgo de continuidad

Si el equipo que mantenga el sistema sabe Java y no Go, esta decisión le impone
un costo. Se mitiga con tres cosas: el código está comentado explicando el
porqué de cada decisión no evidente, no usa framework alguno, y los contratos
son JSON Schema, independientes del lenguaje. Por eso un servicio puede
reescribirse en otro lenguaje, de a uno y sin detener el sistema, sin tocar los
demás ([ADR-003](003-organizacion-del-repositorio.md)). Si la Municipalidad
tomara este sistema como base de producción y su equipo trabajara en otro
lenguaje, esa es la ruta que recomendamos.

### Cuándo revisar esta decisión

- Si el sistema pasa a producción y lo mantiene un equipo que domina otro
  lenguaje.
- Si la ausencia de productor idempotente se vuelve un problema real y se
  necesita una garantía de entrega exactamente una vez.

## 5. Evidencia medida

Estas cifras ilustran las ventajas del apartado 3.3. **No son la razón de la
decisión**, y no prueban que Go sea mejor que Java para este sistema: solo
miden lo que obtuvimos.

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
> cumple su objetivo, no como proyección de capacidad en producción. Un sistema
> equivalente en otro lenguaje podría cumplir el mismo objetivo.

## 6. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Programar los componentes del sistema en **Go**, un lenguaje que el equipo no
conocía, por decisión propia del equipo y con el fin de aprender.

### Por qué

No porque fuera la opción técnicamente única: otros lenguajes más conocidos
habrían servido igual y con menos riesgo. Se eligió porque el proyecto es
también un ejercicio de aprendizaje, y el equipo quiso aprovecharlo para
trabajar con un lenguaje nuevo, muy usado en infraestructura.

Como beneficio práctico, los programas resultantes son pequeños (alrededor de
25 MB cada uno) y arrancan en milisegundos, de modo que actualizar el sistema
no interrumpe el servicio de forma perceptible y los equipos necesarios son
modestos.

### Qué costó

**Tiempo y riesgo.** Aprender un lenguaje durante el proyecto consumió horas que
habrían podido dedicarse a otras tareas, y un lenguaje desconocido tiene más
probabilidad de errores. Se atenuó con verificaciones automáticas y probando el
sistema en funcionamiento.

También supone que quien mantenga el sistema debe conocer este lenguaje, y hay
menos personas que lo conocen que las que conocen Java.

### Qué significa para la Municipalidad

Si la Municipalidad quiere usar este sistema y su equipo trabaja en otro
lenguaje, **no tiene que rehacerlo todo**. Cada componente se comunica con los
demás mediante formatos independientes del lenguaje, de modo que puede
reescribirse de uno en uno, sin detener el sistema.

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| El personal futuro no conoce el lenguaje | Media | Medio | Código documentado, sin framework, contratos independientes del lenguaje: se puede reescribir componente a componente |
| El equipo comete errores por inexperiencia en el lenguaje | Media | Medio | Formato, análisis y pruebas automáticas, y verificación contra el sistema en ejecución |
| Herramientas menos maduras que las de la alternativa | Baja | Bajo | Verificado en funcionamiento; las carencias se compensaron por otra vía |
| El tiempo de aprendizaje resta a otros entregables | Alta | Medio | Riesgo asumido de forma consciente |

---

## Referencias

- Enunciado del proyecto final, secciones 4 y 5 — el stack es punto de partida,
  no imposición.
- [ADR-001](001-plataforma-de-despliegue.md) — el volumen estimado y el
  hardware on-premise.
- [ADR-003](003-organizacion-del-repositorio.md) — el contrato como schema
  permite reescribir un servicio en otro lenguaje.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — idempotencia en el
  consumidor como compensación del productor no idempotente.
- [ADR-010](010-contenerizacion-y-orquestacion.md) — el binario estático es lo
  que permite la imagen mínima.
