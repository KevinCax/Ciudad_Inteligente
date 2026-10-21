# ADR-009: Orquestación de tareas periódicas — retirar Airflow

**Estado:** aceptado e implementado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-007](007-privacidad-y-datos-personales.md) (política de retención), [ADR-008](008-observabilidad-y-slo.md) (sustituye a los chequeos de salud)

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
[ADR-008](008-observabilidad-y-slo.md) eso sería redundancia sin beneficio:
Prometheus recoge el estado cada diez segundos y **catorce reglas de alerta**
avisan sin que nadie mire. Una tarea periódica comprobaría peor y avisaría a
nadie.

**Limpieza de claves en Redis.** La guía del curso la sugiere, pero el diseño
de las ventanas ([ADR-006](006-resiliencia-y-tolerancia-a-fallos.md)) poda por
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
| Purga y degradación del histórico | Diaria | [ADR-007](007-privacidad-y-datos-personales.md) |
| Agregados diarios por zona y tipo | Diaria | Informes municipales |
| Indexación en Elasticsearch | — | Ninguna: la resuelve un consumidor (sección 3.2) |
| Limpieza de claves en Redis | **Ninguna** | Los TTL lo resuelven solos |
| Chequeos de salud | **Ninguna** | [ADR-008](008-observabilidad-y-slo.md) |
| Recorridos por calle de las alertas | Cada minuto | [ADR-012](012-recorridos-por-calle.md) (posterior a esta decisión) |

**El trabajo periódico real eran dos tareas nocturnas.** Desde el
[ADR-012](012-recorridos-por-calle.md) hay una tercera, de otra naturaleza: no cierra
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
  propio y no elástico ([ADR-001](001-plataforma-de-despliegue.md)), eso compite
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
  [ADR-008](008-observabilidad-y-slo.md) en lugar de exigir un sistema de avisos
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
| **Retención** | Diaria, 03:30 | Aplica la política de [ADR-007](007-privacidad-y-datos-personales.md): elimina el seudónimo de placa a los 90 días, las coordenadas al año, y borra el evento a los dos, dejando solo el agregado. Desde el 2026-10-21 también borra los recorridos guardados (365 días) y los avisos (730 días): [ADR-007](007-privacidad-y-datos-personales.md) §4.3 |
| **Recorridos de alertas** | Cada minuto | Calcula con OSRM el recorrido de las alertas con botón de pánico y un solo vehículo y lo recalcula cuando el rastro crece ([ADR-012](012-recorridos-por-calle.md)) |

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
([ADR-006](006-resiliencia-y-tolerancia-a-fallos.md)).

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
  resto ([ADR-008](008-observabilidad-y-slo.md)).
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
- [ADR-001](001-plataforma-de-despliegue.md) — el hardware on-premise es la
  restricción que pesa sobre la huella.
- [ADR-007](007-privacidad-y-datos-personales.md) — la política de retención es
  la tarea periódica principal.
- [ADR-008](008-observabilidad-y-slo.md) — sustituye por completo al DAG de
  chequeos de salud.
- `services/jobs/` — implementación del planificador y las tres tareas.
