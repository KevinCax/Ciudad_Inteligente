# ADR-009: Orquestación de tareas periódicas — retirar Airflow

**Estado:** aceptado (indexer implementado; servicio `jobs` pendiente)
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

El sistema Java de referencia usaba Airflow 2.9 con tres DAGs. La decisión aquí
es si el sistema en Go lo hereda.

### Qué hacía Airflow en el sistema Java

| DAG | Frecuencia | Qué hacía |
|---|---|---|
| `healthcheck_dag` | cada 2 min | `curl` al correlator y `nc` a Kafka |
| `etl_analytics_dag` | cada 15 min | Un `SELECT ... GROUP BY` sobre `alerts` |
| `pg_to_es_etl_dag` | cada hora | Copiar eventos y alertas de PostgreSQL a Elasticsearch |

A cambio, Airflow exigía un servidor web, un planificador y una base de
metadatos —SQLite con `SequentialExecutor`, es decir, sin paralelismo—, además
de sus propias migraciones de esquema y su gestión de usuarios.

### Dos de las tres tareas ya no existen

Al revisarlas contra el sistema actual, la situación cambia:

**`healthcheck_dag` quedó obsoleto.** Hacía `curl` cada dos minutos y escribía
el resultado en un registro que nadie leía. Tras
[ADR-008](008-observabilidad-y-slo.md), Prometheus recoge el estado cada diez
segundos y **once reglas de alerta** avisan sin intervención. Mantener un DAG
que comprueba peor y avisa a nadie sería redundancia sin beneficio.

**`etl_analytics_dag` no hacía nada útil.** Ejecutaba una consulta de
agregación con `PostgresOperator` y **descartaba el resultado**: no lo escribía
en ninguna tabla, no lo publicaba, no lo exportaba. Consumía una conexión a la
base cada quince minutos para producir un resultado que se perdía.

**`pg_to_es_etl_dag` era la única con propósito real**, y no funcionaba: pasaba
objetos `uuid.UUID` como `_id` al cliente de Elasticsearch, que no puede
serializarlos a JSON. Además usaba un identificador de conexión distinto al del
otro DAG (`supabase_postgres` frente a `postgres_default`).

De modo que la pregunta no es "¿conviene Airflow para tres tareas?", sino
**"¿conviene Airflow para una tarea por hora?"**.

### Qué trabajo periódico necesita realmente el sistema en Go

| Tarea | Frecuencia | Origen |
|---|---|---|
| Purga y degradación del histórico | Diaria | [ADR-007](007-privacidad-y-datos-personales.md), pendiente |
| Agregados diarios por zona y tipo | Diaria | Informes municipales |
| Indexación en Elasticsearch | — | Ver sección 3.2 |
| Limpieza de claves en Redis | **Ninguna** | Los TTL lo resuelven solos |
| Chequeos de salud | **Ninguna** | [ADR-008](008-observabilidad-y-slo.md) |

La limpieza de Redis merece una nota: la guía del curso la sugiere como tarea
periódica, pero el diseño de las ventanas
([ADR-006](006-resiliencia-y-tolerancia-a-fallos.md)) poda por marca de tiempo
en la misma operación de escritura y apoya con TTL. No queda nada que limpiar
después.

**El trabajo periódico real son dos tareas nocturnas.**

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
  usuarios, y fallos de arranque cuando el esquema queda desfasado. El README
  del proyecto Java dedicaba una sección entera a recuperarse de ese error
  concreto.
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
problemas existe aquí. Dos tareas nocturnas independientes entre sí no forman un
grafo. No hay histórico que rellenar. No hay un equipo de datos.

Adoptar una herramienta por lo que podría necesitarse algún día tiene un costo
que se paga todos los días, y lo paga un equipo municipal reducido que no tiene
capacidad para operar infraestructura accesoria.

### 3.2 La indexación en Elasticsearch deja de ser una tarea periódica

Esta es la consecuencia de fondo, y es la que más debilita el caso de Airflow.

El sistema Java copiaba de PostgreSQL a Elasticsearch **cada hora**, lo que
significa que una búsqueda podía no encontrar un incidente ocurrido cincuenta
minutos antes. Para una plataforma de seguridad ciudadana, eso es un retraso
difícil de defender.

Pero el sistema ya tiene los eventos circulando por Kafka. Indexarlos no
requiere leer la base periódicamente: requiere **un consumidor más**, con la
misma forma que el persister.

