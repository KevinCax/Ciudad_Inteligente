# Anexo A — Registro de decisiones de arquitectura

**Proyecto:** Ciudad Inteligente — sistema de monitoreo perimetral urbano
**Cliente hipotetico:** Municipalidad de Guatemala
**Curso:** Arquitectura de Computadores II (040) — Universidad Mariano Galvez
**Fecha:** 21/10/2026

---

## Proposito de este anexo

El enunciado exige un minimo de seis decisiones de arquitectura registradas en
el formato de su seccion 4.1 y consolidadas en un solo documento. Este es ese
documento.

Cada ADR responde a cinco apartados obligatorios:

1. **Contexto** — el problema tecnico que obliga a decidir.
2. **Opciones consideradas** — al menos dos alternativas reales, con sus
   trade-offs.
3. **Decision** — la alternativa elegida.
4. **Consecuencias** — efectos positivos y negativos aceptados.
5. **Impacto en el negocio** — costo, riesgo y tiempo, en lenguaje
   comprensible para un directivo no tecnico.

## Premisa comun: el cliente

Ninguna de estas decisiones es correcta en abstracto. Todas se sostienen sobre
una misma premisa, declarada en el ADR-001:

> **Municipalidad de Guatemala**, la capital. Presupuesto de una institucion
> publica con capacidad real de inversion, pero con equipo de sistemas
> reducido. Trata datos personales de ciudadanos. No puede depender de un
> enlace a internet para detectar incidentes de seguridad.

Un cliente distinto —una municipalidad pequena del interior, por ejemplo—
llevaria a decisiones distintas. Declararlo es parte de lo que el enunciado
pide defender.

## Cobertura de las decisiones obligatorias

| Decision exigida (seccion 4.2 del enunciado) | ADR |
|---|---|
| Plataforma de despliegue | ADR-001 |
| Broker de eventos | ADR-004 |
| Persistencia de eventos | ADR-005 |
| Orquestacion de tareas periodicas | ADR-009 |
| Contenerizacion y orquestacion | ADR-010 |
| Resiliencia y tolerancia a fallos | ADR-006 |
| Seguridad y privacidad de datos | ADR-007 |
| Observabilidad y SLO | ADR-008 |


## Indice

1. [ADR-001: Plataforma de despliegue — híbrido LAN + nube](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)
2. [ADR-002: Lenguaje y runtime de los microservicios — Go](#adr-002-lenguaje-y-runtime-de-los-microservicios-go)
3. [ADR-003: Organización del repositorio — monorepo con módulos independientes](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes)
4. [ADR-004: Broker de eventos — Kafka en modo KRaft](#adr-004-broker-de-eventos-kafka-en-modo-kraft)
5. [ADR-005: Persistencia — PostgreSQL relacional, con Elasticsearch para búsqueda](#adr-005-persistencia-postgresql-relacional-con-elasticsearch-para-búsqueda)
6. [ADR-006: Resiliencia y tolerancia a fallos](#adr-006-resiliencia-y-tolerancia-a-fallos)
7. [ADR-007: Privacidad — seudonimización de placas en la frontera LAN–nube](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube)
8. [ADR-008: Observabilidad y objetivos de nivel de servicio](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio)
9. [ADR-009: Orquestación de tareas periódicas — retirar Airflow](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow)
10. [ADR-010: Contenerización y orquestación — Docker Compose, sin Kubernetes](#adr-010-contenerización-y-orquestación-docker-compose-sin-kubernetes)
11. [ADR-011: Aviso a terceros — servicio propio con registro de despacho](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho)
12. [ADR-012: Recorridos por calle — OSRM en la capa de borde](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)

---

# ADR-001: Plataforma de despliegue — híbrido LAN + nube

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Reemplaza a:** —

---

## 1. Cliente declarado

El enunciado exige declarar explícitamente a qué cliente se atiende, porque la
respuesta correcta depende del caso de negocio y no existe una única respuesta
válida.

**Cliente: Municipalidad de Guatemala**, el municipio de la capital.

Las cuatro características que condicionan toda decisión posterior:

1. **Escala de capital.** Aproximadamente un millón de habitantes en el
   municipio y alrededor de tres millones en el área metropolitana. No es el
   caso de una municipalidad pequeña con un puñado de cámaras.
2. **Presupuesto público real pero con equipo reducido.** Puede invertir en
   hardware, pero no puede permitirse un equipo de plataforma dedicado a
   operar infraestructura compleja las veinticuatro horas.
3. **Trata datos personales de ciudadanos.** Las placas vehiculares y la
   ubicación identifican indirectamente a personas.
4. **Función crítica de seguridad ciudadana.** Un sistema que deja de detectar
   incidentes cuando se cae el enlace a internet no cumple su propósito.

El sistema se organiza por **zonas administrativas de la ciudad**, que es la
misma división con la que la Municipalidad opera sus servicios. La correlación
por zona no es una abstracción del diseño: coincide con la unidad real de
despacho y de responsabilidad operativa.

## 2. Contexto

El sistema procesa eventos de sensores urbanos simulados, los correlaciona en
ventanas de tiempo y emite alertas de seguridad. El compromiso operativo es
que **una alerta se genere en menos de dos segundos desde el evento que la
dispara** (ver ADR-008).

Hay que decidir dónde vive cada componente. La decisión es costosa de revertir:
condiciona el modelo de costos, el perfil de latencia, la superficie de
exposición de datos personales y la carga operativa del equipo del cliente.

### Volumen estimado

La estimación determina el dimensionamiento y, con él, el costo. La fuente
dominante son las cámaras de lectura de placas: un punto de control en un
bulevar de la capital puede registrar del orden de mil a tres mil vehículos por
hora en hora pico.

| Supuesto | Valor |
|---|---|
| Puntos de sensado totales | ~200 |
| Cámaras LPR | ~50 |
| Vehículos por cámara en hora pico | ~1 500/h |
| **Eventos sostenidos** | **~20–25 por segundo** |
| **Picos** | **~60–100 por segundo** |
| Volumen diario | ~1,7 millones de eventos |
| Tamaño medio por evento | ~1 KB |
| Crecimiento de la base | ~2–3 GB/día, ~60–90 GB/mes |

Los picos de 100 eventos por segundo son precisamente los que ejercita la
prueba de carga del proyecto (`./make.sh load rafaga`).

## 3. Opciones consideradas

### Opción A — On-premise / LAN

Toda la plataforma en el centro de datos municipal: broker, base de datos,
correlación, indexación y visualización.

**A favor**

- Control total sobre los datos personales: nada sale de la red municipal.
- Latencia mínima y predecible en todo el recorrido.
- Sin costo variable ni dependencia de un proveedor externo.
- Inmune a cortes del enlace a internet.

**En contra**

- Sin elasticidad ante picos: el hardware se dimensiona para el peor caso y
  queda ocioso el resto del tiempo.
- La capa analítica es la que más recursos consume —Elasticsearch sobre un
  histórico creciente— y obliga a comprar hardware que crece con el archivo
  histórico, no con la operación.
- Toda la responsabilidad operativa recae en un equipo municipal reducido:
  respaldos, parches, recuperación ante desastres, capacidad.
- El costo de inversión inicial es el más alto de las tres opciones.

### Opción B — Cloud-native

Todo en servicios gestionados del proveedor: broker gestionado, base
gestionada, búsqueda gestionada, visualización gestionada.

**A favor**

- Elasticidad real ante picos, sin inversión inicial.
- Carga operativa mínima: respaldos, parches y alta disponibilidad los
  resuelve el proveedor.
- Time-to-market más corto.

**En contra**

- **El camino crítico pasa a depender del enlace a internet.** Si se cae el
  enlace, la ciudad deja de detectar incidentes. Para un sistema de seguridad
  ciudadana esto es inaceptable, y es el argumento que decide.
- Los datos personales de ciudadanos salen íntegros y en claro del control
  municipal.
- Dependencia del proveedor: el broker gestionado es el componente más difícil
  de migrar después.
- Costo variable creciente y menos predecible para un presupuesto público, que
  se planifica por ejercicio anual.

### Opción C — Híbrido

Ingesta y correlación on-premise; persistencia histórica, indexación y
visualización en la nube.

**A favor**

- El razonamiento de detección vive en la LAN: la lógica que convierte eventos
  en alertas no depende del enlace.
- La elasticidad se aplica donde de verdad hace falta: el archivo histórico y
  la analítica, que crecen sin parar y cuyo pico de uso es esporádico.
- La inversión inicial es menor que en on-premise puro, porque no hay que
  comprar hardware para la capa analítica.
- **El punto de corte entre capas puede hacerse coincidir con la frontera de
  privacidad**: lo que cruza hacia la nube se transforma antes de salir.

**En contra**

- Dos entornos que operar y dos modelos de seguridad que mantener.
- Costo de transferencia de datos hacia la nube.
- Mayor complejidad de integración y de diagnóstico: un fallo puede estar en
  cualquiera de las dos capas o en el enlace entre ellas.

## 4. Decisión

**Se adopta el modelo híbrido (opción C).**

El criterio que inclina la balanza es el punto 4 de la premisa del cliente: un
sistema de seguridad ciudadana no puede dejar de detectar incidentes porque se
cayó el enlace a internet. Eso descarta la opción B.

Entre A y C, lo que decide es dónde está la elasticidad que se necesita. El
componente que crece sin límite no es la detección —que es estable y acotada
por el número de sensores— sino el **archivo histórico**: entre sesenta y
noventa gigabytes al mes que hay que conservar, indexar y consultar. Comprar y
renovar hardware para eso es precisamente lo que un equipo municipal reducido
no debería estar haciendo.

### Punto de corte

```
┌──────────────── ON-PREMISE (LAN municipal) ─────────────────┐
│                                                             │
│   Simuladores → Ingestor → Kafka → Correlator               │
│                    │        │  │        │                   │
│                  Redis      │  └── Redis (ventanas)         │
│                (dedupe)     │                               │
│                             ▼                               │
│  Persister · Indexer · Notifier · Jobs ◄── alertas          │
│                             │                               │
│  Prometheus · Grafana · OSRM (observabilidad y rutas)       │
│        ▼ tokenización de la placa antes de salir ▼          │
└─────────────────────────────┬───────────────────────────────┘
                              │  TLS, egress controlado
┌─────────────────────────────▼──────────────── NUBE ─────────┐
│  PostgreSQL gestionada        Elasticsearch → Kibana        │
└─────────────────────────────────────────────────────────────┘
```

El esquemático completo, con los segmentos de red y los puertos, está en
`docs/diagramas/infraestructura.svg`.

**En la LAN:** el ingestor, Kafka, Redis, el correlator, los cuatro servicios de salida (persister, indexer, notifier y jobs), el motor de
rutas OSRM ([ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)) y la observabilidad. Todo el recorrido
evento → alerta.

**En la nube:** PostgreSQL gestionada, Elasticsearch y Kibana. Archivo
histórico y búsqueda, nada más.

**Los servicios de salida son la pieza que hace posible el corte.** Consumen de Kafka y escriben en la nube: el persister y el indexer por
lotes, el notifier y jobs en volúmenes mucho menores. Ni el ingestor ni el correlator escriben
fuera de la LAN, de modo que la detección no depende del enlace a internet.

**La observabilidad también se queda dentro, y no por comodidad.** Recolectar
métricas exige que el recolector **abra conexiones hacia cada servicio**; con
Prometheus en la nube, esas serían conexiones de entrada a la red municipal,
que es justo lo que este modelo excluye. Y si el enlace cae, la detección sigue
funcionando: es precisamente entonces cuando hacen falta las métricas y las
alertas, y un recolector al otro lado del enlace se quedaría ciego en el único
momento en que importa. Grafana lo acompaña porque consulta a Prometheus, y
separarlos reintroduciría el mismo tráfico de entrada.

Situarlo exactamente sobre la frontera tiene una consecuencia afortunada: es
también el lugar natural para transformar los datos personales. La placa
vehicular se convierte en un seudónimo HMAC-SHA256 justo antes de cruzar, y la
llave se custodia on-premise. Los mensajes de Kafka conservan el valor en claro
porque el broker no sale de la red municipal, así que la correlación puede
seguir usándolo. El detalle está en ADR-007.

### Presupuesto de latencia

El SLO es de dos segundos. El desglose estimado del recorrido completo:

| Tramo | Ubicación | Estimado |
|---|---|---|
| Sensor → Ingestor | LAN | < 5 ms |
| Validación del contrato | LAN, en memoria | < 2 ms |
| Deduplicación en Redis | LAN | < 2 ms |
| Publicación en Kafka con `acks=all` | LAN | 5–15 ms |
| Consumo por el correlator | LAN | 10–50 ms |
| Ventana en Redis + evaluación de reglas | LAN | < 10 ms |
| Publicación de la alerta | LAN | 5–15 ms |
| **Total estimado** | | **~30–100 ms** |

**Ningún tramo del recorrido crítico cruza el enlace a internet.** Queda más de
un orden de magnitud de margen sobre el SLO de dos segundos.

### Qué pasa cuando cae el enlace

Esta es la pregunta que decide si la arquitectura cumple su propósito, y por
eso conviene responderla explícitamente.

Si el enlace a internet se cae:

- La ingesta **sigue aceptando eventos**: solo necesita Redis y Kafka, ambos
  en la LAN.
- La correlación **sigue generando alertas**: las ventanas están en Redis y las
  reglas se evalúan en memoria.
- Las alertas **siguen publicándose** en Kafka, donde quedan disponibles para
  cualquier consumidor local.
- Lo único que se detiene es el **archivado** y el registro de los avisos a
  terceros ([ADR-011](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho)). El persister acumula retraso y
  reintenta; los eventos y alertas esperan en Kafka, que los retiene 72 horas
  y 7 días respectivamente.

Cuando el enlace vuelve, el persister recupera el retraso leyendo desde el
último offset confirmado. No se pierde nada, porque los offsets solo avanzan
después de que la base aceptó el lote.

El costo de esta propiedad es un servicio más que operar. Se consideró
aceptable porque la alternativa —escribir en la nube dentro del camino
crítico— hacía que un corte de internet detuviera la detección de incidentes
en una ciudad de tres millones de habitantes.

Una consecuencia secundaria, no buscada pero valiosa: al archivar por lotes en
lugar de fila por fila, el costo de la ida y vuelta contra la base remota se
paga una vez cada cien eventos en lugar de una vez por evento.

## 5. Consecuencias

### Positivas

- La lógica de detección funciona con el enlace degradado.
- La elasticidad se paga solo donde se necesita: el archivo histórico.
- El punto de corte del despliegue coincide con la frontera de privacidad, de
  modo que una sola decisión arquitectónica resuelve dos problemas.
- Los datos personales en claro nunca salen de la red municipal.
- Si la capa nube cae por completo, la Municipalidad sigue detectando y
  alertando incidentes; lo que se pierde temporalmente es la visualización y
  el análisis histórico.

### Negativas aceptadas

- **Dos entornos que operar.** Más superficie de configuración, dos modelos de
  seguridad y diagnósticos más difíciles: un fallo puede estar en cualquiera de
  las dos capas o en el enlace.
- **Un servicio más.** Separar el archivado obligó a introducir el persister,
  que hay que desplegar, monitorear y mantener. Es el precio concreto de que la
  detección no dependa del enlace.
- **El archivo histórico es eventualmente consistente.** Un evento recién
  ingerido puede tardar unos segundos en aparecer en los tableros. Para consulta
  operativa inmediata están los endpoints del correlator, que leen de Redis; los
  tableros sirven para análisis, no para despacho.
- **Costo de transferencia de datos.** Bajo en volumen —eventos JSON pequeños—
  pero distinto de cero y creciente con el número de sensores.
- **Custodia de la llave de tokenización.** Aparece un secreto crítico que hay que rotar, respaldar y proteger, y que
  usan tres servicios (persister, indexer y correlator), de modo que hay tres
  entornos donde custodiarlo ([ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube)). Si se pierde, el histórico queda
  irreversiblemente anonimizado; si se filtra, la seudonimización pierde su
  valor.
- **Dependencia parcial del proveedor** para la base gestionada. Se mitiga
  usando PostgreSQL estándar, sin extensiones propietarias, de modo que migrar
  a otro proveedor o traerla on-premise siga siendo viable.

## 6. Impacto en el negocio

*Esta sección está redactada para lectura de dirección, sin términos técnicos.*

### Qué se decidió, en una frase

El sistema que detecta incidentes funcionará **dentro de las instalaciones de
la Municipalidad**, y solo el archivo histórico y los tableros de consulta
estarán en servicios contratados por internet.

Si se cae el internet, la ciudad **sigue detectando incidentes y generando
alertas**. Lo único que se retrasa es el archivo histórico, que se pone al día
solo cuando el servicio vuelve, sin perder información.

### Por qué conviene

La razón principal no es el costo, es la continuidad del servicio. Si se
contrata todo por internet, el día que falle el enlace la ciudad deja de
detectar incidentes. Con este diseño, la detección sigue operando dentro de las
instalaciones municipales aunque el enlace se caiga por completo.

La segunda razón es la protección de datos de los ciudadanos. Las placas de los
vehículos son datos personales. Con este diseño, **las placas nunca salen de la
red municipal en forma legible**: antes de enviarse al archivo externo se
convierten en un código que no permite identificar el vehículo, y la clave para
descifrarlo se queda en las instalaciones de la Municipalidad.

### Costo aproximado

Cifras de orden de magnitud, en dólares, para comparar alternativas. **No son
una cotización**: deben validarse con proveedores antes de cualquier decisión
de compra.

| Concepto | Inversión inicial | Mensual |
|---|---|---|
| Servidores para detección y archivado (2, con respaldo) | 6 000 – 12 000 | — |
| Energía ininterrumpible, red y gabinete | 2 000 – 4 000 | — |
| Electricidad y mantenimiento | — | 100 – 200 |
| Base de datos contratada | — | 100 – 250 |
| Búsqueda histórica y tableros | — | 150 – 400 |
| Transferencia de datos | — | 10 – 50 |
| **Total** | **8 000 – 16 000** | **360 – 900** |

Comparación con las alternativas descartadas:

| Modelo | Inversión inicial | Mensual | Observación |
|---|---|---|---|
| Todo en instalaciones propias | 20 000 – 35 000 | 200 – 400 | La inversión más alta; exige personal técnico propio |
| **Híbrido (elegido)** | **8 000 – 16 000** | **360 – 900** | Equilibrio entre control y carga operativa |
| Todo contratado por internet | 0 | 800 – 2 000 | Sin inversión, pero la detección depende del enlace |

La inversión inicial se amortiza en un horizonte de cuatro a cinco años. El
modelo híbrido resulta más económico que el totalmente contratado a partir de
aproximadamente el segundo año, y además evita el riesgo de continuidad.

### Riesgos

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| Caída del enlace a internet | Media | **Bajo**: la detección continúa; solo se retrasa el archivo histórico | Resuelto: el archivado corre en un servicio aparte y recupera el retraso al volver el enlace |
| Pérdida de la clave de protección de datos | Baja | Alto: el archivo histórico queda inutilizable para investigación | Respaldo cifrado y custodia con procedimiento documentado |
| Crecimiento del costo mensual con más sensores | Alta | Bajo: el crecimiento es gradual y previsible | Revisión presupuestaria anual |
| Falta de personal técnico para dos entornos | Media | Medio | El diseño reduce a un mínimo lo que se opera en instalaciones propias |

### Tiempo de implementación

La arquitectura está construida y funcionando en entorno de laboratorio. Lo que
resta para un despliegue real en la Municipalidad:

| Actividad | Estimación |
|---|---|
| Instalación y configuración del equipo en sitio | 2 – 3 semanas |
| Contratación y configuración de los servicios externos | 1 semana |
| Pruebas de integración y de carga en sitio | 2 semanas |
| Capacitación del personal municipal | 1 semana |
| **Total** | **6 – 7 semanas** |

---

## Fuentes a citar en el informe

Los datos siguientes se usan como supuestos y deben citarse con su fuente en
formato APA 7 en el informe final:

- Población del municipio y del área metropolitana de Guatemala — Instituto
  Nacional de Estadística de Guatemala.
- División por zonas administrativas de la Ciudad de Guatemala — Municipalidad
  de Guatemala.
- Volúmenes de tránsito por punto de control, para sustentar la estimación de
  eventos por segundo.
- Precios de referencia de los servicios contratados, para sustentar el cuadro
  de costos.

## Referencias

- Enunciado del proyecto final, secciones 3 y 4 — decisión de plataforma y
  formato de los ADR.
- [ADR-007](README.md) — tokenización de placas en la frontera LAN–nube.
- [ADR-008](README.md) — definición y medición del SLO.
- [deploy/README.md](../../deploy/README.md) — materialización del punto de
  corte en los archivos de despliegue.

---

# ADR-002: Lenguaje y runtime de los microservicios — Go

**Estado:** aceptado (razonamiento revisado el 2026-10-28)
**Fecha:** 2026-09-21
**Decide:** el equipo
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (el hardware on-premise acota los recursos disponibles), [ADR-003](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes) (el contrato es un schema, no una biblioteca: un servicio puede reescribirse en otro lenguaje)

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
por segundo ([ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)). Java con Spring Boot,
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
  contenedor ([ADR-010](#adr-010-contenerización-y-orquestación-docker-compose-sin-kubernetes)).
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
en [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos).

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
  heredaba un contexto cancelado ([ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) §3.6).
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
demás ([ADR-003](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes)). Si la Municipalidad
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
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — el volumen estimado y el
  hardware on-premise.
- [ADR-003](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes) — el contrato como schema
  permite reescribir un servicio en otro lenguaje.
- [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) — idempotencia en el
  consumidor como compensación del productor no idempotente.
- [ADR-010](#adr-010-contenerización-y-orquestación-docker-compose-sin-kubernetes) — el binario estático es lo
  que permite la imagen mínima.

---

# ADR-003: Organización del repositorio — monorepo con módulos independientes

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go) (la elección de Go condiciona el mecanismo de módulos)

---

## 1. Contexto

El sistema tiene seis microservicios (ingestor, correlator, persister, indexer,
jobs y notifier) más una herramienta de carga. Hay que decidir cómo se organizan en
control de versiones.

La decisión parece administrativa y no lo es: determina si los servicios pueden
desplegarse de forma independiente, que es la propiedad que distingue una
arquitectura de microservicios de un monolito repartido en carpetas.

### El problema concreto: el contrato compartido

Los servicios hablan dos contratos: el evento canónico v1.0 y la alerta
correlacionada v1.0. En cuanto se comparte
una definición entre servicios aparece la tentación de extraerla a una
biblioteca común, y con ella el riesgo de acoplarlos.

Es la pregunta de fondo de este ADR: **¿cómo se comparte un contrato sin
compartir un destino de despliegue?**

## 2. Opciones consideradas

### Opción A — Monorepo con un solo módulo Go

Un único `go.mod` en la raíz, los servicios en `cmd/` y el código compartido en
`internal/`.

**A favor**

- Lo más simple de arrancar: un `go build ./...` compila todo.
- El contrato vive una sola vez, en un paquete que todos importan. No puede
  haber divergencia.
- Refactorizar es trivial: el compilador verifica todo el sistema a la vez.

**En contra**

- **Los servicios dejan de ser independientes.** Comparten dependencias,
  versión de Go y ciclo de compilación. Actualizar una biblioteca para un
  servicio la actualiza para todos.
- Un cambio en el paquete compartido obliga a recompilar y redesplegar los
  cinco, aunque cuatro no lo necesiten.
- Es un **monolito distribuido**: se paga la complejidad operativa de tener
  cinco procesos sin obtener la autonomía que los justifica.

### Opción B — Monorepo con un módulo Go por servicio

Un repositorio, pero cada servicio con su propio `go.mod`, sus dependencias y
su ciclo de compilación.

**A favor**

- Cada servicio se compila, versiona y despliega por separado.
- Las dependencias son por servicio: el correlator no arrastra el cliente de
  Elasticsearch, y el indexer no arrastra el de Redis.
- Un solo `git clone` para el equipo, y los cambios que tocan varios servicios
  caben en un commit atómico.
- La configuración de integración continua es una sola.

**En contra**

- Compartir código entre módulos exige publicarlo o usar directivas `replace`,
  que ensucian los `go.mod`.
- Hay tentación permanente de extraer código común, y ceder reintroduce el
  acoplamiento de la opción A por la puerta de atrás.
- Tocar el contrato implica editar varios sitios.

### Opción C — Un repositorio por servicio

**A favor**

- Autonomía máxima: cada servicio tiene su historial, sus permisos y su
  cadencia de publicación.
- El acoplamiento accidental es imposible, porque no hay nada a mano que
  compartir.

**En contra**

- **Un cambio de contrato exige coordinar cinco repositorios.** En un equipo
  pequeño y con fecha de entrega fija, ese costo se paga en cada iteración.
- El contrato hay que publicarlo como artefacto versionado, con su propio
  ciclo de publicación.
- Integración continua duplicada cinco veces.
- Reproducir el sistema completo para una demostración exige clonar cinco
  repositorios en las versiones correctas.

## 3. Decisión

**Se adopta la opción B: un repositorio, un módulo Go por servicio.**

```
Ciudad_Inteligente_GO/
├── contracts/                   fuente de verdad de los contratos
│   ├── canonical-event/v1.0/
│   └── correlated-alert/v1.0/
├── services/
│   ├── ingestor/    go.mod → ciudadinteligente/ingestor
│   ├── correlator/  go.mod → ciudadinteligente/correlator
│   ├── persister/   go.mod → ciudadinteligente/persister
│   ├── indexer/     go.mod → ciudadinteligente/indexer
│   ├── jobs/        go.mod → ciudadinteligente/jobs
│   └── notifier/    go.mod → ciudadinteligente/notifier
├── deploy/
├── docs/adr/
└── test/load/       go.mod propio
```

### 3.1 El contrato es el schema, no una biblioteca

Esta es la decisión que resuelve la tensión del contexto.

**No hay módulo Go compartido.** El contrato es el **JSON Schema**, que vive una
sola vez en `contracts/`. Cada servicio:

- embebe su copia con `go:embed`;
- define sus propios structs, con solo los campos que ese servicio necesita;
- puede migrar a una versión nueva del contrato sin esperar a los demás.

Que los structs sean propios de cada servicio no es duplicación por descuido:
el ingestor necesita el evento completo para validarlo y publicarlo, mientras
que el correlator solo lee los campos que sus reglas consultan. Obligarlos a
compartir una estructura los ataría a las necesidades del otro.

El precio es el riesgo de que las copias diverjan, y se paga con una
comprobación automática:

```
make check-contracts
```

Compara cada copia con la fuente de verdad y falla si difieren. Corre dentro de
`make check`, de modo que nadie puede olvidarla.

### 3.2 Por qué monorepo y no seis repositorios

Lo que decide es el **costo de cambiar el contrato**.

En polyrepo, subir el evento canónico a la versión 1.1 significa: publicar el
contrato nuevo, abrir seis pull requests, coordinar su orden de integración y
verificar que ninguna combinación intermedia rompa el sistema. En monorepo es
un commit que toca la fuente y sus ocho copias, y que la comprobación de contratos
valida de una vez.

Con un equipo de estudiantes y una fecha fija, esa diferencia es lo que decide.

### 3.3 La decisión es reversible

Importa dejarlo dicho porque es lo que permite elegir el monorepo sin
comprometerse para siempre.

Como **no hay código compartido entre servicios**, cada carpeta `services/X/` se
puede extraer a su propio repositorio con `git subtree split` conservando su
historial, y seguirá compilando sin tocar una línea. Lo único que haría falta
añadir es la sincronización del contrato, que hoy resuelve el `make`.

La propiedad que importa —independencia real— ya está garantizada por los
módulos separados. El monorepo es solo dónde viven hoy.

## 4. Consecuencias

### Positivas

- Los seis servicios se compilan y despliegan por separado, verificado en el
  despliegue real.
- Dependencias mínimas por servicio, lo que reduce la superficie que auditar.
- Un cambio de contrato es un commit atómico y verificado.
- Un solo `git clone` reproduce el sistema completo.
- Pasar a un repositorio por servicio sigue siendo posible sin tocar código.

### Negativas aceptadas

- **El contrato está copiado en ocho sitios**: cuatro copias del evento (ingestor,
  correlator, persister e indexer) y cuatro de la alerta (correlator, persister,
  indexer y notifier). La comprobación automática impide la divergencia, pero sigue
  siendo duplicación que hay que sincronizar al cambiarlo. Las listas de quién recibe
  cada contrato están en el `Makefile` y la comprobación se deriva de ellas: antes
  estaban escritas copia por copia y `check-contracts` solo verificaba 3 de 7.
- **Hay código repetido entre servicios**: el arranque, la carga de
  configuración, el registro estructurado y el bucle de lotes se parecen mucho
  entre el persister y el indexer. Es deliberado —extraerlo los acoplaría— pero
  significa que una mejora en uno no llega sola a los demás.
- **La independencia hay que sostenerla a propósito.** Nada impide técnicamente
  que alguien añada un `replace ../otro-servicio` y rompa la propiedad. Es una
  disciplina, no una garantía del compilador.
- **El repositorio crece.** Seis módulos significan seis `go.sum` y seis árboles de dependencias que actualizar por separado.

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Que los seis programas del sistema vivan **en un mismo archivo de proyecto,
pero como piezas independientes**: se pueden actualizar y reemplazar uno por uno,
sin tocar los demás.

### Por qué importa

Si los programas estuvieran fundidos en uno solo, cualquier corrección —por
pequeña que fuera— obligaría a reinstalar el sistema completo, con el riesgo y
la ventana de interrupción que eso supone. Con esta organización se puede
corregir el componente que archiva la información sin detener el que detecta
incidentes.

La otra opción era el extremo contrario: cinco proyectos separados. Se descartó
porque cualquier cambio que afecte a varios obligaría a coordinar cinco
actualizaciones distintas, y el equipo que mantendrá el sistema es reducido.

### Qué exige

Una disciplina de trabajo: mantener los programas verdaderamente separados
aunque convivan en el mismo proyecto. Hay una comprobación automática que
detecta si alguien rompe esa separación en la parte más delicada —la definición
común de los datos—, pero el resto depende del criterio de quien programe.

### Costo

Ninguno adicional. Es una decisión de organización, no de infraestructura, y no
añade licencias ni equipos.

---

## Referencias

- Enunciado del proyecto final, sección 4 — toda decisión relevante se registra
  como ADR.
- [ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go) — la elección de Go determina que la
  unidad de independencia sea el módulo.
- [contracts/README.md](../../contracts/README.md) — por qué el contrato es un
  schema y no una biblioteca.
- `make.sh check-contracts` — la comprobación que impide la divergencia.

---

# ADR-004: Broker de eventos — Kafka en modo KRaft

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (el broker vive en la LAN), [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) (garantías de entrega)

---

## 1. Contexto

El broker es la columna vertebral del sistema. Todo evento pasa por él, y las
decisiones de [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) lo convierten en algo
más: al sacar la persistencia del camino crítico, **el broker es la fuente de
verdad del transporte**. Si un evento está en el broker, llegará a su destino;
si no está, se perdió.

Eso le impone tres requisitos:

1. **Retención suficiente** para que un consumidor caído se ponga al día al
   volver. El persister recupera su retraso leyendo desde el último offset
   confirmado, y eso solo funciona si los mensajes siguen ahí.
2. **Particionado por clave**, para que los eventos de una misma zona conserven
   su orden relativo. La correlación por ventana depende de ello.
3. **Varios consumidores independientes** sobre el mismo flujo: correlator, persister e indexer leen `events.standardized` sin interferirse, y
   persister, indexer y notifier hacen lo mismo con `correlated.alerts`.

El enunciado lo plantea como decisión a documentar (sección 4.2): Kafka frente a
RabbitMQ frente a un servicio gestionado, con el trade-off de rendimiento,
complejidad operativa y costo.

## 2. Opciones consideradas

### Opción A — RabbitMQ

**A favor**

- Considerablemente más simple de operar: un proceso, sin coordinación de
  particiones ni gestión de offsets.
- Menor consumo de recursos, lo que pesa sobre el hardware propio y acotado del
  [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube).
- Enrutamiento flexible por intercambios y claves de enlace.

**En contra**

- **El mensaje desaparece al consumirse.** Para que tres servicios lean el mismo
  evento hay que duplicarlo en tres colas, y cada una gestiona su propio
  ciclo de vida.
- **No hay reproducción del histórico.** Un consumidor que estuvo caído no puede
  volver atrás: lo que no consumió, se perdió. Eso rompe directamente la
  recuperación del persister, que es una propiedad verificada del sistema.
- Sin orden garantizado por clave cuando hay varios consumidores sobre una cola.

### Opción B — Servicio gestionado (MSK, Confluent Cloud, Pub/Sub)

**A favor**

- Sin carga operativa: respaldos, parches y alta disponibilidad los resuelve el
  proveedor.
- Elasticidad real.

**En contra**

- **Contradice [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube).** El broker es el
  componente central del camino crítico; ponerlo en la nube haría que un corte
  de internet detuviera la detección de incidentes. Es exactamente lo que esa
  decisión descarta.
- Es además el componente más difícil de migrar después: la dependencia del
  proveedor se vuelve estructural.
- Costo mensual significativo —del orden de cientos de dólares— para un volumen
  que un solo broker local absorbe sin esfuerzo.

### Opción C — Kafka con Zookeeper

La configuración clásica de Kafka, la que aparece en la mayoría de los
tutoriales.

**A favor**

- Configuración muy documentada y conocida.
- Es lo que el equipo ya había desplegado y sabía operar.

**En contra**

- **Dos componentes en lugar de uno.** Zookeeper tiene su propio ciclo de vida,
  su propia configuración y sus propios modos de fallo, y hay que vigilarlo
  aparte.
- **Está deprecado.** Kafka lo marcó como obsoleto en la serie 3.x y lo eliminó
  en 4.0. Adoptarlo hoy sería heredar deuda técnica desde el primer día del
  sistema nuevo.

### Opción D — Kafka en modo KRaft

Kafka gestionando su propio quórum de metadatos, sin Zookeeper.

**A favor**

- **Un componente menos que operar y monitorear**, lo que pesa cuando el equipo
  de sistemas del cliente es reducido.
- Es la dirección en la que va el proyecto: no hay deuda que pagar después.
- Arranque más rápido y menos memoria que el par Kafka + Zookeeper.
- Conserva todas las propiedades que el sistema necesita: retención,
  particionado por clave y grupos de consumo independientes.

**En contra**

- Menos material de referencia acumulado que la configuración con Zookeeper.
- La configuración inicial tiene detalles propios, y equivocarse impide
  arrancar.

## 3. Decisión

**Se adopta Kafka en modo KRaft (opción D).**

### 3.1 Por qué Kafka y no RabbitMQ

Lo que decide no es el rendimiento —el volumen estimado, de veinte a cien
eventos por segundo, lo absorbe cualquiera de los dos— sino **la reproducción
del histórico**.

Tras [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube), el persister y el indexer
consumen de forma asíncrona. Que puedan recuperar su retraso tras una caída es
una propiedad central del diseño, y se verificó: al detener el persister durante
una tanda de eventos y volverlo a levantar, recuperó exactamente los once que
faltaban leyendo desde su último offset confirmado.

Con RabbitMQ esa recuperación no existe. Habría que construirla a mano —una
tabla de pendientes, un reintentador— reimplementando mal lo que Kafka da hecho.

El segundo argumento es la **lectura múltiple**: tres servicios leen el mismo
flujo con grupos de consumo distintos, cada uno a su ritmo. En RabbitMQ serían
tres colas alimentadas por un intercambio, con tres ciclos de vida que
sincronizar.

### 3.2 Configuración

| Parámetro | Valor | Por qué |
|---|---|---|
| Particiones por topic | 3 | Permite paralelismo sin fragmentar en exceso a escala de laboratorio |
| Factor de replicación | 1 | Un solo broker; en producción debería subirse a 3 |
| Clave de partición | `geo.zone` | Los eventos de una zona conservan su orden relativo |
| `events.standardized` | 72 h | Margen suficiente para recuperar una caída larga del enlace |
| `correlated.alerts` | 7 d | Las alertas alimentan análisis posteriores |
| `events.dlq` | 14 d | Su función es que alguien pueda revisarla y reprocesar |
| `acks` | `all` | El `202` al cliente significa que Kafka confirmó |
| Primera lectura de un grupo nuevo | persister e indexer desde el principio; correlator y notifier desde lo último | Archivar de más es inocuo; reprocesar alertas viejas no (ADR-011 §3.4) |
| Creación automática de topics | desactivada | Un nombre mal escrito debe fallar, no crear un topic fantasma |

El factor de replicación de 1 merece una nota: **es una limitación consciente
del laboratorio**. Con un solo broker, su caída detiene el sistema. En el
despliegue real sobre los dos servidores del
[ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) debería subirse a 3 con un mínimo de
réplicas sincronizadas de 2.

### 3.3 La clave de partición es la zona

La alternativa evidente es usar `event_type`, y produce dos problemas a la
vez: solo hay cinco valores posibles para tres particiones —reparto
desbalanceado— y los eventos de una misma zona quedan dispersos entre
particiones, perdiendo el orden relativo del que depende la correlación por
ventana.

Usar `geo.zone` corrige ambos. Es además lo que prescribe la guía del curso
(sección 4, "afinidad por zona").

### 3.4 El cliente y el productor idempotente

Se usa `segmentio/kafka-go`, en Go puro, para conservar el binario estático de Go y sus imágenes mínimas, una de las
ventajas del lenguaje elegido por el equipo ([ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go)).

La contrapartida es que **no ofrece productor idempotente completo**, que la
guía recomienda. Se compensa con idempotencia en el consumidor —`SET NX` sobre
el identificador de evento, `ON CONFLICT DO NOTHING` al archivar—, que protege
además contra reprocesos por rebalanceo, no solo contra reintentos de escritura.
El detalle está en [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos).

Hay un segundo detalle que no es el valor por defecto y que importa: kafka-go
reparte por turno rotatorio si no se indica otra cosa, **ignorando por completo
la clave del mensaje**. Con esa configuración, el particionado por zona no
existiría. Se usa `Murmur2Balancer`, que es además el algoritmo estándar de
particionado de Kafka: cualquier otro productor que escriba en el mismo topic
reparte las claves igual.

## 4. Consecuencias

### Positivas

- Un componente menos que operar frente a Kafka con Zookeeper.
- Sin deuda técnica heredada: Zookeeper habría que retirarlo más adelante.
- La reproducción del histórico permite que un consumidor caído se recupere
  solo, verificado en ejecución.
- Tres consumidores independientes sobre el mismo flujo, sin duplicar nada.
- El broker vive en la LAN: su disponibilidad depende de la Municipalidad.

### Negativas aceptadas

- **Kafka sigue siendo más pesado que RabbitMQ.** Para el volumen real del
  sistema, buena parte de su capacidad queda sin usar.
- **Menos material de referencia** para KRaft. Se comprobó en la práctica: el
  identificador de clúster debe ser un UUID de dieciséis bytes en base64, y un
  nombre legible impide arrancar con un error poco evidente.
- **Factor de replicación 1 en laboratorio**: un solo broker es un punto único
  de fallo, y subirlo en producción es trabajo pendiente.
- **`kafka-go` no detecta un topic creado después de unirse al grupo.** Un
  consumidor que arranca antes de que exista su topic queda con cero particiones
  asignadas, sin error ni aviso, y el grupo figura como estable. Se vio en un Kafka
  recién creado: el persister y el notifier no consumían las alertas porque
  arrancaron antes que `kafka-init`, y las alertas no se archivaban ni se
  notificaban. Se resolvió haciendo que cinco servicios esperen a `kafka-init`
  (`service_completed_successfully`).
- **Conceptos que el equipo debe dominar**: offsets, grupos de consumo,
  rebalanceo y retención. Es más superficie de aprendizaje que una cola simple.

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Usar **Apache Kafka** como sistema de transporte de los eventos, instalado en
las instalaciones de la Municipalidad, en su configuración moderna que no
requiere un segundo programa auxiliar.

### Por qué

La razón principal es que Kafka **conserva los eventos durante días** después de
entregarlos. Si uno de los componentes se detiene —por mantenimiento, por un
fallo o porque se cayó el internet— al volver recupera todo lo que se perdió, en
orden y sin intervención. Lo verificamos apagando deliberadamente uno de los
componentes: al reactivarlo recuperó por sí solo todo lo pendiente.

La alternativa más ligera que se evaluó, RabbitMQ, entrega los mensajes y los
descarta. Con ella, lo que no se recogiera a tiempo se perdería, y habría que
construir a mano un mecanismo de recuperación.

También se descartó contratarlo por internet: el transporte de eventos es el
corazón del sistema, y ponerlo fuera haría que un corte de conexión detuviera la
detección de incidentes.

### Qué costó

Kafka es más exigente en recursos que alternativas más simples, y su operación
requiere conocimientos específicos. Se eligió la variante que elimina un
programa auxiliar, reduciendo en uno los componentes a vigilar.

### Pendiente para producción

En el laboratorio funciona **un solo servidor de transporte**, lo que significa
que su caída detiene el sistema. Para el despliegue real debe configurarse con
tres copias sincronizadas, de modo que la caída de una no interrumpa el
servicio. Está previsto sobre los servidores contemplados en el presupuesto y
**no supone costo adicional de licencias**.

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — broker de eventos.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — el broker vive en la LAN.
- [ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go) — la elección de cliente y el binario
  estático.
- [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) — garantías de entrega e
  idempotencia en el consumidor.
- `deploy/docker-compose.edge.yml` — configuración de KRaft.
- `deploy/scripts/create-topics.sh` — particiones y retención por topic.

---

# ADR-005: Persistencia — PostgreSQL relacional, con Elasticsearch para búsqueda

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (la base vive en la nube), [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) (retención), [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow) (agregados e indexación)

---

## 1. Contexto

El enunciado plantea la decisión (sección 4.2): base relacional frente a base de
series de tiempo —TimescaleDB, InfluxDB— frente a NoSQL, **justificando frente
al volumen y la naturaleza temporal de los datos**.

Esa última frase es la que hace la pregunta interesante, porque los datos del
sistema *parecen* series de tiempo: eventos con marca de tiempo que llegan de
forma continua y se consultan por rango temporal. El caso para una base
especializada es aparentemente fuerte.

### Qué se guarda y cómo se consulta

| Dato | Volumen | Cómo se consulta |
|---|---|---|
| `events` | ~1,7 M/día, 2–3 GB/día | Por rango de tiempo, zona y tipo; por `event_id` al investigar |
| `alerts` | decenas al día | Por rango, zona y tipo; con su evidencia |
| Agregados diarios | cientos de filas/día | Por rango de fechas |
| `notifications` | de una a tres filas por alerta | Por alerta y por organismo ([ADR-011](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho)) |
| `alert_routes` | una fila por alerta | Por alerta; geometría del recorrido ([ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)) |

Dos observaciones que condicionan la decisión:

**Las alertas no son una serie de tiempo.** Son decenas al día, se consultan de
forma individual, llevan una lista de evidencias y alimentan investigaciones.
Es un caso relacional clásico.

**El acceso a los eventos cambió con [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow).**
La búsqueda exploratoria —filtrar por texto, por proximidad geográfica, dibujar
mapas de calor— la resuelve Elasticsearch, que se alimenta directamente de
Kafka. La base relacional quedó como **archivo autoritativo** y respaldo de los
agregados, no como motor de consulta interactiva.

## 2. Opciones consideradas

### Opción A — PostgreSQL relacional

**A favor**

- Un solo motor para eventos, alertas y agregados, con integridad referencial y
  transacciones.
- `jsonb` almacena el payload heterogéneo de los cinco tipos de sensor sin
  forzar un esquema común ni una tabla por tipo.
- Universalmente conocido: cualquier persona que mantenga el sistema después
  sabrá operarlo, lo que importa con un equipo municipal reducido.
- Disponible como servicio gestionado en cualquier proveedor, sin dependencia
  estructural.
- Grafana lo consulta de forma nativa.

**En contra**

- No está optimizado para series de tiempo: sin particionado manual, una tabla
  que crece indefinidamente degrada las consultas por rango.
- Compresión limitada frente a motores especializados.
- Las agregaciones sobre millones de filas son más lentas.

### Opción B — TimescaleDB

Extensión de PostgreSQL orientada a series de tiempo.

**A favor**

- Particionado automático por tiempo: las consultas por rango reciente no tocan
  el histórico completo.
- Compresión de hasta un orden de magnitud sobre datos antiguos.
- Agregados continuos que se mantienen solos, lo que haría innecesaria la tarea
  nocturna del [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow).
- Es PostgreSQL: mismo lenguaje, mismos clientes, misma curva de aprendizaje.

**En contra**

- **Es una extensión, y no todos los proveedores gestionados la permiten.** Eso
  ata la decisión a un proveedor concreto o a autogestionar la base, que es
  justo la carga operativa que [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) quiso
  evitar.
- Su ventaja principal —consultas analíticas rápidas sobre grandes volúmenes—
  se solapa con lo que ya resuelve Elasticsearch.
- Un concepto más que el equipo debe entender para operar correctamente.

### Opción C — InfluxDB

**A favor**

- Diseñada específicamente para métricas y series temporales.
- Compresión y retención automática por política.

**En contra**

- **Modela mal las alertas.** Una alerta con su lista de evidencias, su
  ventana y su puntuación es un registro relacional, no un punto de medición.
  Guardarlas ahí obligaría a tener dos bases de todos modos.
- Lenguaje de consulta propio: nada de lo que el equipo ya sabe se reutiliza.
- Integración con Grafana peor que la de PostgreSQL para tablas de detalle.

### Opción D — NoSQL documental (MongoDB)

**A favor**

- El payload heterogéneo encaja de forma natural.
- Escalado horizontal sencillo.

**En contra**

- `jsonb` de PostgreSQL ya resuelve la heterogeneidad **sin renunciar a las
  transacciones ni a la integridad**.
- Las consultas analíticas por rango y agrupación son más incómodas.
- Añade un motor que el equipo no conoce, sin resolver ningún problema que
  PostgreSQL no resuelva.

## 3. Decisión

**Se adopta PostgreSQL como archivo autoritativo**, complementado con
Elasticsearch para búsqueda exploratoria y análisis geoespacial.

### 3.1 Por qué no una base de series de tiempo

La respuesta corta: **porque la parte que de verdad se beneficiaría ya la
resuelve otro componente.**

TimescaleDB e InfluxDB destacan en consultas analíticas sobre grandes volúmenes
temporales. Pero tras [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow) esas
consultas las atiende Elasticsearch, que además aporta búsqueda por texto y por
proximidad geográfica —el mapa de calor por zona— que ninguna base de series de
tiempo ofrece.

Lo que queda para la base relacional es: ser el registro autoritativo, permitir
buscar un evento concreto por su identificador durante una investigación, y
sostener los agregados. Todo ello es trabajo relacional ordinario.

Adoptar TimescaleDB habría supuesto atar la decisión a un proveedor que permita
la extensión, a cambio de una ventaja que se solapa con lo que ya existe.

### 3.2 Dos motores, dos propósitos

```
Kafka ──▶ Persister ──▶ PostgreSQL     archivo autoritativo
      └──▶ Indexer   ──▶ Elasticsearch búsqueda y análisis
```

Un tercer escritor, el notifier, registra los avisos en PostgreSQL, y `jobs`
escribe los agregados y los recorridos. Ninguno de los dos toca el camino de los
eventos.

Ambos consumen del broker de forma independiente
([ADR-004](#adr-004-broker-de-eventos-kafka-en-modo-kraft)). Esa separación tiene una consecuencia
operativa valiosa: **si Elasticsearch se cae, el archivo sigue completo**, y al
volver se reconstruye desde Kafka sin tocar la base. La alternativa —copiar de
PostgreSQL a Elasticsearch por lotes cada cierto tiempo— ataría los dos
destinos entre sí: un fallo en el índice obligaría a releer la base, y un fallo
en la base dejaría el índice sin fuente.

La división de responsabilidades:

| | PostgreSQL | Elasticsearch |
|---|---|---|
| Papel | Registro autoritativo | Índice de consulta |
| Consultas | Por identificador, agregados | Texto, geoespacial, exploración |
| Si se pierde | **Pérdida real** | Se reconstruye desde Kafka |
| Retención | Degradación progresiva | Borrado de índices mensuales |

### 3.3 El payload como `jsonb`

Los cinco tipos de sensor llevan campos distintos. Las alternativas eran una
tabla por tipo —cinco tablas, cinco caminos de consulta, y una migración cada
vez que apareciera un sensor nuevo— o una tabla con columnas para la unión de
todos los campos, mayormente nulas.

`jsonb` evita ambas. El evento se archiva tal como llegó, y los campos comunes
—tipo, zona, marca de tiempo, severidad— se extraen a columnas propias, que son
las que llevan índice y por las que se filtra de verdad.

Permite además que la política de retención opere sobre el contenido:
`payload - 'placa_vehicular'` elimina un campo personal sin reescribir el resto
del documento.

### 3.4 Dos decisiones de esquema que parecen menores

**`score` como `numeric` con restricción de rango.** Declararlo entero parece
inocuo y no lo es: al redondear, 0.85, 0.90 y 0.95 acabarían los tres como `1`
y toda la gradación de confianza entre reglas se perdería al persistir. Se
añadió además `CHECK (score >= 0 AND score <= 1)`: un valor fuera de rango
indica un error de cálculo, y vale más rechazar la fila que dejar que contamine
los tableros.

**Índices compuestos por zona, tipo y tiempo.** Los tableros agrupan
exactamente por esa combinación; con índices sueltos, cada refresco recorría la
tabla completa.

### 3.5 Escritura por lotes

El persister agrupa hasta cien eventos por escritura. Es la ventaja concreta de
haber sacado la persistencia del camino crítico
([ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)): el costo de la ida y vuelta contra
una base remota se paga una vez cada cien eventos en lugar de una vez por
evento.

La escritura de un lote de cien tardó decenas de milisegundos en las pruebas.

## 4. Consecuencias

### Positivas

- Un solo motor relacional que el equipo ya conoce y cualquier proveedor ofrece
  gestionado.
- `jsonb` admite sensores nuevos sin migrar el esquema.
- La búsqueda exploratoria no carga la base autoritativa.
- Si Elasticsearch se pierde, se reconstruye desde Kafka sin pérdida de datos.
- Transacciones e integridad disponibles cuando hagan falta.

### Negativas aceptadas

- **Sin particionado por tiempo.** La tabla `events` crece hasta que la
  retención la purga. Con el volumen estimado —entre sesenta y noventa
  gigabytes al mes— llegará a decenas de millones de filas antes del primer
  borrado a los dos años, y las consultas por rango se degradarán. El
  particionado nativo de PostgreSQL por mes es la solución, y **está
  pendiente**.
- **El esquema ya no se aplica solo con el volumen vacío.** El directorio de
  inicialización de PostgreSQL solo corre la primera vez, de modo que añadir una
  tabla obligaba a borrar los datos. Un servicio de un solo uso, `postgres-migrate`,
  aplica `deploy/sql/*.sql` en cada arranque; todo lleva `IF NOT EXISTS`. La base de
  producción es gestionada y no está en ningún Compose: ahí el paso es manual y
  está en `deploy/README.md`.
- **Compresión limitada** frente a un motor especializado: más costo de
  almacenamiento en la nube.
- **Dos motores que operar**, con sus dos modelos de retención.
- **Duplicación de los datos** en ambos destinos, y por tanto doble costo de
  almacenamiento.

### Pendiente

| Trabajo | Motivo |
|---|---|
| Particionado de `events` por mes | Evitar la degradación de consultas sobre decenas de millones de filas |
| Subir la réplica de la base gestionada | Hoy depende de la disponibilidad que ofrezca el proveedor |

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Guardar la información en **dos sistemas con propósitos distintos**: una base de
datos tradicional que funciona como archivo oficial, y un buscador especializado
para consultas y mapas.

### Por qué no una base especializada

Se evaluaron bases diseñadas específicamente para datos con fecha y hora, que
son más eficientes en ese tipo de información. Se descartaron por dos razones:

La primera es que **su ventaja ya la aporta el buscador**. Las consultas
exigentes —filtrar por texto, buscar por cercanía, dibujar mapas de calor— las
resuelve ese componente, y además hace cosas que las bases especializadas no
hacen.

La segunda es de dependencia: esas bases requieren extensiones que no todos los
proveedores permiten, lo que ataría a la Municipalidad a un proveedor concreto.
La base elegida la ofrece cualquiera, de modo que cambiar de proveedor sigue
siendo posible.

### Una ventaja de tenerlos separados

Si el buscador falla, **el archivo oficial permanece intacto** y el buscador se
reconstruye solo a partir del sistema de transporte. Si el buscador se
alimentara copiando de la base de datos cada cierto tiempo —que es la forma
habitual— el funcionamiento de uno quedaría atado al del otro.

### Qué costó

Mantener dos sistemas significa pagar dos almacenamientos y operar dos
componentes. Es el precio de que una consulta pesada no afecte al archivo
oficial.

### Pendiente

La tabla principal **crece sin dividirse internamente**. Con el volumen previsto
—entre sesenta y noventa gigabytes al mes— las consultas irán perdiendo
velocidad con los meses. La solución es una configuración de la propia base que
la divide por periodos; **está identificada y pendiente**, y no supone costo
adicional, solo trabajo técnico.

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — persistencia de eventos.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — la base gestionada vive en la
  nube y no está en el camino crítico.
- [ADR-004](#adr-004-broker-de-eventos-kafka-en-modo-kraft) — ambos destinos consumen del broker de
  forma independiente.
- [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) — la retención opera sobre el
  contenido del payload.
- [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow) — indexación en tiempo
  casi real y agregados diarios.
- `deploy/sql/` (001 a 004) — esquema, índices, restricciones, avisos y recorridos.

---

# ADR-006: Resiliencia y tolerancia a fallos

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (plataforma híbrida)

---

## 1. Contexto

El sistema es una cadena de cinco eslabones —ingestor, Kafka, correlator,
Redis, persister— y cualquiera de ellos puede fallar. Hay que decidir
**explícitamente qué ocurre con un evento cuando algo falla**, porque la
alternativa no es que no pase nada: es que pase algo sin que nadie lo decida.

El modo de fallo por omisión es fácil de escribir y difícil de detectar:
envolver todo el procesamiento en un manejador que registre el error y siga,
con la confirmación de offsets en automático. El offset avanza igual, y
**un evento que falle desaparece sin dejar rastro**: no queda en ninguna cola,
no hay métrica que lo cuente y el productor ya recibió un `202`. Nadie puede
saber después cuántos eventos se perdieron ni cuáles.

Ese es el comportamiento que esta decisión existe para evitar.

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
| Consumidor que se une antes de que exista su topic | Alta en un arranque en frío | No consume, sin error ni aviso |
| Motor de rutas no disponible | Media | Alertas sin recorrido |

## 2. Opciones consideradas

### Opción A — Descartar y continuar

Capturar el error, registrarlo en consola y seguir con el siguiente mensaje.
Es lo que ocurre por omisión si nadie decide otra cosa.

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
  [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) decidió evitar para no depender
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
transitorio —reintenta más tarde. Responder `400` a todo dejaría al cliente sin
forma de distinguir un caso del otro, y reintentando lo que nunca va a
funcionar o descartando lo que sí.

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
| Notifier | `UNIQUE (alert_id, organismo)` y `ON CONFLICT DO NOTHING` | Protege el registro, no la entrega: ver [ADR-011](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho) §3.3 |

La forma intuitiva —comprobar si la clave existe y después escribirla— deja
entre ambas llamadas un hueco por el que cabe otra instancia procesando el
mismo evento: la idempotencia se rompería justo al escalar horizontalmente, que
es cuando más hace falta.

Esta propiedad es la que permite que el persister arranque por defecto desde el
offset más antiguo: reprocesar un evento ya archivado solo suma un duplicado a
las métricas, mientras que no archivarlo deja un hueco permanente en el
histórico. Ante la duda, prefiere releer de más.

### 3.5 Publicación confirmada

El productor usa `acks=all` de forma **síncrona**: el `202` al cliente
significa que Kafka confirmó la recepción en todas las réplicas. Publicar de
forma asíncrona y responder `202` sin esperar sería más rápido y mentiroso: si
la publicación fallara, el cliente ya se habría llevado un "aceptado" por un
evento que no llegaría nunca al correlator.

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

### 3.7 Un consumidor no debe unirse antes de que exista su topic

Se descubrió al arrancar un Kafka recién creado. `kafka-go` no se entera de que
un topic aparece después de que el consumidor se unió al grupo: este queda
«estable» con cero particiones asignadas y no consume nada, sin error y sin
métrica que lo delate. Persister y notifier no consumían las alertas, que por eso
no se archivaban ni se notificaban.

La corrección es de orden de arranque: persister, indexer, notifier, correlator e
ingestor esperan a que `kafka-init` termine (`service_completed_successfully`).
Que el síntoma sea silencioso es el motivo de que figure aquí: es de la misma
familia que el resto de esta decisión, un fallo que no se manifiesta solo.

### 3.8 Matriz de comportamiento ante fallos

| Componente caído | Ingesta | Detección | Archivo | Respuesta al cliente |
|---|---|---|---|---|
| Persister | Continúa | Continúa | Se detiene y acumula | `202` |
| PostgreSQL | Continúa | Continúa | Se detiene y acumula | `202` |
| Enlace a la nube | Continúa | Continúa | Se detiene y acumula | `202` |
| Elasticsearch / Grafana | Continúa | Continúa | Continúa | `202` |
| Notifier | Continúa | Continúa | Continúa; los avisos esperan en Kafka | `202` |
| Motor de rutas (OSRM) | Continúa | Continúa | Continúa; los recorridos se reintentan hasta 3 veces | `202` |
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
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — la plataforma híbrida es la que
  descarta el outbox transaccional.
- [deploy/README.md](../../deploy/README.md) — procedimiento para reproducir la
  prueba de caída y recuperación.
- `services/*/internal/broker/kafka.go` — confirmación manual de offsets y
  publicación en la DLQ.
- `services/ingestor/internal/ingest/service.go` — clasificación de fallos y
  contexto de limpieza.

---

# ADR-007: Privacidad — seudonimización de placas en la frontera LAN–nube

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (el punto de corte del despliegue es la frontera de privacidad)

---

## 1. Contexto

El sistema procesa datos que identifican indirectamente a personas. El
enunciado lo señala de forma explícita (secciones 4.2 y 9) y añade una
precisión que conviene no pasar por alto: **la obligación aplica aunque los
datos sean sintéticos**, porque forma parte del ejercicio de defensa ante el
área legal.

### Qué datos personales circulan

| Dato | De dónde viene | Por qué es personal |
|---|---|---|
| `placa_vehicular` | `sensor.lpr` | Identifica un vehículo y, por registro, a su propietario |
| `geo.lat` / `geo.lon` | Todos los eventos | Ubica a una persona en un momento concreto |
| `geo.zone` | Todos los eventos | Ubicación de menor precisión |
| `timestamp` | Todos los eventos | Combinado con lo anterior, construye un rastro de movimiento |
| `identificador_dispositivo` | `panic.button` | Puede asociarse a una persona o a un domicilio |

El elemento crítico no es ninguno por separado, sino su **combinación**: placa
más coordenadas más marca de tiempo, repetida a lo largo de días, es un
historial de desplazamientos de una persona identificable.

### El marco legal es delgado, y eso obliga a decidir

El cliente declarado ([ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)) es la
**Municipalidad de Guatemala**, una entidad pública. Guatemala **no cuenta con
una ley general de protección de datos personales** comparable al RGPD europeo;
han existido iniciativas legislativas, pero no una norma integral vigente.

Lo que sí existe y resulta aplicable:

- La **Ley de Acceso a la Información Pública** (Decreto 57-2008) regula el
  tratamiento de datos personales en poder de sujetos obligados, categoría en
  la que entra una municipalidad, y restringe su difusión sin consentimiento
  del titular.
- La **Constitución Política de la República** reconoce el derecho de toda
  persona a conocer lo que de ella conste en archivos y registros estatales, y
  a que se corrija o actualice.

> **Pendiente de verificación:** el articulado concreto de ambas normas debe
> comprobarse y citarse en formato APA 7 en el informe final. Este ADR
> identifica el marco aplicable, no sustituye la revisión legal.

La delgadez del marco es en sí misma un argumento arquitectónico: **no basta
con "cumplir la ley", porque la ley dice poco.** La Municipalidad tiene que
definir su propia política, y esa política tiene que quedar inscrita en el
diseño del sistema, no en un documento aparte que nadie aplica.

## 2. Opciones consideradas

### Opción A — No hacer nada: son datos simulados

**A favor**

- Cero esfuerzo.
- Técnicamente cierto: ninguna placa del sistema corresponde a un vehículo real.

**En contra**

- El enunciado lo descarta expresamente.
- Y, más de fondo: el sistema está diseñado para operar con datos reales. Un
  diseño que solo protege los datos cuando son falsos no protege nada. Los
  controles de privacidad hay que construirlos cuando se construye el sistema,
  porque añadirlos después exige migrar el histórico ya acumulado.

### Opción B — Cifrar la base de datos en reposo

Activar el cifrado de disco que ofrece el proveedor de base gestionada.

**A favor**

- Trivial: suele ser una casilla en el panel del proveedor.
- Protege ante el robo físico del medio de almacenamiento.

**En contra**

- **No protege contra el escenario que importa.** El cifrado en reposo es
  transparente para cualquiera que se conecte con credenciales válidas: el
  proveedor, un administrador, una credencial filtrada o una consulta mal
  hecha ven los datos en claro.
- No es alternativa sino complemento: conviene tenerlo, pero no resuelve el
  problema.

### Opción C — Anonimización irreversible: descartar el campo

Sencillamente no archivar la placa.

**A favor**

- La protección más fuerte posible: lo que no se guarda no se puede filtrar.
- Elimina la obligación de custodiar una llave.

**En contra**

- **Destruye la utilidad del archivo histórico.** Sin placa no se puede
  investigar después la recurrencia de un vehículo en una zona, que es una de
  las razones por las que una municipalidad despliega cámaras de lectura de
  placas.
- Rompe la correlación por vehículo entre distintos puntos de control.

### Opción D — Cifrado reversible con llave propia

Cifrar la placa con AES y una llave custodiada on-premise, de modo que se
pueda descifrar cuando haya una orden judicial.

**A favor**

- Conserva la utilidad completa: el dato original es recuperable.
- Modelo habitual cuando la legislación exige poder responder a requerimientos.

**En contra**

- **Un mismo texto cifrado no es estable** con modos seguros: cifrar dos veces
  la misma placa da resultados distintos, así que no sirve para correlacionar
  apariciones sin descifrar primero. Forzar la estabilidad —usando un modo
  determinista— reintroduce los problemas que se quieren evitar.
- Mantiene el dato personal recuperable: ante una filtración de la llave, la
  exposición es total.
- Exige un procedimiento formal de descifrado que la Municipalidad no tiene
  definido.

### Opción E — Seudonimización estable con HMAC en la frontera

Sustituir la placa por un seudónimo calculado con HMAC-SHA256 y una llave
secreta, justo antes de que el dato cruce hacia la nube.

**A favor**

- El seudónimo es **estable**: el mismo vehículo produce siempre el mismo
  token, de modo que la correlación histórica sigue siendo posible.
- Es **irreversible sin la llave**, y la llave no acompaña al dato.
- Se aplica en un punto único y verificable del sistema.

**En contra**

- El dato sigue siendo **personal**: un seudónimo estable permite rastrear,
  aunque no identificar directamente. No es anonimización.
- Introduce un secreto crítico que hay que custodiar, rotar y respaldar.
- Perder la llave vuelve el histórico irreversiblemente anónimo.

## 3. Decisión

**Se adopta la opción E**, complementada con B —cifrado en reposo del
proveedor, por ser gratuito— cuando esté disponible.

### 3.1 Dónde se aplica: la frontera es un componente

La decisión más importante de este ADR no es *qué* transformación se aplica,
sino **dónde**.

```
Productor ──en claro──▶ Ingestor ──en claro──▶ Kafka (LAN) ──▶ Correlator
                                                   │
                                        ┌──────────┴──────────┐
                                        ▼                     ▼
                                   Persister              Indexer
                                        │ seudónimo           │ seudónimo
                                        ▼                     ▼
                                  PostgreSQL            Elasticsearch
```

Solo **dos componentes escriben eventos fuera de la LAN**: el persister y el indexer
([ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)). Ambos se sitúan exactamente sobre
la frontera, y ambos aplican la misma transformación con **la misma llave**.

Que la llave sea la misma no es un detalle de configuración: si difiriera, el
mismo vehículo tendría seudónimos distintos en cada destino y las búsquedas
cruzadas entre el archivo y el índice fallarían **en silencio**, devolviendo
resultados incompletos sin dar ninguna señal de error. Se verificó sobre tres
eventos que los tokens coinciden en ambos destinos.

Eso da una propiedad valiosa: **para auditar que ningún dato personal sale del
control municipal en claro, basta revisar dos componentes**, y ambos comparten
el mismo paquete de tokenización. No hay que rastrear cada consulta del sistema
buscando fugas.

El servicio `jobs` también habla con la nube: borra, agrega y, desde el
[ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde), guarda la geometría de los recorridos, que
sí es ubicación y por eso sigue la misma retención que las coordenadas (ver 4.3). El notifier escribe los avisos, que no contienen
placas ni coordenadas.

Dentro de la LAN la placa circula en claro a propósito. El broker no sale de la
red municipal, y la correlación necesita el valor original para agrupar
apariciones del mismo vehículo. Proteger el dato dentro del perímetro que ya
está protegido añade coste sin añadir seguridad.

### 3.2 Por qué HMAC y no un resumen simple

Un `SHA-256` de la placa sería inútil. **El espacio de placas es pequeño y
enumerable**: unos pocos millones de combinaciones posibles. Cualquiera con el
resumen puede generar todas las placas posibles, calcular sus resúmenes y
construir la tabla inversa completa en minutos.

HMAC-SHA256 con una llave secreta convierte ese ataque en inviable mientras la
llave esté protegida: sin ella no se puede calcular el resumen de una placa
candidata, de modo que no hay nada que comparar.

### 3.3 Normalización previa

La placa se normaliza antes de calcular el seudónimo: mayúsculas y solo
caracteres alfanuméricos. Sin eso, `abc-123`, `ABC123` y ` ABC 123 ` producirían
tres seudónimos distintos para el mismo vehículo, según cómo lo escribiera cada
sensor, y **se perdería precisamente la correlación que se quería conservar**.

### 3.4 Longitud del seudónimo

Se conservan 128 bits (32 caracteres hexadecimales) de los 256 que produce el
HMAC, con el prefijo `plate_`. Es suficiente para que las colisiones sean
despreciables y deja un identificador más manejable en registros y tableros.

### 3.5 Custodia de la llave

| Aspecto | Decisión |
|---|---|
| Ubicación | On-premise, en el entorno del persister, el indexer y el correlator |
| Versionado | **Nunca** en el repositorio; `.env` está en `.gitignore` |
| Generación | `openssl rand -hex 32` |
| Respaldo | Cifrado, con procedimiento documentado y custodia separada |
| Rotación | Invalida la correlación con el histórico previo (ver consecuencias) |

Si la llave no está configurada, los servicios **arrancan igual pero lo
advierten en el registro**: `"PLATE_HMAC_KEY no configurada: las placas se
archivarán en claro en la nube"`. Se prefirió advertir a impedir el arranque
para no convertir un descuido de configuración en una caída del sistema de
seguridad ciudadana.

### 3.6 El correlator también custodia la llave, por un motivo distinto

Al incorporarse la correlación por vehículo
([ADR-011](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho) documenta el servicio hermano; la regla
vive en el correlator), un tercer servicio pasó a necesitar la llave. Conviene
ser preciso sobre por qué, porque **no** es el mismo motivo que en los otros
dos.

El persister y el indexer la usan para que el dato personal no cruce a la nube:
tokenizan en la frontera. El correlator no escribe nada fuera de la LAN y, por
tanto, no tiene ninguna frontera que proteger. La usa para **indexar por
seudónimo la ventana de correlación de vehículo** en Redis, y eso resuelve dos
problemas a la vez:

1. **La clave de Redis deja de ser una matrícula.** La ventana por vehículo es
   un `corr:plate:<seudónimo>`. Si se indexara por la placa en claro, un `SCAN`
   sobre Redis devolvería el listado de matrículas que han circulado por la
   ciudad en los últimos treinta minutos: el estado transitorio de la
   correlación se habría convertido en un fichero de vehículos.

2. **El seudónimo coincide con el archivado.** Al usar la misma llave, el
   identificador con el que el correlator agrupa es idéntico al que el
   persister escribe en PostgreSQL. Eso es lo que permite partir de una alerta
   de persecución y reconstruir el histórico del vehículo sin que la placa en
   claro salga nunca de la LAN.

Esto amplía la superficie de custodia de dos servicios a tres, y es una
contrapartida real: una llave en más entornos es una llave con más
oportunidades de filtrarse. Se acepta porque la alternativa —indexar por la
placa en claro— es estrictamente peor en el mismo eje que esta decisión intenta
proteger.

## 4. Lo que esta decisión NO protege

Esta sección existe porque un ADR de privacidad que solo enumere sus aciertos
es propaganda.

### 4.1 El rastro de movimiento permanece

La placa se seudonimiza, pero **`geo.lat`, `geo.lon`, `geo.zone` y `ts_utc`
viajan a la nube sin transformar**. Un seudónimo estable más coordenadas más
marca de tiempo sigue siendo un historial de desplazamientos.

En terminología del RGPD —que no rige en Guatemala pero sirve de referencia
conceptual— esto es **dato seudonimizado, que sigue siendo dato personal**. No
es anonimización, y llamarlo así sería incorrecto.

Se verificó empíricamente sobre el sistema en ejecución: de **537 eventos de
lectura de placas, 526 seudónimos distintos**. Los repetidos corresponden al
mismo vehículo visto varias veces, que es justamente la propiedad buscada —y
justamente lo que hace rastreable el dato.

Es un compromiso consciente: sin seudónimo estable no hay correlación por
vehículo, y sin correlación por vehículo la cámara de lectura de placas pierde
su propósito.

**Actualización (2026-10-03): esto ha dejado de ser un efecto colateral.**

Cuando se escribió esta sección, el rastreo de un vehículo era una
*posibilidad* que el diseño no impedía. Ya no: el sistema incorpora una regla
de correlación que encadena las infracciones de un mismo vehículo a través de
varias zonas, y un tablero que dibuja su recorrido sobre el mapa. El rastreo
pasó de riesgo residual a funcionalidad deliberada.

El cambio no altera qué dato se guarda —no se almacena nada que no se
almacenara antes— pero sí altera lo que hay que declarar ante el área legal. Un
dato que *podría* usarse para seguir a una persona y un dato que *se usa* para
eso no son equivalentes a efectos de proporcionalidad, aunque la base de datos
sea idéntica.

Tres límites acotan el uso, y conviene que figuren por escrito:

- **El plazo.** El rastreo alcanza `RETENCION_DIAS_PLACA` días (90 por
  defecto). Pasado ese plazo el trabajo de retención elimina
  `placa_vehicular` del payload y el vehículo deja de ser seguible. No es una
  limitación técnica: es el plazo que acota la vigilancia.
- **El disparador.** La regla no se activa por circular, sino por acumular
  infracciones **y** existir un botón de pánico en la misma zona. Un vehículo
  que respeta las normas no genera ninguna cadena por mucho que circule.
- **El alcance de la ventana.** La correlación en vivo solo mira quince minutos atrás; el histórico de noventa días requiere una consulta explícita a la base,
  que es una acción deliberada de un operador y no un proceso automático.

Queda pendiente que la Municipalidad apruebe expresamente este uso, como ya
estaba pendiente la aprobación de los plazos de retención.

**Actualización (2026-10-21):** el recorrido ahora se dibuja siguiendo las calles
([ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)). El motor de rutas corre en la LAN, de
modo que las coordenadas no salen a ningún tercero para calcularlo; pero la
geometría resultante se guarda en la nube, y de ello trata el apartado 4.3.

### 4.2 La retención está implementada, con un matiz

El enunciado pregunta cómo se **protegen, anonimizan o retienen** los datos.
Los tres están resueltos: la retención la aplica el servicio `jobs` cada noche
(ver [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow)).

La política degrada el dato conforme envejece, en lugar de conservarlo intacto
para siempre o eliminarlo de golpe:

| Antigüedad | Placa | Coordenadas | Zona | Evento |
|---|---|---|---|---|
| 0 – 90 días | conservada | conservadas | conservada | conservado |
| 90 días – 1 año | **eliminada** | conservadas | conservada | conservado |
| 1 – 2 años | — | **eliminadas** | conservada | conservado |
| Más de 2 años | — | — | — | **borrado**; queda el agregado diario |

En Elasticsearch la purga es más simple todavía: como el indexer crea un índice
por mes, eliminar un periodo es borrar índices completos.

**El matiz:** los plazos son configurables y los valores actuales son una
propuesta técnica, no una decisión institucional. **La Municipalidad debe
aprobarlos formalmente**, porque determinan durante cuánto tiempo conserva
datos personales de ciudadanos y eso no es una decisión de ingeniería.

### 4.3 Los recorridos guardados y los avisos siguen la retención

Esta sección recoge una incoherencia que existió y ya está corregida. Se deja
escrita porque explica por qué la política abarca más que los eventos.

Al incorporar los recorridos por calle ([ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)),
`jobs` pasó a guardar en `alert_routes` la geometría del recorrido de cada alerta
con botón de pánico y un solo vehículo: una secuencia de coordenadas que reproduce
por dónde circuló. No lleva la placa, pero sí el `alert_id`, que enlaza con los
eventos y, mientras existan, con el seudónimo. La retención solo actuaba sobre
`events`, de modo que una trayectoria sobrevivía indefinidamente aunque el evento
del que salía ya hubiera perdido sus coordenadas.

La retención aplica ahora cinco fases, de la más reciente a la más antigua:

| Fase | Qué hace | Plazo |
|---|---|---|
| 1 | Elimina el seudónimo de la placa de los eventos | 90 días |
| 2 | Elimina las coordenadas de los eventos | 365 días |
| 3 | Borra los eventos | 730 días |
| 4 | Borra los **recorridos guardados** (`alert_routes`) | 365 días, el de las coordenadas |
| 5 | Borra los **avisos** (`notifications`) | 730 días, el del borrado |

Dos criterios que conviene saber:

- **La edad de un recorrido es la de su alerta, no la de su cálculo.** La ruta se
  recalcula mientras el rastro crece, y su fecha de cálculo no dice cuándo ocurrió
  el incidente. Si la alerta ya no existe, se usa la fecha de cálculo, para que una
  fila huérfana no se quede para siempre.
- **El recorrido sigue el plazo de las coordenadas porque es lo mismo:**
  coordenadas encadenadas. Un año de plazo para la coordenada de un evento y
  indefinido para su trayectoria no tendría sentido.

Se verificó con una prueba automática contra un PostgreSQL real, en un esquema
temporal para no tocar datos de nadie, y sobre el sistema en ejecución: una alerta
de 400 días con su recorrido y un aviso de 800 días se eliminaron al lanzar la
retención a mano, y los datos recientes quedaron intactos.

**Lo que no se purga, y por qué.** Las alertas (`alerts`) no se borran: guardan tipo,
zona, ventana, puntaje e identificadores de evidencia, sin placa ni coordenadas, y
son el registro al que apuntan los agregados diarios y los avisos. Es una decisión
que, como los plazos del apartado 4.2, debe aprobar la Municipalidad.

### 4.4 Sin control de acceso granular

Cualquiera con credenciales de la base ve todo el histórico. No hay roles que
distingan entre consultar agregados y consultar eventos individuales. Los
endpoints de consulta del correlator sí exigen autenticación, pero el acceso
directo a la base no está segmentado.

## 5. Consecuencias

### Positivas

- La nube nunca recibe placas identificables.
- Auditable en pocos puntos: los dos servicios que archivan eventos y el que
  guarda recorridos, que además cae bajo la misma retención.
- La correlación por vehículo se conserva íntegra.
- El punto de corte del despliegue y la frontera de privacidad coinciden: una
  sola decisión arquitectónica resuelve dos problemas.
- Ante una filtración de la base, el atacante obtiene seudónimos inútiles sin
  la llave.

### Negativas aceptadas

- **Aparece un secreto crítico.** Hay que custodiarlo, respaldarlo y rotarlo.
  Es una responsabilidad operativa nueva para un equipo reducido.
- **Rotar la llave rompe la continuidad del histórico.** Los seudónimos
  anteriores y los posteriores no coinciden, de modo que un vehículo aparece
  como dos. Una rotación exige re-seudonimizar el archivo o aceptar un corte en
  la serie.
- **Perder la llave anonimiza el histórico de forma irreversible.** Deja de ser
  útil para investigación. Es el reverso exacto de la propiedad de seguridad.
- **Los datos siguen siendo personales.** Todas las obligaciones de tratamiento
  aplican igual; la seudonimización reduce el riesgo, no la responsabilidad.
- **Las alertas no se purgan.** No llevan datos personales, pero se conservan
  sin plazo hasta que la Municipalidad decida uno (apartado 4.3).

## 6. Evidencia medida

Verificado el 21 de septiembre de 2026 sobre el sistema en ejecución.

### La frontera funciona

| Dónde | Valor de la placa |
|---|---|
| Kafka, topic `events.standardized` (LAN) | `P123ABC` |
| PostgreSQL, tabla `events` (nube) | `plate_161f238e9d6f70a3986ebb6ffb670b8a` |

Consulta de control sobre el total del archivo:

```
placas en claro en la nube: 0
```

### El seudónimo es estable y, por tanto, rastreable

537 eventos de lectura de placas produjeron 526 seudónimos distintos. La
repetición es intencionada y es lo que permite correlacionar; también es lo que
mantiene el dato dentro de la categoría de dato personal.

### Lo que igualmente cruza

Una fila real del archivo, tal como queda en la nube:

```
zona=zona_18  lat=14.672  lon=-90.471  placa=plate_4f78e4c6…  ts=15:01:59
```

Las coordenadas y la marca de tiempo están sin transformar. Es la limitación
descrita en la sección 4.1.

## 7. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Las placas de los vehículos **nunca salen de las instalaciones de la
Municipalidad en forma legible**. Antes de enviarse al archivo contratado por
internet se convierten en un código, y la clave para relacionarlo con la placa
real se queda en las instalaciones municipales.

Si alguien obtuviera acceso indebido al archivo externo, vería códigos sin
valor. Sin la clave, que nunca sale de la Municipalidad, no hay forma de saber
a qué vehículo corresponde cada uno.

### Qué se conserva

El código es **siempre el mismo para el mismo vehículo**. Eso permite seguir
investigando si un vehículo apareció varias veces en una zona, que es una de
las razones por las que se instalan cámaras de lectura de placas. Se protege el
dato sin perder su utilidad.

### Qué no cubre, y conviene saberlo

**La ubicación y la hora sí se envían sin transformar.** Aunque la placa esté
protegida, el archivo permite reconstruir los desplazamientos de un vehículo
—sin saber cuál— a lo largo del tiempo. Es información sensible y sigue
sujeta a las obligaciones de protección de datos.

**La política de borrado ya funciona.** Cada noche, un proceso automático
elimina la información que superó su plazo: la identificación del vehículo a
los tres meses, la ubicación exacta al año, y el registro completo a los dos
años, conservando solo estadísticas.

Los plazos son configurables y los actuales son una **propuesta técnica que la
Municipalidad debe aprobar formalmente**, porque determinan cuánto tiempo se
conservan datos personales de ciudadanos.

### Qué exige

| Obligación | Frecuencia |
|---|---|
| Custodiar la clave de protección, con respaldo cifrado | Permanente |
| Designar responsable de su resguardo | Una vez |
| **Aprobar formalmente los plazos de borrado** | **Pendiente** |
| Revisar quién tiene acceso al archivo | Semestral |
| Comprobar que la purga nocturna se ejecutó | Mensual |

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| Pérdida de la clave | Baja | **Alto**: el archivo queda inservible para investigación | Respaldo cifrado con custodia separada |
| Filtración de la clave | Baja | Alto: la protección deja de tener efecto | Nunca se versiona; rotación ante sospecha |
| Reconstrucción de desplazamientos a partir de ubicación y hora | Media | Medio | **No mitigado.** Requiere reducir la precisión de las coordenadas en el archivo |
| Crecimiento indefinido del archivo | Baja | Medio | Resuelto: purga nocturna automática, verificada |

---

## Fuentes a citar en el informe

- Ley de Acceso a la Información Pública de Guatemala, Decreto 57-2008 —
  articulado sobre datos personales en poder de sujetos obligados. **Verificar
  los artículos concretos.**
- Constitución Política de la República de Guatemala — derecho de acceso a los
  registros estatales. **Verificar el artículo.**
- Estado de las iniciativas legislativas guatemaltecas sobre protección de
  datos personales, para sustentar la afirmación de que no existe norma general
  vigente.
- RGPD (Unión Europea), artículo 4 — se cita únicamente como **referencia
  conceptual** de la distinción entre seudonimización y anonimización; no rige
  en Guatemala.

## Referencias

- Enunciado del proyecto final, secciones 4.2 y 9.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — el punto de corte del despliegue.
- [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) — el persister como único
  componente que escribe en la nube.
- `services/persister/internal/privacy/plate.go` — implementación y pruebas.

---

# ADR-008: Observabilidad y objetivos de nivel de servicio

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (presupuesto de latencia), [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) (clasificación de fallos)

---

## 1. Contexto

El enunciado plantea este punto con una exigencia precisa (sección 4.2):
definir **umbrales concretos** —pone como ejemplo "alerta generada en menos de
2 segundos desde el evento"— y **qué ocurre cuando se incumplen, *más allá de
tener dashboards en Grafana***.

Esa última frase es la que decide el contenido de este ADR. Un tablero solo
avisa a quien lo está mirando. La pregunta no es qué se muestra, sino **qué
pasa a las tres de la madrugada cuando nadie mira**.

### El punto de partida: qué significa "tener métricas"

Exponer un endpoint `/metrics` es facilísimo y no significa nada por sí solo.
El antipatrón es un endpoint que devuelve un literal:

```
GET /metrics
    // pendiente: agregar la lógica real de métricas
    return {"alerts": 0, "events": 0}
```

Los registros eran `System.out.println` sin estructura, imposibles de filtrar o
agregar. Y aunque el evento canónico incluye `trace_id`, y el ingestor lo
generaba, **ese identificador nunca llegaba a los registros**: no había forma de
seguir un evento concreto desde su ingesta hasta la alerta que produjo.

El resultado práctico: era imposible responder *"¿cuánto tarda el sistema en
generar una alerta?"* con un número. Sin esa capacidad no hay SLO posible,
porque un objetivo que no se puede medir no es un objetivo, es una intención.

## 2. Opciones consideradas

### Opción A — Tableros de Grafana consultando la base de datos

Grafana conectado directamente a PostgreSQL, con consultas de agregación sobre
la tabla de alertas filtradas por fecha.

**A favor**

- Cero instrumentación: los datos ya están en la base.
- Suficiente para mostrar volumen y distribución por zona y tipo.

**En contra**

- **Mide el resultado, no el proceso.** Permite saber cuántas alertas hay, pero
  no cuánto tardaron en generarse, cuántos eventos se rechazaron ni por qué.
- **Depende de la nube.** Tras
  [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube), la base está fuera de la LAN: si
  se cae el enlace, se pierde la visibilidad justo cuando más hace falta.
- Carga el sistema de archivo con consultas de monitoreo que se repiten cada
  pocos segundos.
- No permite alertar: Grafana avisa a quien lo mira.

### Opción B — Solo registros estructurados

Emitir todo en JSON y agregarlo en Elasticsearch, calculando lo que haga falta
con consultas.

**A favor**

- Un solo mecanismo que cubre diagnóstico y medición.
- Máximo detalle: cada línea conserva su contexto completo.

**En contra**

- Calcular un percentil de latencia sobre registros es caro y lento.
- El volumen crece con el tráfico; las métricas no.
- También depende de la nube.

### Opción C — Métricas Prometheus, registros estructurados y reglas de alerta

Instrumentar los servicios con métricas en el proceso, registros JSON con
`trace_id` propagado, y reglas que disparen sin intervención humana.

**A favor**

- El cálculo de percentiles es lo que Prometheus hace bien y barato.
- Las métricas viven **en la LAN**: la observabilidad sobrevive a una caída del
  enlace.
- Las reglas de alerta avisan sin que nadie mire.
- Los registros conservan el detalle para diagnosticar lo que las métricas
  señalan.

**En contra**

- Un componente más que desplegar y mantener.
- Instrumentar es trabajo explícito en cada servicio.
- Las métricas agregan: para entender un caso concreto hay que ir al registro.

### Opción D — Trazabilidad distribuida completa

OpenTelemetry con un recolector y un backend de trazas.

**A favor**

- Visión exacta del recorrido de cada evento, tramo por tramo.

**En contra**

- Complejidad operativa desproporcionada para seis servicios y un equipo
  reducido.
- Buena parte del beneficio se obtiene propagando el `trace_id` por registros y
  cabeceras de Kafka, que es mucho más barato.

## 3. Decisión

**Se adopta la opción C.** La D queda registrada como evolución natural si el
sistema creciera en número de servicios.

### 3.1 Objetivos de nivel de servicio

Cuatro objetivos, cada uno con su indicador y su ventana de evaluación:

| # | Objetivo | Umbral | Indicador | Ventana |
|---|---|---|---|---|
| 1 | **Latencia de detección** | 99 % de las alertas en menos de **2 s** desde el evento disparador | `correlator_correlation_latency_seconds` | 30 días |
| 2 | **Integridad de ingesta** | 99,9 % de los eventos con `202` quedan archivados | eventos en PostgreSQL / eventos aceptados | 30 días |
| 3 | **Disponibilidad de ingesta** | 99,5 % de las peticiones sin error `5xx` | `ingestor_request_duration_seconds{status}` | 30 días |
| 4 | **Frescura del archivo** | 95 % de los eventos archivados en menos de **60 s** | `persister_retraso_archivo_seconds` | 7 días |

El objetivo 1 es el que define el propósito del sistema; el 4 es deliberadamente
más laxo porque, tras
[ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube), el archivado está **fuera del camino
crítico**: que se retrase no impide detectar incidentes.

### 3.2 El indicador de latencia mide lo correcto

`correlator_correlation_latency_seconds` registra el tiempo entre el
**`timestamp` del evento que dispara la correlación** y el momento de emitir la
alerta. No mide el tiempo de proceso interno, sino el recorrido completo:
transporte por Kafka, espera en la cola, evaluación de reglas y publicación.

La distinción importa. Si el correlator acumula retraso, la latencia interna
seguiría siendo de milisegundos mientras la real crece a minutos. Medir desde
el timestamp del evento hace visible ese retraso; medir desde la recepción lo
escondería.

Es también la razón de que la herramienta de carga advierta explícitamente que
**la latencia de ingesta no es el SLO**: `/events` responde en 14 ms, pero eso
solo dice que el evento se aceptó, no que se haya detectado nada.

### 3.3 Qué ocurre cuando se incumplen

Aquí está el núcleo de lo que pide el enunciado.

**Catorce reglas de alerta** en `deploy/prometheus/alertas.yml`, validadas con
`promtool`, agrupadas en cinco familias: latencia, integridad, disponibilidad,
salud interna y tareas periodicas.

Cada alerta lleva tres anotaciones: `resumen`, `descripcion` y —la que importa—
**`accion`**, con el primer paso concreto de diagnóstico. El criterio es que
quien reciba la alerta de madrugada no tenga que deducir por dónde empezar.

Ejemplo, la del SLO 1:

```yaml
- alert: SLOLatenciaDeteccionIncumplido
  expr: |
    ( sum(rate(correlator_correlation_latency_seconds_count[30m]))
      - sum(rate(correlator_correlation_latency_seconds_bucket{le="2"}[30m])) )
    / sum(rate(correlator_correlation_latency_seconds_count[30m])) > 0.01
  for: 10m
  annotations:
    accion: >-
      1) Revisar el lag del consumidor: si crece, el correlator no sigue el
      ritmo de la ingesta. 2) Revisar correlator_window_events: una ventana
      desproporcionada encarece cada evaluacion. 3) Si ambos son normales,
      sospechar de Redis.
```

#### Escalamiento por severidad

| Severidad | Significado | Respuesta esperada |
|---|---|---|
| `critica` | El sistema no cumple su propósito | Inmediata, a cualquier hora |
| `aviso` | Se consume presupuesto de error | En horario laboral |
| `info` | Conviene saberlo | Revisión periódica |

La regla `ServicioCaido` distingue en su propia acción: si el caído es el persister, la detección continúa y solo se retrasa el archivo; si es el notifier, la detección continúa pero nadie registra a qué organismo se avisó; si es el ingestor o el correlator, **la ciudad ha dejado de detectar incidentes** y es
la máxima prioridad. Esa jerarquía es consecuencia directa de
[ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube).

#### Presupuesto de error

El SLO 1 al 99 % sobre 30 días deja un **1 % de presupuesto**. Su consumo
gobierna decisiones:

| Presupuesto consumido | Qué se hace |
|---|---|
| Menos del 50 % | Operación normal; se pueden desplegar cambios |
| 50 % – 100 % | Se congelan los cambios no esenciales; prioridad a estabilizar |
| Más del 100 % | Se escala a la coordinación del proyecto; se revisa el umbral o la capacidad |

El valor de un presupuesto de error es que convierte "¿desplegamos el viernes?"
en una pregunta con respuesta objetiva.

### 3.4 Dos alertas que no son de infraestructura

Dos reglas vigilan el **dominio**, no la máquina, y conviene señalarlas porque
son las que un profesor probablemente pregunte:

- **`MuchasAlertasSuprimidas`** — si se suprime más del 90 % de las alertas,
  `ALERT_SUPPRESS_TTL` probablemente sea demasiado largo y esté ocultando
  incidentes distintos en la misma zona. La supresión evita ruido; en exceso,
  esconde.
- **`TasaAltaDeFalsosPositivos`** — si más de la mitad de las coincidencias
  parciales expira sin completarse, los umbrales o las ventanas están mal
  calibrados. El enunciado trata el análisis de falsos positivos como mérito
  adicional (sección 10.3); esta regla lo convierte en una señal operativa
  continua en lugar de un análisis puntual.

### 3.5 Trazabilidad extremo a extremo

El `trace_id` entra por la cabecera `X-Trace-Id` —o se genera si falta—, viaja
en el contexto de la petición, se emite en **cada línea de registro** del
ingestor, se adjunta como cabecera del mensaje de Kafka y el correlator lo
recupera y lo emite en sus propios registros.

Resultado: un solo identificador permite reconstruir el recorrido completo de
un evento a través de los servicios. Es el 80 % del valor de una
trazabilidad distribuida al 5 % de su costo operativo.

### 3.5.1 Dónde vive la observabilidad, y por qué no en la nube

Prometheus y Grafana corren **en la LAN municipal**, no en la capa de nube,
pese a que la analítica es justamente lo que el
[ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) sitúa fuera. La excepción tiene dos
razones y conviene dejarlas escritas, porque la ubicación intuitiva es la
contraria.

**La razón de seguridad.** Recolectar métricas no es que los servicios envíen
datos: es que el recolector **abre conexiones hacia cada servicio**. Con
Prometheus en la nube, esas serían conexiones de nube hacia la red municipal,
es decir **tráfico de entrada**, que es precisamente lo que el modelo híbrido
excluye. Todo lo que cruza la frontera debe ser de salida.

**La razón de utilidad.** Si el enlace se cae, la detección sigue funcionando
—esa es la propiedad central del ADR-001— y es justo entonces cuando hacen
falta las métricas y las alertas. Un recolector al otro lado del enlace se
queda ciego en el único momento en que importa.

Grafana acompaña a Prometheus porque lo consulta, y separarlos reintroduciría
el mismo tráfico de entrada. Sus paneles de PostgreSQL y Elasticsearch sí
cruzan la frontera, pero como **consultas puntuales de salida**: durante un
corte esos paneles quedan en blanco y los de tiempo real siguen vivos, que es
exactamente la degradación que se quiere.

El esquemático de `docs/diagramas/infraestructura.svg` lo muestra como nodo E,
dentro del segmento LAN.

### 3.6 Tres propiedades de Prometheus que condicionan las reglas

Las tres se observaron durante la verificación y afectan a cómo hay que
escribir las reglas:

**Los contadores se reinician al reiniciar el proceso.** Se comprobó al
reiniciar el correlator: sus métricas volvieron a cero. Por eso todas las
reglas usan `rate()` o `increase()`, que manejan correctamente los reinicios,
y ninguna compara valores absolutos acumulados.

**Las métricas con etiquetas no existen hasta su primer uso.** Un
`CounterVec` no publica ninguna serie hasta que se observa una combinación de
etiquetas. Una consulta como `correlator_alerts_generated_total` no devuelve
cero antes de la primera alerta: **no devuelve nada**. Por eso la detección de
ausencia de tráfico se hace sobre `ingestor_events_ingested_total`, que siempre
tiene actividad en operación normal, y no sobre métricas que pueden no existir.

**Prometheus 3 normaliza las etiquetas `le` de los histogramas.** Es la que más
cara salió, porque no produce ningún error: produce silencio.

El correlator publica su bucket como `le="2"`, tal como lo declara el código.
Prometheus 3 lo almacena en su representación canónica de coma flotante, es
decir `le="2.0"`. Un selector literal `{le="2"}` no encuentra serie alguna, de
modo que la regla del SLO **no podía dispararse nunca**: el objetivo estaba
declarado, documentado y sin vigilancia real.

Se detectó porque el panel de cumplimiento del tablero llegaba vacío, no porque
alguna comprobación fallara. `promtool check rules` valida la sintaxis y da
`SUCCESS` igual, porque la expresión es correcta; lo que no existe es la serie.

La corrección es usar una expresión regular que acepte ambas formas:

```
correlator_correlation_latency_seconds_bucket{le=~"2(\\.0)?"}
```

Funciona igual en Prometheus 2 y 3. Como consecuencia adicional, la imagen de
Prometheus dejó de usar la etiqueta `latest` y pasó a versión fija: un cambio
de versión silencioso puede dejar sin efecto una regla de SLO, y para una
demostración evaluada en vivo la reproducibilidad vale más que estar al día.

La lección general es la que más conviene retener: **una regla de alerta que
no encuentra datos no se distingue de una regla que no tiene nada que
reportar.** Ambas callan. Por eso la verificación no puede limitarse a validar
la sintaxis; hay que comprobar que cada expresión devuelve serie contra un
Prometheus con datos reales.

## 4. Consecuencias

### Positivas

- Los SLO son verificables con un número, no con una impresión.
- La observabilidad vive en la LAN y sobrevive a una caída del enlace.
- Las alertas avisan sin que nadie mire un tablero.
- El presupuesto de error convierte decisiones de despliegue en objetivas.
- Un `trace_id` reconstruye el recorrido completo de cualquier evento.

### Negativas aceptadas

- **Un componente más que operar.** Prometheus hay que desplegarlo, dimensionar
  su retención y mantenerlo.
- **Las alertas hay que atenderlas.** Un sistema que avisa y a quien nadie
  responde es peor que uno que no avisa: enseña a ignorar avisos. Exige definir
  quién recibe cada severidad.
- **Los umbrales están sin calibrar con datos reales.** Los de este ADR se
  fijaron sobre mediciones de laboratorio; en producción habrá que ajustarlos
  tras observar el comportamiento real, y probablemente generen falsas alarmas
  al principio.
- **Falta el canal de notificación.** Las reglas disparan dentro de Prometheus,
  pero **no hay Alertmanager configurado** para enviarlas a un correo o un
  teléfono. Está identificado como trabajo pendiente.

  Conviene no confundirlo con lo que resuelve
  [ADR-011](#adr-011-aviso-a-terceros-servicio-propio-con-registro-de-despacho). Son dos canales distintos y ninguno
  sustituye al otro: aquel avisa a **los cuerpos de respuesta** de que hay un
  incendio en la zona 12; este avisaría al **equipo que opera el sistema** de
  que el propio sistema está fallando. El segundo sigue pendiente.
- **No hay un objetivo de nivel de servicio para el aviso.** El notifier mide
  `notifier_retraso_aviso_seconds` (de la creación de la alerta al aviso), pero no
  se fijó un umbral ni una regla de alerta sobre él. Es el siguiente SLO natural
  ahora que existe el servicio.
- **Instrumentar es trabajo recurrente.** Cada funcionalidad nueva debería
  añadir sus métricas, y eso se olvida con facilidad.

## 5. Evidencia medida

Medido el 21 de septiembre de 2026 sobre el sistema en ejecución.

### SLO 1 — Latencia de detección

Medición limpia, con las métricas reiniciadas y sobre alertas generadas en el
momento:

| Métrica | Valor |
|---|---|
| Alertas medidas | 10 |
| Latencia media | **44,1 ms** |
| Bajo 50 ms | 70 % |
| Bajo 100 ms | 100 % |
| **Bajo 2 s (objetivo)** | **100 %** |

Una medición anterior arrojó un 60 % bajo el umbral. Esa cifra estaba
contaminada por el *catch-up* posterior a reiniciar el correlator: al ponerse al
día procesa eventos con `timestamp` antiguo, y la latencia medida —que va desde
el timestamp del evento— refleja correctamente ese retraso. Es el indicador
funcionando como debe; no era representativo de la operación normal.

Merece quedar anotado porque es el tipo de matiz que distingue una medición de
una cifra.

### SLO 3 — Disponibilidad bajo carga

| Escenario | Peticiones | `5xx` | p50 | p99 |
|---|---|---|---|---|
| Ráfaga de 100 ev/s durante 10 s | 1 192 | **0** | — | 19,7 ms |
| Sostenido a 25 ev/s durante 12 s | 299 | **0** | 14,3 ms | 18,8 ms |

### SLO 2 — Integridad

2 403 eventos transportados por Kafka, 2 403 archivados en PostgreSQL. 40
alertas, 40 archivadas. Cero discrepancia.

### Reglas de alerta

```
Checking alertas.yml
  SUCCESS: 14 rules found
```

Las catorce evalúan sin error contra el Prometheus en ejecución: ninguna quedó
en estado `err` ni con expresión que no encuentre serie.

### Tableros

Cuatro tableros provisionados con **42 paneles**. Se comprobó enviando a sus
respectivos motores el texto exacto de cada consulta generada:

| Comprobación | Resultado |
|---|---|
| Expresiones PromQL de paneles y reglas | 45 comprobadas, 0 rechazadas |
| Consultas SQL del tablero de incidentes | 11 comprobadas, 0 con error |
| Paneles del tablero de trazabilidad | 11 de 11 con datos, sobre un incidente de la simulación (`./make.sh verificar`, 21 de octubre) |
| Orígenes de datos provisionados | Prometheus, PostgreSQL y Elasticsearch, los tres con conexión correcta |

Esa comprobación encontró dos defectos que ninguna validación de sintaxis
habría detectado: el selector de bucket descrito en el apartado 3.6, y unas
credenciales de PostgreSQL que el archivo de despliegue no pasaba al contenedor
de Grafana, de modo que el origen de datos se provisionaba con usuario y base
vacíos.

## 6. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Que el sistema **se vigile a sí mismo y avise solo** cuando deja de cumplir lo
prometido, en lugar de depender de que alguien esté mirando una pantalla.

### El compromiso que se asume

**Una alerta de seguridad se genera en menos de 2 segundos desde que ocurre el
evento**, en el 99 % de los casos. Hoy se cumple con amplio margen: el promedio
medido es de 44 milésimas de segundo.

Se asumen otros tres compromisos: que todo evento aceptado quede archivado, que
el sistema esté disponible, y que el archivo histórico no se atrase más de un
minuto.

### Qué pasa cuando no se cumple

Catorce reglas automáticas vigilan esos compromisos. Cada una lleva escrito el
primer paso a seguir, para que quien la reciba —incluso de madrugada— sepa por
dónde empezar sin tener que averiguarlo.

Las alertas se clasifican en tres niveles. Solo el más grave —que el sistema
haya dejado de detectar incidentes— exige atención inmediata a cualquier hora.
El resto se atiende en horario laboral.

### El concepto de "presupuesto de fallos"

Se acepta de antemano que el sistema falle un 1 % de las veces. Mientras se
consuma menos de la mitad de ese margen, se pueden hacer cambios con
normalidad. Si se supera la mitad, se congelan los cambios no urgentes hasta
estabilizarlo.

Esto evita la discusión recurrente de *"¿es buen momento para actualizar?"*:
la respuesta es un número, no una opinión.

### Lo que falta

**El canal de aviso no está conectado.** Las reglas funcionan y detectan, pero
todavía no envían la notificación a un correo o un teléfono. Es configuración
pendiente, de pocas horas de trabajo.

Además, **los umbrales están calibrados con pruebas de laboratorio**. Durante
las primeras semanas en operación real habrá que ajustarlos, y es previsible
que al principio se generen avisos que no correspondan a problemas reales.

### Qué exige

| Obligación | Frecuencia |
|---|---|
| Definir quién recibe cada nivel de alerta | Una vez |
| Conectar el canal de notificación | Pendiente |
| Revisar y ajustar umbrales | Mensual los primeros meses |
| Revisar el consumo del presupuesto de fallos | Mensual |

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — observabilidad y SLOs; sección
  10.3 — análisis de falsos positivos.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — presupuesto de latencia y
  jerarquía de criticidad entre servicios.
- [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) — la clasificación de
  fallos alimenta las métricas por motivo.
- `deploy/prometheus/alertas.yml` — las catorce reglas.
- `services/*/internal/obs/obs.go` — instrumentación de cada servicio.

---

# ADR-009: Orquestación de tareas periódicas — retirar Airflow

**Estado:** aceptado e implementado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) (política de retención), [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) (sustituye a los chequeos de salud)

---

## 1. Contexto

El enunciado no plantea este punto de forma neutra. Lo formula como pregunta
(sección 4.2):

> **Orquestación de tareas periódicas:** Apache Airflow vs. un scheduler más
> liviano — *¿se justifica la complejidad operativa de Airflow a esta escala?*

Una pregunta así invita a responderla, no a esquivarla adoptando lo sugerido.

Airflow exige un servidor web, un planificador y una base de metadatos, con
sus propias migraciones de esquema y su gestión de usuarios. Adoptarlo se
justifica si hay trabajo periódico que gobernar, de modo que la pregunta
previa es cuánto hay.

### Las tareas candidatas, una por una

Un sistema como este invita a pensar en cuatro clases de trabajo periódico.
Tres se disuelven al examinarlas.

**Chequeos de salud.** La forma clásica es una tarea que consulta cada servicio
cada pocos minutos y escribe el resultado en un registro. Tras
[ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) eso sería redundancia sin beneficio:
Prometheus recoge el estado cada diez segundos y **catorce reglas de alerta**
avisan sin que nadie mire. Una tarea periódica comprobaría peor y avisaría a
nadie.

**Limpieza de claves en Redis.** La guía del curso la sugiere, pero el diseño
de las ventanas ([ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos)) poda por
marca de tiempo en la misma operación de escritura y apoya con TTL. No queda
nada que limpiar después.

**Copia periódica de PostgreSQL a Elasticsearch.** Es la tarea que más
naturalmente pediría un orquestador, y este diseño la elimina por completo
sustituyéndola por un consumidor de Kafka. El razonamiento está en la sección
3.2, y es el que más debilita el caso de Airflow.

**Agregados y retención.** Estas dos sí son trabajo periódico real y no se
pueden disolver: dependen del calendario, no de la llegada de un evento.

### Qué trabajo periódico queda

| Tarea | Frecuencia | Origen |
|---|---|---|
| Purga y degradación del histórico | Diaria | [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) |
| Agregados diarios por zona y tipo | Diaria | Informes municipales |
| Indexación en Elasticsearch | — | Ninguna: la resuelve un consumidor (sección 3.2) |
| Limpieza de claves en Redis | **Ninguna** | Los TTL lo resuelven solos |
| Chequeos de salud | **Ninguna** | [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) |
| Recorridos por calle de las alertas | Cada minuto | [ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde) (posterior a esta decisión) |

**El trabajo periódico real eran dos tareas nocturnas.** Desde el
[ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde) hay una tercera, de otra naturaleza: no cierra
un día, acompaña al flujo y corre cada minuto. Tres tareas siguen sin ser un caso
para Airflow, y el argumento de esta sección se mantiene. De ahí que la pregunta
deje de ser "¿conviene Airflow para gobernar el trabajo periódico?" y pase a
ser **"¿conviene Airflow para dos tareas que corren de madrugada?"**.

## 2. Opciones consideradas

### Opción A — Mantener Airflow

**A favor**

- Interfaz web con historial de ejecuciones: personal no técnico puede ver si
  el proceso de anoche corrió y por qué falló.
- Reintentos, *backfill* y dependencias entre tareas resueltos.
- Es lo que sugiere el enunciado, y desviarse exige justificarlo.
- Si el número de tareas creciera, ya estaría desplegado.

**En contra**

- **Desproporción manifiesta.** Servidor web, planificador y base de metadatos
  para ejecutar dos tareas al día.
- Su huella supera a la de los tres microservicios juntos. Sobre hardware
  propio y no elástico ([ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)), eso compite
  con Kafka por la misma memoria.
- **Carga operativa propia**: migraciones de su base de metadatos, gestión de
  usuarios y fallos de arranque cuando el esquema queda desfasado, que son una
  fuente recurrente de incidencias bien documentada.
- Introduce Python en un sistema que, por lo demás, es un único lenguaje: otro
  ecosistema de dependencias que auditar y actualizar.

### Opción B — Cron del sistema invocando un binario

**A favor**

- La herramienta más simple que resuelve el problema.
- Presente en cualquier sistema tipo Unix, sin desplegar nada.

**En contra**

- **Encaja mal con Docker Compose.** Obliga a un contenedor con cron dentro, o
  a depender del cron del anfitrión, lo que rompe la portabilidad del
  despliegue.
- Sin historial: saber si la tarea de anoche corrió exige buscar en registros.
- La sintaxis de cron es notoriamente fácil de equivocar.

### Opción C — Binario Go con planificador interno

Un servicio más, del mismo estilo que los otros tres, que duerme entre
ejecuciones y despierta a su hora.

**A favor**

- **Consistente con el resto del sistema**: mismo lenguaje, mismo formato de
  registros, mismas métricas, misma forma de desplegar.
- Huella despreciable: alrededor de 15 MB en reposo, como los demás.
- Se instrumenta igual que todo lo demás, de modo que sus fallos entran por el
  mismo canal de alertas definido en
  [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) en lugar de exigir un sistema de avisos
  aparte.
- El patrón ya está en uso y probado: el correlator barre coincidencias
  parciales con un temporizador interno.

**En contra**

- Hay que escribirlo: planificación, reintentos y registro de ejecuciones no
  vienen dados.
- Sin interfaz web. Para saber qué pasó hay que mirar registros o métricas.
- Un proceso que duerme la mayor parte del tiempo es, en rigor, desperdicio.

### Opción D — `pg_cron` dentro de PostgreSQL

**A favor**

- Cero infraestructura adicional: la propia base ejecuta la tarea.
- Ideal para purgas, que son SQL puro.

**En contra**

- Depende de que el proveedor de base gestionada permita la extensión, lo que
  no está garantizado y ata la decisión a ese proveedor.
- Solo sirve para tareas expresables en SQL.
- La lógica periódica queda repartida entre la base y la aplicación, que es
  justo donde nadie la busca cuando falla.

## 3. Decisión

**Se retira Airflow.** Las tareas periódicas las ejecutará un servicio en Go con
planificador interno (opción C).

### 3.1 Por qué no Airflow

La pregunta del enunciado tiene una respuesta corta: **no, no se justifica.**

Airflow resuelve problemas reales —grafos de dependencias, *backfill*,
reintentos con política, visibilidad para equipos de datos— y ninguno de esos
problemas existe aquí. Tres tareas independientes entre sí no forman un grafo. No hay histórico que rellenar. No hay un equipo de datos.

Adoptar una herramienta por lo que podría necesitarse algún día tiene un costo
que se paga todos los días, y lo paga un equipo municipal reducido que no tiene
capacidad para operar infraestructura accesoria.

### 3.2 La indexación en Elasticsearch deja de ser una tarea periódica

Esta es la consecuencia de fondo, y es la que más debilita el caso de Airflow.

La forma habitual de llevar los eventos a Elasticsearch es una copia periódica
desde PostgreSQL. Con frecuencia horaria, eso significa que una búsqueda puede
no encontrar un incidente ocurrido cincuenta minutos antes. Para una plataforma
de seguridad ciudadana, ese retraso es difícil de defender.

Pero el sistema ya tiene los eventos circulando por Kafka. Indexarlos no
requiere leer la base periódicamente: requiere **un consumidor más**, con la
misma forma que el persister.

```
Kafka ──▶ Persister ──▶ PostgreSQL   (archivo autoritativo)
      └──▶ Indexer   ──▶ Elasticsearch (búsqueda, casi en tiempo real)
```

Ventajas sobre la copia periódica:

- La búsqueda pasa de una hora de retraso a segundos.
- Desaparece la tarea periódica más compleja de las tres.
- Elasticsearch deja de depender de PostgreSQL: si la base falla, la
  indexación continúa.
- Reutiliza el patrón ya construido y probado.

Con eso, **el planificador queda con dos tareas nocturnas** (hoy son tres, ver 3.3), y sostener Airflow
para eso resulta indefendible.

### 3.3 Lo que hará el servicio de tareas

| Tarea | Horario | Descripción |
|---|---|---|
| **Agregados diarios** | Diaria, 03:00 | Calcula recuentos por zona y tipo del día anterior y los guarda en tablas de resumen |
| **Retención** | Diaria, 03:30 | Aplica la política de [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube): elimina el seudónimo de placa a los 90 días, las coordenadas al año, y borra el evento a los dos, dejando solo el agregado. Desde el 2026-10-21 también borra los recorridos guardados (365 días) y los avisos (730 días): [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) §4.3 |
| **Recorridos de alertas** | Cada minuto | Calcula con OSRM el recorrido de las alertas con botón de pánico y un solo vehículo y lo recalcula cuando el rastro crece ([ADR-012](#adr-012-recorridos-por-calle-osrm-en-la-capa-de-borde)) |

El orden no es arbitrario: **los agregados corren antes que la retención**. Si
la purga borrara los eventos primero, el resumen del día saldría incompleto, y
esa pérdida sería irreversible porque el agregado es justamente lo que
sobrevive al borrado.

**Actualización (2026-10-14):** el planificador solo sabía de tareas diarias a hora
fija y se extendió con tareas **por intervalo**. El cerrojo cambia de forma: el de
las diarias lleva la fecha y dura casi un día; el de las de intervalo lleva el
número de ventana y dura lo que el intervalo. No hay todavía pruebas automáticas
de las tareas por intervalo: las 10 del planificador cubren las diarias.

Ambas se ejecutarán con **bloqueo distribuido en Redis** (`SET NX` con TTL),
para que dos instancias del servicio no ejecuten la misma purga a la vez. Es el
mismo mecanismo que ya usan el ingestor y el correlator para la idempotencia
([ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos)).

Cada ejecución emitirá métricas —duración, filas afectadas, resultado— de modo
que un fallo dispare una alerta por el canal ya definido, sin sistema de avisos
propio.

### 3.4 Cuándo habría que revisar esta decisión

Un ADR que descarta una herramienta debe decir qué la traería de vuelta. Esta
decisión debería reconsiderarse si ocurre cualquiera de estas cosas:

- Las tareas periódicas superan la decena, o aparecen **dependencias entre
  ellas** ("ejecutar B solo si A terminó bien").
- Se necesita **backfill de rangos**. El caso simple ya está cubierto: la
  ejecución manual acepta una fecha concreta
  (`POST /tareas/agregados_diarios/ejecutar?dia=2026-09-15`), que resuelve
  recalcular el día que falló. Reprocesar un mes entero seguiría siendo
  manual.
- **Personal no desarrollador** necesita consultar el historial de ejecuciones
  o modificar horarios sin tocar código.
- Aparecen flujos de datos con ramificaciones o reintentos condicionales.

El primero y el tercero son los más probables si la Municipalidad amplía el
sistema a otras áreas. Llegado ese punto, la alternativa a evaluar no sería
necesariamente Airflow: existen orquestadores más ligeros con interfaz web.

### 3.5 Estado de implementación

**El `indexer` y el servicio `jobs` están construidos y verificados.**

| Componente | Estado |
|---|---|
| Servicio `indexer` (Kafka → Elasticsearch) | **Listo y verificado** |
| Servicio `jobs` con las dos tareas nocturnas y la de recorridos | **Listo y verificado** |
| Tablas de agregados y política de retención en SQL | **Listo y verificado** |

#### Verificación del servicio de tareas

**Los agregados cuadran exactamente con los datos crudos**, y son idempotentes:

| | Agregado | Crudo |
|---|---|---|
| Eventos del día | 3 334 | 3 334 |
| Alertas del día | 90 | 90 |

Repetir la ejecución no duplica filas: 25 filas de eventos y 20 de alertas
antes y después. El `ON CONFLICT DO UPDATE` recalcula en lugar de insertar.

**La degradación progresiva de la retención funciona en sus tres fases.** Se
verificó con tres eventos sintéticos de distinta antigüedad:

| Antigüedad | Placa | Coordenadas | Zona | Evento |
|---|---|---|---|---|
| 100 días | **eliminada** | conservada | conservada | conservado |
| 400 días | **eliminada** | **eliminada** | conservada | conservado |
| 800 días | — | — | — | **borrado** |

Los 3 334 eventos del día quedaron intactos, con sus 728 placas. La purga no
tocó nada que estuviera dentro de plazo.

**El planificador está probado**: 10 pruebas automáticas, incluidas las del
cerrojo contra un Redis real. Cubren que la tarea corra una sola vez al día
aunque se revise el minuto varias veces, que un fallo libere el cerrojo para
poder reintentar el mismo día, que no corra fuera de su hora, y que una zona
horaria mal escrita impida arrancar en lugar de desplazar las ejecuciones en
silencio.

#### Verificación del indexer

Medido el 21 de septiembre de 2026 sobre el sistema en ejecución.

**Integridad en los tres destinos**, sin discrepancia alguna:

| | Kafka | PostgreSQL | Elasticsearch |
|---|---|---|---|
| Eventos | 2 736 | 2 736 | **2 736** |
| Alertas | 70 | 70 | **70** |

**El retraso de indexación pasó de una hora a un segundo.** Medición en estado
estable, calculada por diferencia entre dos lecturas del histograma para evitar
la contaminación del arranque:

| Enfoque | Retraso evento → disponible para búsqueda |
|---|---|
| Copia periódica horaria desde la base | hasta 3 600 s |
| Consumidor de Kafka (lo implementado) | **1,01 s** de media |

El valor se corresponde con lo esperado: `BATCH_TIMEOUT` es de 2 segundos, de
modo que la espera media de un evento hasta que su lote se cierra es de
aproximadamente un segundo.

**El mapeo funciona**, que es donde una copia genérica suele fallar:

```
geo        -> geo_point
timestamp  -> date
zone       -> keyword
score      -> float
```

Una consulta geoespacial real —eventos a menos de 5 km de la zona 10— devolvió
2 198 resultados. Sin promover `geo` a `geo_point`, esa consulta no sería
posible: Elasticsearch deduciría los tipos por su cuenta y el campo quedaría
inutilizable como geolocalización.

**Los seudónimos coinciden entre destinos.** Se verificó sobre tres eventos que
el token de la placa es idéntico en PostgreSQL y en Elasticsearch, lo que
confirma que ambos servicios usan la misma llave HMAC. Si difirieran, el mismo
vehículo tendría identidades distintas en cada destino y las búsquedas cruzadas
fallarían en silencio.

**Los scores conservan su precisión** en el índice: 0.95 para incendio, 0.90
para exceso de velocidad, 0.85 para posible robo, y 0.86 de media para
accidente —la mezcla de los que llevan caída de velocidad y los que no.

## 4. Consecuencias

### Positivas

- Se elimina el componente de mayor huella y mayor carga operativa del stack
  original.
- Un solo lenguaje en todo el sistema: sin ecosistema Python que auditar.
- Las tareas periódicas se observan y se alertan por el mismo canal que el
  resto ([ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio)).
- La búsqueda pasa de una hora de retraso a segundos.
- Elasticsearch deja de depender de PostgreSQL.

### Negativas aceptadas

- **Se pierde la interfaz web.** No hay pantalla donde ver si la tarea de anoche
  corrió; hay que consultar métricas o registros. Para un equipo municipal es
  una pérdida real, y se mitiga solo en parte con un panel en Grafana.
- **Hay que escribir la planificación, los reintentos y el bloqueo.** Es código
  propio que mantener, frente a funcionalidad que Airflow daba resuelta.
- **Backfill limitado a un día.** La ejecución manual acepta una fecha, pero
  recuperar varios días seguidos exige repetir la llamada para cada uno.
- **Se desvía de la herramienta sugerida por el enunciado**, lo que obliga a
  defender la decisión en lugar de apoyarse en ella.
- **Dos servicios más** que desplegar, aunque su huella conjunta sea una
  fracción de la de Airflow.

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

**No usar Apache Airflow**, la herramienta de programación de tareas que
contemplaba el planteamiento original, y sustituirla por un programa propio,
mucho más pequeño.

### Por qué

Airflow es una herramienta potente pensada para organizaciones que ejecutan
decenas o cientos de procesos automáticos con dependencias entre sí. Este
sistema necesita **tres tareas: dos una vez al día y una cada minuto**.

Instalarla exigiría tres programas adicionales funcionando de forma permanente
—incluido uno con su propia base de datos— que en conjunto consumirían más
recursos que todo el resto del sistema junto. Sobre equipos comprados por la
Municipalidad, que no se pueden ampliar sobre la marcha, esos recursos hacen
falta en otro sitio.

También requeriría mantenimiento propio: actualizaciones, gestión de usuarios y
los fallos de arranque por desfase de esquema, que son una fuente recurrente de
incidencias.

### Un beneficio adicional

Al revisar las tareas se descubrió que **la búsqueda de incidentes históricos se
actualizaba solo una vez por hora**. Con el nuevo enfoque pasa a actualizarse en
segundos: alguien que busque un incidente lo encontrará prácticamente en cuanto
ocurra, en lugar de esperar hasta una hora.

Para una plataforma de seguridad ciudadana, esa diferencia es sustantiva.

### Qué se pierde

**La pantalla de supervisión.** Airflow ofrece una interfaz donde ver si cada
proceso corrió y cuándo falló, sin conocimientos técnicos. El programa propio
no la tiene: sus resultados se consultan en los tableros generales del sistema.

Si en el futuro la Municipalidad necesita que personal administrativo gestione
estas tareas por sí mismo, conviene reconsiderar esta decisión. Queda anotado
en el documento técnico cuándo hacerlo.

### Estado

**Construido y verificado.** Las dos tareas nocturnas, la de recorridos y el proceso
de búsqueda funcionan, y se comprobaron con datos reales del sistema en ejecución.

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| Un fallo silencioso de la tarea nocturna | Baja | Medio | Cada ejecución registra la hora de su último éxito; si deja de actualizarse, salta una alerta |
| Una purga mal configurada borra datos vigentes | Baja | **Alto**: el borrado es irreversible | El servicio no arranca si la política es incoherente; se verificó la degradación sobre datos de prueba |
| Necesidad futura de supervisión sin conocimientos técnicos | Media | Bajo | Criterios de reconsideración documentados |

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — la pregunta sobre la complejidad
  operativa de Airflow a esta escala.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — el hardware on-premise es la
  restricción que pesa sobre la huella.
- [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) — la política de retención es
  la tarea periódica principal.
- [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) — sustituye por completo al DAG de
  chequeos de salud.
- `services/jobs/` — implementación del planificador y las tres tareas.

---

# ADR-010: Contenerización y orquestación — Docker Compose, sin Kubernetes

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (el corte híbrido define las capas a desplegar), [ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go) (el binario estático determina el tamaño de la imagen), [ADR-003](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes) (cada servicio se despliega por separado)

---

## 1. Contexto

El sistema son seis servicios propios más siete componentes de infraestructura
(Kafka, Redis, PostgreSQL, Elasticsearch, Prometheus, Grafana y el motor de rutas
OSRM) repartidos en
dos capas por el corte híbrido del
[ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube). Hay que decidir cómo se empaquetan
y cómo se ponen en marcha.

El enunciado plantea la disyuntiva en la sección 4.2 y añade una frase que vale
la pena tomar en serio: *"defender tanto la adopción como la decisión
consciente de no usar Kubernetes es válido"*. Es decir, lo que se evalúa no es
cuál se elige sino si la elección responde al caso de negocio declarado.

Ese caso está fijado en el [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube):
Municipalidad de Guatemala, dos servidores propios en la LAN municipal, una
capa analítica en la nube, y **un equipo de sistemas reducido** que heredará la
operación cuando el proyecto termine.

### Lo que el sistema ya sabe de su propia carga

La decisión de orquestación suele tomarse antes de conocer la carga real. Aquí
no hace falta, porque está medida:

| | Resultado |
|---|---|
| Volumen estimado en operación | 20 – 100 eventos/s |
| Ráfaga probada | 100 ev/s durante 10 s, 1 192 peticiones |
| Respuestas fuera de `202` | ninguna |
| Latencia p99 bajo ráfaga | 19,7 ms |
| Instancias necesarias para absorberla | **una de cada servicio** |
| Memoria residente por servicio | 13 – 18 MB |
| Arranque de un servicio | 7 – 50 ms |

Es un dato que condiciona todo lo demás: **el escalado horizontal automático,
que es el problema central que Kubernetes resuelve, no es un problema que este
sistema tenga.** Y si lo tuviera, el límite no sería el número de procesos sino
las tres particiones de Kafka del [ADR-004](#adr-004-broker-de-eventos-kafka-en-modo-kraft), porque
un grupo de consumo no puede tener más consumidores activos que particiones.

## 2. Opciones consideradas

### Opción A — Sin contenedores: binarios y unidades `systemd`

Los servicios en Go son binarios estáticos; técnicamente no necesitan
contenedor.

**A favor**

- Lo más liviano posible: sin capa de contenedor, sin demonio de Docker.
- `systemd` ya reinicia procesos caídos, y el equipo de sistemas de la
  Municipalidad lo conoce.

**En contra**

- **La infraestructura no son binarios.** Kafka, Elasticsearch y Grafana
  seguirían necesitando instalación propia, con sus versiones de Java y sus
  dependencias del sistema operativo.
- Se pierde la reproducibilidad: "funciona en mi máquina" vuelve a ser un
  problema real, y la jornada de simulación se monta sobre equipos ajenos.
- La tabla de nodos del enunciado (sección 6) mezcla Ubuntu y macOS. Sin
  contenedores, cada uno exige su propio procedimiento.

### Opción B — Kubernetes gestionado (EKS, GKE, AKS)

**A favor**

- Reprogramación automática: si un nodo muere, los pods se levantan en otro.
- Despliegues progresivos sin interrupción, con reversión automática si las
  sondas fallan.
- Escalado horizontal por métrica.
- Es el estándar de la industria, y lo que el equipo encontrará en su vida
  profesional.

**En contra**

- **El plano de control vive en la nube, y eso roza lo que el
  [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) protege.** Con el enlace caído los
  pods ya en marcha siguen corriendo —conviene ser preciso, no es que el
  sistema se detenga—, pero se pierde la capacidad de reprogramar, escalar o
  desplegar hasta que vuelva. Justo en el momento en que más falta haría.
- Costo permanente: entre el plano de control y los nodos gestionados, del
  orden de cientos de dólares mensuales para mover 100 eventos por segundo que
  un servidor propio absorbe sin despeinarse.
- **Superficie operativa permanente**: CNI, ingress, almacenamiento
  persistente, rotación de certificados y actualizaciones cada pocos meses. No
  es trabajo de instalación, es trabajo de todos los meses.

### Opción C — Kubernetes ligero on-premise (K3s)

El punto intermedio honesto: la API de Kubernetes sobre los servidores propios.

**A favor**

- Conserva la reprogramación entre nodos sin depender del enlace ni pagar un
  plano de control gestionado.
- Un solo binario, mucho más liviano que una distribución completa.
- Si más adelante se migra a Kubernetes gestionado, los manifiestos ya están
  escritos.

**En contra**

- **Con dos nodos, la alta disponibilidad es en buena medida aparente.** Un
  quórum de `etcd` necesita tres; con dos, la caída del nodo equivocado deja el
  clúster sin plano de control.
- La complejidad conceptual es la de Kubernetes completo: el equipo que opere
  el sistema debe entender pods, servicios, volúmenes persistentes y políticas
  de red aunque el binario sea pequeño.
- Los componentes con estado —Kafka, PostgreSQL, Elasticsearch— son
  precisamente los que peor encajan, y acaban anclados a un nodo por su volumen
  persistente, que es donde la reprogramación deja de servir.

### Opción D — Docker Compose

**A favor**

- Reproducibilidad de contenedores sin plano de control que operar.
- **Un archivo por capa de despliegue**, lo que hace visible el corte híbrido:
  `edge` es literalmente lo que corre en la LAN municipal.
- `restart: unless-stopped` y las comprobaciones de salud cubren la
  autorreparación a nivel de contenedor, que es el modo de fallo frecuente.
- El equipo lo conoce, y la jornada de simulación se levanta con una orden.

**En contra**

- **No reprograma entre máquinas.** Si el servidor muere, nada mueve los
  servicios: hay que hacerlo a mano.
- Sin despliegue progresivo: recrear un contenedor deja un hueco de segundos.
- Sin escalado automático por métrica.

## 3. Decisión

**Se adopta Docker Compose (opción D), y se descarta Kubernetes de forma
consciente.**

### 3.1 El argumento, en una línea

Kubernetes resuelve bien tres problemas —escalar por demanda, reprogramar entre
muchos nodos y desplegar sin interrupción— y **este sistema, con este cliente,
tiene uno y medio de esos tres**.

No tiene el primero: está medido que una instancia de cada servicio absorbe el
doble del volumen esperado con un p99 de 19,7 ms. No tiene el segundo: hay dos
nodos, y los componentes con estado están anclados a su disco de todos modos.
Sí tiene el tercero a medias, y es la carencia que se acepta y se documenta más
abajo.

Adoptarlo significaría pagar una superficie operativa permanente —que hereda un
equipo reducido— a cambio de automatizar algo que a esta escala se hace a mano
una vez al mes.

### 3.2 Tres archivos, que son el corte híbrido hecho explícito

| Archivo | Dónde corre | Qué contiene |
|---|---|---|
| `docker-compose.edge.yml` | LAN municipal | Kafka (KRaft) y su inicializador, Redis, los seis microservicios, el motor de rutas OSRM, Prometheus, Grafana, Kafka-UI |
| `docker-compose.cloud.yml` | Nube | Elasticsearch, Kibana |
| `docker-compose.dev.yml` | Máquina del desarrollador | PostgreSQL local, en lugar de la base gestionada, y `postgres-migrate`, que aplica el esquema en cada arranque |

Que el corte del [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) coincida con la
frontera entre dos archivos no es cosmético: hace que la pregunta *"¿qué pasa
si se cae el enlace?"* se responda leyendo cuál de los dos sigue arriba.

Los servicios que cruzan la frontera (persister, indexer, notifier y jobs)
**corren en la capa `edge`** aunque escriban en la nube. Es deliberado: así la lectura de Kafka es
local y solo la escritura, ya por lotes, viaja por el enlace.

### 3.3 Qué aporta Compose que es suficiente

| Mecanismo | Uso en el proyecto | Qué cubre |
|---|---|---|
| `restart: unless-stopped` | 12 servicios en `edge`, 2 en `cloud`, 1 en `dev` | Un proceso que muere vuelve solo; sobrevive al reinicio del servidor |
| `healthcheck` | 9 servicios en `edge`, 1 en `cloud`, 1 en `dev` | Distingue *arrancado* de *listo*, que no es lo mismo en Kafka ni en Elasticsearch |
| `depends_on: condition: service_healthy` | 12 dependencias | Arranque ordenado: el ingestor no arranca antes que Kafka esté listo |
| `depends_on: condition: service_completed_successfully` | 9 dependencias | Espera a procesos de un solo uso: los topics de Kafka (`kafka-init`) y el esquema de la base (`postgres-migrate`) |

La tercera fila resuelve un problema concreto y muy visible en una demostración
en vivo: sin ella, los servicios arrancan a la vez, fallan al conectar y
dependen de que el reinicio los salve. Con ella, el sistema sube en orden a la
primera. La espera a `kafka-init` no es un detalle: sin ella, un consumidor que se
une antes de que exista su topic queda sin particiones y sin error
([ADR-004](#adr-004-broker-de-eventos-kafka-en-modo-kraft)).

### 3.4 Las imágenes

`distroless/static` sobre compilación en dos etapas. Las imágenes finales pesan
entre **22 y 29 MB**, frente a los 257 MB que ocupa una imagen base de Java antes siquiera de añadir la
aplicación. Es una referencia de contraste y no el motivo de haber elegido Go,
que fue una decisión del equipo ([ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go)).

Importa más allá del tamaño: una imagen `distroless` **no tiene intérprete de
comandos**. No se puede abrir una sesión dentro del contenedor, lo que reduce
lo que un atacante puede hacer si logra ejecutar algo. El precio es que
diagnosticar exige métricas y registros en lugar de entrar a mirar, que es
justamente la disciplina que sostiene el
[ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio).

### 3.5 La decisión es reversible, y ese es el punto

Es lo que permite descartar Kubernetes sin descartarlo para siempre.

Los seis servicios ya cumplen lo que Kubernetes espera de una aplicación:

| Propiedad | Estado hoy | Qué sería en Kubernetes |
|---|---|---|
| Sin estado en el proceso | Sí; el estado vive en Redis, Kafka y PostgreSQL | `Deployment` sin volúmenes |
| Configuración por variables de entorno | Sí, sin archivos de configuración | `ConfigMap` y `Secret` |
| Endpoint de salud | `/health` en los seis | Sondas de vitalidad y disponibilidad |
| Métricas expuestas | `/metrics` en los seis | Escrapeo por anotación |
| Cierre ordenado ante señal | Sí, con confirmación de offsets pendientes | `terminationGracePeriodSeconds` |
| Imagen mínima | 22 – 29 MB, distroless | Imagen de contenedor |

Migrar sería **escribir manifiestos, no reescribir servicios**. Las
comprobaciones de salud del Compose se traducen casi literalmente a sondas. No
hay una sola línea de código que dependa de Docker Compose.

### 3.6 Cuándo habría que reconsiderarlo

Escrito por adelantado, para que la revisión no dependa de que a alguien se le
ocurra:

- **Más de tres nodos** en la capa on-premise. A partir de ahí, coordinar a
  mano qué corre dónde deja de ser razonable.
- **Se exige despliegue sin interrupción** por acuerdo de nivel de servicio con
  la Municipalidad. Hoy la ventana de segundos es tolerable; si deja de serlo,
  hace falta despliegue progresivo.
- **Más de quince servicios**, o varios equipos desplegando de forma
  independiente.
- **Un segundo cliente**: si el sistema atiende a otra municipalidad sobre la
  misma infraestructura, el aislamiento por espacios de nombres pasa a valer lo
  que cuesta.

## 4. Consecuencias

### Positivas

- Sin plano de control que operar, actualizar ni diagnosticar.
- El archivo de despliegue es legible por cualquiera que sepa leer YAML, lo que
  importa cuando el sistema lo hereda un equipo reducido.
- La demostración en vivo se levanta con una orden, sin clúster previo.
- Imágenes de 22 a 29 MB: la transferencia al servidor de la Municipalidad es
  cuestión de segundos.
- Migrar a Kubernetes sigue abierto y no exige tocar código.

### Negativas aceptadas

- **No hay reprogramación entre nodos.** Si el servidor que hospeda la capa
  `edge` falla por hardware, el sistema se detiene hasta que alguien intervenga.
  Es la carencia más seria de esta decisión y no se disimula: se acepta porque
  un clúster de dos nodos tampoco la resolvería de verdad.
- **Sin despliegue progresivo.** Recrear un contenedor deja un hueco de
  segundos. Para el correlator y el persister es inocuo —consumen de Kafka y
  retoman desde su último offset—, pero **el ingestor sí rechaza conexiones
  durante ese hueco**, y los simuladores no reintentan. En operación real, los
  productores deberían hacerlo.
- **Escalar es manual.** Ajustar réplicas exige editar y volver a aplicar, y
  por encima de tres consumidores por grupo hace falta además subir las
  particiones de Kafka.
- **El equipo no practica Kubernetes en este proyecto**, que es un costo
  formativo real aunque no sea un costo del sistema.

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Empaquetar cada componente del sistema en una **unidad autocontenida** que
incluye todo lo que necesita para funcionar, y ponerlas en marcha con una
herramienta sencilla de arranque coordinado. Se evaluó y **se descartó
deliberadamente** la plataforma de orquestación estándar de la industria,
Kubernetes.

### Por qué se descartó

Esa plataforma resuelve principalmente el problema de **crecer y encogerse
automáticamente** ante cambios grandes de demanda, y de repartir el trabajo
entre muchos servidores. El sistema de la Municipalidad no tiene ese problema:
se sometió a una prueba con el doble del volumen esperado y una sola copia de
cada componente lo absorbió respondiendo en veinte milésimas de segundo.

A cambio, esa plataforma exige **atención especializada permanente**:
actualizaciones frecuentes, configuración de red propia y personal con
formación específica. Para un equipo de sistemas reducido, que es el que
heredará la operación, ese costo es continuo y no compra nada que el sistema
necesite hoy.

### Qué se pierde, dicho con claridad

Si **el servidor principal falla por avería física**, el sistema no se traslada
solo a otro equipo: alguien debe intervenir. Con la plataforma descartada, ese
traslado sería automático.

Es el riesgo consciente de esta decisión. Se acepta porque con solo dos
servidores esa automatización tampoco sería confiable, y porque la avería de un
servidor es un suceso infrecuente frente a un costo operativo que se paga todos
los meses.

**Mitigación disponible:** como cada componente es una unidad autocontenida,
levantar el sistema en un servidor de repuesto es cuestión de minutos, no de
reinstalar nada. El procedimiento debe quedar escrito y ensayarse una vez.

### Costo

**Ninguno adicional.** Las herramientas son libres y sin licencia. La
alternativa descartada habría supuesto entre cientos de dólares mensuales —si
se contrataba— y la dedicación parcial de una persona formada —si se operaba en
casa.

### Si el sistema crece

La decisión **no cierra la puerta**. Los componentes se construyeron cumpliendo
los requisitos que esa plataforma exige, de modo que adoptarla más adelante
sería trabajo de configuración, no de reprogramación. Los criterios que
dispararían esa revisión están escritos en el apartado 3.6.

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — contenerización y orquestación;
  la no adopción de Kubernetes es defendible si se justifica.
- [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) — el corte híbrido que los tres
  archivos de despliegue materializan.
- [ADR-002](#adr-002-lenguaje-y-runtime-de-los-microservicios-go) — el binario estático que permite la
  imagen distroless.
- [ADR-003](#adr-003-organización-del-repositorio-monorepo-con-módulos-independientes) — cada servicio se construye y
  despliega por separado.
- [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) — diagnosticar sin entrar al
  contenedor.
- `deploy/docker-compose.edge.yml`, `deploy/docker-compose.cloud.yml`,
  `deploy/docker-compose.dev.yml` — las tres capas.
- `deploy/README.md` — procedimiento de puesta en marcha.

---

# ADR-011: Aviso a terceros — servicio propio con registro de despacho

**Estado:** aceptado
**Fecha:** 2026-10-03
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-004](#adr-004-broker-de-eventos-kafka-en-modo-kraft) (el aviso se dispara desde `correlated.alerts`), [ADR-005](#adr-005-persistencia-postgresql-relacional-con-elasticsearch-para-búsqueda) (el registro vive en PostgreSQL), [ADR-006](#adr-006-resiliencia-y-tolerancia-a-fallos) (idempotencia ante reentrega), [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) (canal de aviso: son dos huecos distintos, no el mismo)

---

## 1. Contexto

Hasta ahora el sistema respondía "qué ocurrió" y no respondía "a quién se le
dijo". Una alerta de incendio y una alerta de incendio atendida eran el mismo
registro en la base: `alerts` guarda el hecho detectado y nada más.

Esa carencia estaba identificada. El [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio)
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
([ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) §4.3), y crece con las alertas, no con los eventos: a razón de
dos o tres filas por alerta, su volumen es irrelevante frente a `events`.
Cuando exista integración real, el reintento tiene ya su columna (`intentos`) y falta el bucle; el acuse
necesitará una columna nueva.

---

## Referencias

- `services/notifier/` — implementación completa
- `deploy/sql/002_trazabilidad.sql` — tabla `notifications` y sus restricciones
- `deploy/grafana/provisioning/dashboards/04-trazabilidad.json` — tablero
- [ADR-008](#adr-008-observabilidad-y-objetivos-de-nivel-de-servicio) §"Lo que falta" — el otro canal, el del equipo que opera el sistema, que sigue pendiente

---

# ADR-012: Recorridos por calle — OSRM en la capa de borde

**Estado:** aceptado
**Fecha:** 2026-10-07
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube) (por qué el motor vive en la LAN), [ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) (las coordenadas son dato personal), [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow) (quién calcula y cuándo)

---

## 1. Contexto

El mapa de incidentes unía los puntos donde una cámara vio un vehículo con
líneas rectas, que cortan por encima de las manzanas. La petición es que el
recorrido siga las calles, como una ruta de navegador.

Antes de resolverlo técnicamente hay que decir qué se está dibujando, porque
determina el resto de la decisión:

> **El sistema no sabe por dónde circuló el vehículo.** Sabe que una cámara lo
> captó en tres puntos. Cualquier línea entre ellos —recta o por calles— es una
> interpolación.

La diferencia es que la recta *se nota* que es una simplificación, y la ruta por
calles no: parece un recorrido medido. Esa es la contrapartida real de esta
decisión y conviene que figure por escrito.

## 2. Opciones consideradas

### Opción A — Dejar la línea recta

Cero coste. Y una virtud que se pierde al cambiar: nadie confunde una recta
que atraviesa seis manzanas con el camino que tomó un coche.

Se descartó porque no responde a la pregunta operativa. Un agente que mira el
mapa necesita saber por qué calles pudo escapar el vehículo para decidir dónde
interceptarlo, y una recta no se lo dice.

### Opción B — API externa de rutas

Un servicio como OpenRouteService o el OSRM público. Se monta en una tarde.

Se descartó por dos razones, y la segunda pesa más que la primera.

La primera es de disponibilidad: el [ADR-001](#adr-001-plataforma-de-despliegue-híbrido-lan-nube)
sostiene que la detección no depende del enlace a internet, y buena parte de la
defensa del diseño híbrido descansa en eso. Un mapa que deja de funcionar
cuando se cae la conexión contradice el argumento.

La segunda es de privacidad. Pedir una ruta significa **enviar a un tercero las
coordenadas exactas y la hora de un incidente de seguridad ciudadana**. El
[ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube) se toma el trabajo de que la
placa no cruce a la nube sin seudonimizar; mandar las ubicaciones a un servicio
externo por comodidad habría vaciado ese esfuerzo por otro lado.

### Opción C — OSRM en la capa de borde

Un contenedor con el mapa de Guatemala de OpenStreetMap, preprocesado una vez.
Calcula rutas reales sin salir de la red municipal.

Cuesta un contenedor más, 131 MB de descarga inicial y unos minutos de
preprocesado.

## 3. Decisión

**Opción C.** El motor de rutas vive en la LAN, junto al correlator.

### 3.1 Los datos se preparan aparte del arranque

`./make.sh rutas-preparar` descarga el mapa y construye los índices. **No** es
parte de `up`.

Meterlo en el arranque habría significado que levantar el sistema para una
demostración depende de poder descargar 131 MB, y que un volumen recreado
obliga a esperar varios minutos antes de ver nada. Los datos viven en
`deploy/osrm-data`, que está en `.gitignore`: un mapa de OpenStreetMap no se
versiona, se descarga.

Se usa el algoritmo MLD y no CH porque permite recalcular los pesos sin rehacer
el índice entero, que es lo que haría falta el día que se quisieran incorporar
cortes de calle o tráfico.

### 3.2 Calcula `jobs`, no el correlator

Pedir una ruta es una llamada HTTP a un motor externo. Ponerla en el camino
crítico de la detección significaría que **un fallo del motor de rutas retrasa
la emisión de una alerta de incendio**. El recorrido es presentación; la alerta
es la función del sistema.

Por eso lo calcula una tarea periódica. Eso obligó a extender el planificador
del [ADR-009](#adr-009-orquestación-de-tareas-periódicas-retirar-airflow), que solo sabía de
tareas diarias a hora fija, con tareas **por intervalo**. La distinción no es
caprichosa: una tarea diaria cierra el día, una tarea por intervalo acompaña al
flujo. Calcular a las tres de la madrugada el recorrido de un incidente de media
tarde no le sirve a nadie, porque el operador ya miró el mapa y lo vio vacío.

El cerrojo también cambia de forma. El de las tareas diarias lleva la fecha y
dura casi un día, para que la purga corra una sola vez. El de las tareas por
intervalo lleva el **número de ventana** y dura lo que el intervalo, de modo
que el propio cerrojo marca el ritmo: el planificador consulta cada minuto y
solo entra quien toma la ventana. Sin eso habría que guardar en memoria la
última ejecución, que se pierde en cada reinicio y no sirve con varias
instancias.

### 3.3 Se guarda el resultado, incluidos los fracasos

La tabla `alert_routes` registra tres estados: `calculada`, `sin_ruta` y
`fallida`.

Guardar solo los éxitos habría hecho que la tarea reintentara lo mismo cada
minuto para siempre, y nadie podría distinguir una alerta cuyo recorrido aún no
se ha calculado de otra para la que no existe. `sin_ruta` no se reintenta
—ese resultado no va a cambiar—; `fallida` sí, hasta tres veces, porque el
fallo típico es que el motor estaba arrancando.

La geometría se guarda como un array JSON de pares `[lon, lat]` en **una sola
fila por alerta**, no una fila por vértice. Un recorrido urbano tiene cientos de
vértices y desgranarlos multiplicaría el tamaño de la tabla sin que nadie
consulte jamás un vértice suelto: siempre se lee la ruta entera.

### 3.3 bis El recorrido cubre el rastro completo, y se recalcula

La alerta se emite en cuanto el patrón se completa, y el vehículo sigue
circulando después. Una ruta calculada una sola vez, al nacer la alerta,
terminaría en la última evidencia y no en el último contacto, que es justo lo
que interesa: por dónde fue y dónde se le perdió la pista.

Por eso el recorrido pasa por **las evidencias y por el rastro posterior del
vehículo**, que termina cuando entre dos apariciones pasan más de 30 minutos
(`RUTAS_SILENCIO_MIN`). Es una política y no una medida: si un coche reaparece
pasado ese plazo, eso ya no es la misma huida. El tablero usa el mismo valor
escrito en su propia consulta, de modo que si se cambia en uno hay que cambiarlo
en el otro.

La ventana de correlación (15 minutos) y el rastro (hasta 30 de silencio) son
cosas distintas y no deben confundirse. La ventana es de **detección**: tiene
que ser corta, porque una alerta de robo que llega media hora tarde no sirve. El
rastro es de **reconstrucción**: se hace sobre el archivo y no tiene por qué
heredar ese límite.

`alert_routes` guarda con qué se calculó cada ruta —cuántos puntos y qué
intervalo abarcan—, y la tarea la rehace cuando el rastro tiene más puntos que
ella. Sin eso la línea se quedaría atrás conforme el vehículo sigue apareciendo
en cámaras. El contador de intentos solo sube cuando el cálculo *falla*: si
subiera con cada recálculo correcto, una ruta que se ha ido alargando acabaría
con los intentos agotados y, si el siguiente fallara, no se reintentaría nunca.

Lo que el sistema no puede decir es **por qué** se perdió el contacto: si el
vehículo se detuvo, salió de la ciudad o sencillamente dejó de haber cámaras.
Distinguirlo exigiría un inventario de cobertura de sensores que no existe.

### 3.3 ter Cuándo se traza un recorrido, y cuándo no

Un recorrido es la trayectoria de **un** vehículo que se aleja de un hecho. Solo
se calcula si la alerta incluye un botón de pánico y exactamente un vehículo
identificado. En los demás casos la fila de `alert_routes` queda en `sin_ruta`
con un detalle que dice «no aplica».

La primera versión calculaba una ruta para toda alerta, y la verificación de
extremo a extremo lo cazó: en un incendio las evidencias son un reporte y un
sensor acústico en el mismo instante, y en un exceso de velocidad son tres coches
distintos. Unirlos con una ruta por calles dibuja una línea que nadie recorrió
(«el recorrido estimado son 125 s y solo pasaron 0»). No se veía porque el tablero
solo enseña rutas de incidentes con pulsador, pero estaban en la base.

Se deja la fila explícita en lugar de omitir la alerta: sin ella parecería tener
el cálculo pendiente para siempre, y nadie distinguiría «todavía no» de «no
aplica».

Un pulsador que no generó ninguna alerta —el falso positivo del enunciado— sí
aparece en el mapa, como un único punto INICIO. Un mapa vacío parece un fallo del
tablero cuando es justo lo que hay que enseñar.

### 3.3 quater Un tramo imposible no se dibuja

Una cámara que lee mal un carácter de la placa produce, a veces, la matrícula de
otro coche, y esa lectura aparece en el rastro de un vehículo al que no
pertenece. Unirla con una ruta por calles dibuja un salto de varios kilómetros en
segundos que ningún coche pudo hacer.

El rastro se filtra por plausibilidad física antes de calcular la ruta: se
descarta una lectura si la velocidad implícita desde la anterior supera
`RUTAS_VEL_MAX_KMH` (70 km/h medidos en línea recta, con un margen de 0,3 km por el
error de posición). El tablero aplica el mismo criterio en su consulta, de modo que
si se cambia en un sitio hay que cambiarlo en el otro. El umbral se calibró con el
propio motor de rutas: a flujo libre por las calles de la capital, la velocidad en
línea recta salió entre 26 y 34 km/h. El correlator aplica la misma regla para no
encadenar lecturas imposibles en una persecución
([ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube), `docs/correlacion.md`).

El recorrido parte del momento del pánico y recoge solo lo posterior: lo que el
vehículo hiciera antes no es una huida de ese incidente.

### 3.4 Qué se dibuja y qué no

El panel superpone dos capas:

| Capa | Qué es | Origen |
|---|---|---|
| Marcas | Los puntos donde una cámara vio el vehículo | `events.geo_lat/geo_lon` — **medido** |
| Línea | El camino más probable entre esos puntos | `alert_routes.geometria` — **estimado** |

La geometría guardada es ubicación, y por eso la retención la elimina a los 365
días, el plazo de las coordenadas ([ADR-007](#adr-007-privacidad-seudonimización-de-placas-en-la-frontera-lannube)
§4.3).

La distinción está en el esquema, en la descripción del panel y en este documento. No está en el color de la línea: se decidió dibujarla como una ruta
normal, sólida, porque es lo que hace legible el recorrido. Es una decisión
consciente y tiene un riesgo asociado, que se enumera abajo.

## 4. Consecuencias

### Positivas

- El recorrido de un incidente se lee como una ruta de navegador: se ve por
  dónde pudo escapar el vehículo y hacia dónde iba.
- Funciona sin internet, igual que la detección.
- Las coordenadas de los incidentes no salen de la red municipal.
- El planificador gana tareas por intervalo, que es una capacidad que faltaba
  y que otras tareas podrán usar.

### Negativas aceptadas

- **La ruta es una hipótesis dibujada como un hecho.** El sistema no midió ese
  camino. Si alguien pregunta en la defensa «¿cómo saben que fue por ahí?», la
  respuesta honesta es: no lo sabemos, es el camino más probable entre tres
  puntos que sí medimos. Mitigarlo visualmente —línea punteada, leyenda de
  «estimado»— se consideró y se descartó a favor de la legibilidad.
- **Un contenedor y 131 MB más.** El preprocesado tarda varios minutos y hay
  que acordarse de ejecutarlo en una instalación nueva.
- **El mapa envejece.** OpenStreetMap cambia; el extracto descargado no. Una
  calle nueva no aparecerá hasta que alguien rehaga los índices.
- **El contraste con el tiempo real es parcial.** La verificación compara la
  duración que estima el motor con el tiempo transcurrido entre el primer y el
  último punto, y falla si el trayecto no cabía. Lo hace sobre las rutas
  calculadas, no al momento de emitir la alerta: el correlator todavía no
  consulta el callejero: aplica solo el filtro de plausibilidad en línea recta de
  la sección 3.3 quater, que es más permisivo que el tiempo por calle.

## 5. Impacto en el negocio

### Qué se decidió

Que el recorrido de un incidente se dibuje siguiendo las calles, calculado
dentro de la red municipal.

### Por qué

Porque un mapa con líneas rectas sirve para contar incidentes y no para
perseguir uno. La diferencia entre «el vehículo fue visto en estos tres puntos»
y «el vehículo pudo tomar esta ruta» es la diferencia entre un informe y una
herramienta operativa.

### Costo

Ninguno recurrente. OpenStreetMap y OSRM son libres y el servicio corre sobre
el mismo equipo que el resto de la capa de borde. El único coste es el tiempo
de preparar los índices cuando se instala, y rehacerlos cuando se quiera un
mapa actualizado.

### Si el sistema crece

El extracto de Guatemala entero ocupa poco y el motor responde en
milisegundos. Si un día se quisieran incorporar cortes de calle o estado del
tráfico, el algoritmo elegido (MLD) permite recalcular los pesos sin rehacer el
índice.

---

## Referencias

- `deploy/preparar-rutas.sh` — descarga y preprocesado
- `deploy/sql/003_rutas.sql` — tabla `alert_routes`
- `services/jobs/internal/rutas/osrm.go` — cliente del motor
- `services/jobs/internal/scheduler/scheduler.go` — tareas por intervalo