```
Kafka ──▶ Persister ──▶ PostgreSQL   (archivo autoritativo)
      └──▶ Indexer   ──▶ Elasticsearch (búsqueda, casi en tiempo real)
```

Ventajas sobre el ETL por lotes:

- La búsqueda pasa de una hora de retraso a segundos.
- Desaparece la tarea periódica más compleja de las tres.
- Elasticsearch deja de depender de PostgreSQL: si la base falla, la
  indexación continúa.
- Reutiliza el patrón ya construido y probado.

Con eso, **el planificador queda con dos tareas nocturnas**, y sostener Airflow
para eso resulta indefendible.

### 3.3 Lo que hará el servicio de tareas

| Tarea | Horario | Descripción |
|---|---|---|
| **Retención** | Diaria, 03:00 | Aplica la política de [ADR-007](007-privacidad-y-datos-personales.md): elimina el seudónimo de placa pasados 90 días, las coordenadas al año, y conserva solo agregados después |
| **Agregados diarios** | Diaria, 03:30 | Calcula recuentos por zona y tipo del día anterior y los guarda en una tabla de resumen |

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
- Se necesita **backfill**: reprocesar un rango de fechas pasado.
- **Personal no desarrollador** necesita consultar el historial de ejecuciones
  o modificar horarios sin tocar código.
- Aparecen flujos de datos con ramificaciones o reintentos condicionales.

El primero y el tercero son los más probables si la Municipalidad amplía el
sistema a otras áreas. Llegado ese punto, la alternativa a evaluar no sería
necesariamente Airflow: existen orquestadores más ligeros con interfaz web.

### 3.5 Estado de implementación

**El `indexer` está construido y verificado.** El servicio `jobs` con las dos
tareas nocturnas sigue pendiente.

| Componente | Estado |
|---|---|
| Servicio `indexer` (Kafka → Elasticsearch) | **Listo y verificado** |
| Servicio `jobs` con las dos tareas nocturnas | Pendiente (~4 h) |
| Tabla de agregados y política de retención en SQL | Pendiente (~2 h) |

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

| Sistema | Retraso evento → disponible para búsqueda |
|---|---|
| Java, ETL por lotes `@hourly` | hasta 3 600 s |
| Go, consumidor de Kafka | **1,01 s** de media |

El valor se corresponde con lo esperado: `BATCH_TIMEOUT` es de 2 segundos, de
modo que la espera media de un evento hasta que su lote se cierra es de
aproximadamente un segundo.

**El mapeo funciona**, que era el fallo del ETL original:

```
geo        -> geo_point
timestamp  -> date
zone       -> keyword
score      -> float
```

Una consulta geoespacial real —eventos a menos de 5 km de la zona 10— devolvió
2 198 resultados. Con el mapeo que producía el ETL del sistema Java, esa
consulta no habría sido posible.

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
- **Sin *backfill*.** Si una tarea falla varios días seguidos, recuperar el
  trabajo perdido exige intervención manual.
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
sistema necesita **dos tareas, una vez al día**.

Instalarla exigiría tres programas adicionales funcionando de forma permanente
—incluido uno con su propia base de datos— que en conjunto consumirían más
recursos que todo el resto del sistema junto. Sobre equipos comprados por la
Municipalidad, que no se pueden ampliar sobre la marcha, esos recursos hacen
falta en otro sitio.

También requeriría mantenimiento propio: actualizaciones, gestión de usuarios y
una fuente recurrente de fallos de arranque que el sistema anterior ya había
experimentado.

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

**Está decidido, no construido.** Las dos tareas y el proceso de búsqueda están
pendientes de desarrollo, estimados en unas diez horas de trabajo.

Mientras tanto, el sistema **detecta y archiva incidentes con normalidad**: lo
que falta es la limpieza automática del archivo antiguo y la búsqueda histórica
avanzada.

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| Las tareas no se implementan a tiempo | Media | Medio: el archivo crece sin límite y no hay búsqueda histórica | Priorizar el proceso de búsqueda, que aporta más valor |
| Un fallo silencioso de la tarea nocturna | Baja | Medio | Cada ejecución emite métricas; una ausencia dispara alerta |
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
- `../../Ciudad_Inteligente/analytics/dags/` — los tres DAGs del sistema de
  referencia.
