# Ciudad Inteligente — Backend en Go

Sistema de monitoreo perimetral urbano para la **Municipalidad de Guatemala**. Reescritura en Go del backend de microservicios, con modelo de despliegue **híbrido**.

Proyecto final de Arquitectura de Computadores II (UMG). Entrega: 31 de octubre de 2026.

---

## Arquitectura

```
┌──────────────── ON-PREMISE (LAN municipal) ─────────────────┐
│                                                             │
│   Simuladores → Ingestor → Kafka → Correlator               │
│                    │        │  │        │                   │
│                  Redis      │  └── Redis (ventanas)         │
│                (dedupe)     │                               │
│                             ▼                               │
│              Persister · Indexer · Jobs ◄── alertas         │
│                             │                               │
│        ▼ tokenización de la placa antes de salir ▼          │
└─────────────────────────────┬───────────────────────────────┘
                              │ TLS / egress controlado
┌─────────────────────────────▼──────────────── NUBE ─────────┐
│  PostgreSQL                          Elasticsearch → Kibana │
│       └──────→ Grafana ←── Prometheus ←──────┘               │
└─────────────────────────────────────────────────────────────┘
```

**Ningún tramo del camino crítico cruza el enlace a internet.** El ingestor solo habla con Redis y Kafka; el correlator, con Redis y Kafka. Los servicios que escriben en la nube —**persister**, **indexer** y **jobs**— lo hacen por lotes y fuera del camino crítico.

Si se cae el enlace, la ciudad sigue detectando incidentes y generando alertas: lo único que se retrasa es el archivo histórico, que se pone al día solo cuando el servicio vuelve, leyendo desde el último offset confirmado.

Situar el persister exactamente sobre la frontera LAN→nube tiene una consecuencia afortunada: es también el lugar natural para tokenizar la placa vehicular antes de que el dato salga del control municipal.

El esquemático completo —nodos, servicios, puertos y segmentos de red— está en
[`docs/diagramas/infraestructura.svg`](docs/diagramas/infraestructura.svg). Se
genera desde código, de modo que el SVG y el PNG del informe no puedan
discrepar:

```bash
python docs/diagramas/infraestructura.py
```

**Prometheus y Grafana viven en la LAN, no en la nube.** Raspar métricas exige
que el recolector abra conexiones hacia cada servicio; con Prometheus en la
nube esas serían conexiones de **entrada** a la red municipal, que es lo que
este modelo excluye. Y si el enlace cae, es justo cuando hacen falta las
métricas. El razonamiento completo está en
[ADR-008 §3.5.1](docs/adr/008-observabilidad-y-slo.md).

## Microservicios

| Servicio | Módulo | Puerto | Rol | Depende del enlace |
|---|---|---|---|---|
| [ingestor](services/ingestor) | `ciudadinteligente/ingestor` | 8000 | Entrada REST: valida, enriquece, deduplica y publica | No |
| [correlator](services/correlator) | `ciudadinteligente/correlator` | 8080 | Correlación en ventanas sobre Redis y emisión de alertas | No |
| [persister](services/persister) | `ciudadinteligente/persister` | 8002 | Archiva eventos y alertas en PostgreSQL, por lotes | Sí |
| [indexer](services/indexer) | `ciudadinteligente/indexer` | 8003 | Indexa eventos y alertas en Elasticsearch, por lotes | Sí |
| [jobs](services/jobs) | `ciudadinteligente/jobs` | 8004 | Agregados diarios y retención del histórico | Sí |

Cada servicio es un **módulo Go independiente**: se compila, versiona y despliega por separado. No comparten código.

## Contratos

Los contratos viven en [`contracts/`](contracts/) y **no son una librería compartida**: cada servicio embebe su copia del JSON Schema con `go:embed` y define sus propios structs. Una librería común obligaría a redesplegar ambos servicios ante cualquier cambio de contrato, que es justamente lo que un microservicio debe evitar.

El riesgo de drift se controla con `check-contracts`, que debe correr en CI.

Ver [contracts/README.md](contracts/README.md) para el detalle.

## Despliegue

Dos capas separadas, que es como se materializa el modelo híbrido:

| Compose | Capa | Contiene |
|---|---|---|
| `deploy/docker-compose.edge.yml` | On-premise | Kafka (KRaft, sin Zookeeper), Redis, ingestor, correlator, Kafka-UI |
| `deploy/docker-compose.dev.yml` | Desarrollo | PostgreSQL local en lugar de la base gestionada |
| `deploy/docker-compose.cloud.yml` | Nube | Elasticsearch, Kibana, Grafana, Prometheus |

```bash
cp deploy/.env.example deploy/.env
```

```bash
./make.sh up
```

```bash
./make.sh up-cloud
```

Detalle completo y prueba end-to-end en [deploy/README.md](deploy/README.md).

## Pruebas de carga

```bash
./make.sh load rafaga
```

Genera un pico de 100 eventos/s durante 10 segundos con fases de calentamiento y recuperación, y reporta percentiles de latencia, códigos de respuesta y saturación. Cubre el requisito del enunciado de evaluar el sistema ante picos de carga (§2.3), que los simuladores Python originales no ejercían porque enviaban a ritmo constante.

### Los cinco escenarios

| Escenario | Qué hace |
|---|---|
| `sostenido` | Ritmo constante configurable con `-rps` y `-duracion` |
| `rafaga` | Calentamiento, pico de 100 ev/s durante 10 s y recuperación |
| `alertas` | Las secuencias que disparan cada uno de los cuatro tipos de alerta |
| `bulk` | Comprueba el contrato de `/events/bulk` |
| `enunciado` | Reproduce los **dos** renglones de la tabla §2.4 del enunciado |

```bash
./make.sh load alertas
```

Deja una alerta viva de cada tipo y muestra el `curl` para consultarla:

```
possible_robbery         -> zona_1
accident                 -> zona_10
fire                     -> zona_12
traffic_speed_violation  -> zona_18
```

El escenario `enunciado` es el que conviene enseñar en la defensa: reproduce literalmente la tabla del profesor, incluido **el renglón que casi nunca se prueba**.

```bash
./make.sh load enunciado
```

```
Renglon 1 - zona_4, 22:05: panico + placa XYZ123 a 85 km/h
           -> alerta emitida  [OK]

Renglon 2 - zona_10, 17:22: boton de panico sin eventos complementarios
           -> ninguna alerta emitida  [OK]
           -> falso positivo confirmado y contabilizado  [OK]
```

El segundo renglón comprueba algo más difícil que disparar: **que no dispare, y que aun así quede constancia**. Sin ese registro, una zona tranquila y una zona llena de señales incompletas son indistinguibles. Tarda unos dos minutos porque hay que esperar a que cierre la ventana de correlación; con `-espera 0` se omite la espera.

El escenario `bulk` no mide volumen, **comprueba la promesa del endpoint**: que un evento inválido no invalide el lote y que la respuesta diga en qué índice falló y por qué. Envía un lote limpio, uno con inválidos entremezclados, uno con un duplicado dentro del propio lote, y un cuerpo mal formado que debe dar `400` y no `202`. Si alguna comprobación falla, sale con código distinto de cero, de modo que sirve en integración continua.

## Puesta en marcha

Requisitos: Go 1.25+ y Docker.

```bash
cp services/ingestor/.env.example services/ingestor/.env
cp services/correlator/.env.example services/correlator/.env
# editar cada .env con las credenciales reales
```

En Linux o macOS:

```bash
make check
```

En Windows con Git Bash (sin `make`):

```bash
./make.sh check
```

Ambos ejecutan lo mismo: verificación de contratos, formato, `vet` y tests. Los targets disponibles son `sync-contracts`, `check-contracts`, `tidy`, `fmt`, `vet`, `test`, `build`, `docker` y `check`.

### API del ingestor (:8000)

| Método | Ruta | Respuestas |
|---|---|---|
| `POST` | `/events` | `202` aceptado · `400` contrato · `409` duplicado · `401` sin credenciales · `503` dependencia caída |
| `POST` | `/events/bulk` | `202` con resultado por índice; un evento inválido no invalida el lote |
| `GET` | `/events/health` | `200` si todo responde · `503` si Postgres o Kafka están caídos |
| `GET` | `/events/schema` | Contrato canónico v1.0 vigente |
| `GET` | `/metrics` | Métricas Prometheus |

Autenticación por cabecera `X-API-Key`. El `trace_id` entra y sale por `X-Trace-Id` y viaja en las cabeceras del mensaje de Kafka.

### API del correlator (:8080)

| Método | Ruta | Respuestas |
|---|---|---|
| `GET` | `/alerts/active?zone=` | Alertas vigentes de la zona desde Redis (requiere token) |
| `GET` | `/alerts/db?zone=&limit=` | Alertas persistidas, con tope obligatorio (requiere token) |
| `GET` | `/health` | `200` / `503` según Redis, Postgres y Kafka |
| `GET` | `/schema/event`, `/schema/alert` | Contratos vigentes |
| `GET` | `/metrics` | Métricas Prometheus, incluida la latencia del SLO |

Autenticación por `Authorization: Bearer`.

### Persister (:8002) e Indexer (:8003)

Sin API de negocio: solo `/health` y `/metrics`. Ambos consumen `events.standardized` y `correlated.alerts` con grupos de consumo independientes, y escriben por lotes — el persister en PostgreSQL, el indexer en Elasticsearch.

Escribir por lotes es la ventaja concreta de haber sacado la persistencia del camino crítico: el costo de la ida y vuelta contra un destino remoto se paga una vez cada cien eventos en lugar de una vez por evento.

El indexer sustituye a la copia periódica desde la base que este tipo de sistemas suele usar, y que introduce hasta una hora de retraso en la búsqueda. Crea **un índice por mes** (`events-2026.09`), de modo que aplicar la política de retención sea borrar un índice completo —operación inmediata— en lugar de un `DELETE BY QUERY` sobre millones de documentos. Las plantillas de índice se aplican solas al arrancar, y son las que hacen que `geo` sea consultable como `geo_point` y el mapa de calor funcione.

### Jobs (:8004)

Sustituye a Apache Airflow ([ADR-009](docs/adr/009-orquestacion-de-tareas-periodicas.md)). Dos tareas nocturnas:

| Tarea | Hora | Qué hace |
|---|---|---|
| `agregados_diarios` | 03:00 | Resume el día anterior por zona y tipo |
| `retencion` | 03:30 | Aplica la política de privacidad del [ADR-007](docs/adr/007-privacidad-y-datos-personales.md) |

Los agregados corren **antes** que la retención: si la purga borrara los eventos primero, el resumen saldría incompleto, y esa pérdida es irreversible porque el agregado es justamente lo que sobrevive al borrado.

Un cerrojo `SET NX` en Redis garantiza una sola ejecución al día aunque haya varias instancias o el proceso se reinicie. Un fallo libera el cerrojo, para poder reintentar el mismo día.

| Método | Ruta | Uso |
|---|---|---|
| `GET` | `/tareas` | Qué hay programado y a qué hora |
| `POST` | `/tareas/{nombre}/ejecutar` | Lanza la tarea ahora (requiere token) |
| `POST` | `/tareas/agregados_diarios/ejecutar?dia=2026-09-15` | Recalcula una fecha concreta |

El parámetro `dia` es el *backfill* mínimo: cubre el caso real —recalcular el día que falló— sin la maquinaria de rangos y dependencias de un orquestador.

## Privacidad: dónde se tokeniza la placa

La placa vehicular se convierte en un seudónimo HMAC-SHA256 **solo en el camino hacia la base de datos**, que está en la nube. El mensaje de Kafka conserva el valor en claro porque el broker vive en la LAN municipal.

```
Productor ──en claro──> Ingestor ──en claro──> Kafka (LAN) ──> Correlator
                                                   │
                                        ┌──────────┴──────────┐
                                        ▼                     ▼
                                   Persister              Indexer
                                        │ tokenizada          │ tokenizada
                                        ▼                     ▼
                                  PostgreSQL            Elasticsearch
                                    (nube)                  (nube)
```

Los dos servicios que cruzan la frontera usan **la misma llave HMAC**. Si difirieran, el mismo vehículo tendría seudónimos distintos en cada destino y las búsquedas cruzadas dejarían de funcionar.

Así, el punto de corte del modelo híbrido coincide con la frontera de privacidad: la nube nunca recibe placas identificables, y la llave HMAC se custodia on-premise. La tokenización normaliza el formato antes de calcular, de modo que `abc-123` y `ABC123` producen el mismo seudónimo y el vehículo sigue siendo correlacionable sin ser identificable.

## SLO

**99 % de las alertas se generan en menos de 2 segundos desde el momento del evento que las dispara.** Medido: media de 44 ms, 100 % bajo el umbral.

Lo mide `correlator_correlation_latency_seconds`, un histograma etiquetado por tipo de alerta que registra el tiempo entre el `timestamp` del evento disparador y la emisión de la alerta. El camino crítico es íntegramente on-premise, de modo que el SLO no depende del enlace a internet de la Municipalidad.

Hay cuatro SLO definidos y **catorce reglas de alerta** en [`deploy/prometheus/alertas.yml`](deploy/prometheus/alertas.yml), cada una con el primer paso de diagnóstico escrito en su anotación. El detalle, junto con la política de presupuesto de error, está en [ADR-008](docs/adr/008-observabilidad-y-slo.md).

## Visualización

Tres tableros de Grafana, **provisionados automáticamente** al arrancar la capa
de nube. No hay que importar nada a mano.

| Tablero | Origen | Contenido |
|---|---|---|
| Operación en tiempo real | Prometheus | Ingesta por tipo de sensor, alertas por tipo, rechazos, ventanas y estado de los cinco servicios |
| Incidentes y mapa de calor | PostgreSQL | Mapa de calor, lista de incidentes y alertas críticas |
| SLO y presupuesto de error | Prometheus | Cumplimiento a 30 días, presupuesto consumido, percentiles con el umbral marcado |

Los orígenes de datos llevan `uid` explícito: sin él Grafana genera uno al azar
en cada instalación y los tableros llegan vacíos a la demostración.

## Correlación e inferencia

El motor que convierte señales sueltas en incidentes está documentado aparte, en
[`docs/correlacion.md`](docs/correlacion.md): ventanas por zona, las cuatro
reglas con sus umbrales, la supresión, el registro de falsos positivos, los
parámetros ajustables y los límites conocidos del modelo.

Los umbrales se ajustan **sin recompilar** por variable de entorno, y un valor
sin sentido —un umbral de velocidad en cero, una caída invertida— **impide
arrancar el servicio** en lugar de producir un motor que no detecta nada o que
alerta por todo.

## Decisiones de arquitectura

Las decisiones relevantes se documentan como ADR en [`docs/adr/`](docs/adr/), con el formato que exige el enunciado: contexto, opciones consideradas con sus trade-offs, decisión, consecuencias e impacto en el negocio.

| ADR | Decisión | Estado |
|---|---|---|
| [001](docs/adr/001-plataforma-de-despliegue.md) | Plataforma de despliegue: híbrido LAN + nube | Aceptado |
| [002](docs/adr/002-lenguaje-y-runtime.md) | Lenguaje y runtime: Go | Aceptado |
| [006](docs/adr/006-resiliencia-y-tolerancia-a-fallos.md) | Resiliencia: DLQ, offsets manuales y pérdida aceptable | Aceptado |
| [007](docs/adr/007-privacidad-y-datos-personales.md) | Privacidad: seudonimización en la frontera LAN–nube | Aceptado |
| [008](docs/adr/008-observabilidad-y-slo.md) | Observabilidad y objetivos de nivel de servicio | Aceptado |
| [009](docs/adr/009-orquestacion-de-tareas-periodicas.md) | Orquestación de tareas periódicas: retirar Airflow | Aceptado |
| [003](docs/adr/003-organizacion-del-repositorio.md) | Organización del repositorio: monorepo con módulos independientes | Aceptado |
| [004](docs/adr/004-broker-de-eventos.md) | Broker de eventos: Kafka en modo KRaft | Aceptado |
| [005](docs/adr/005-persistencia.md) | Persistencia: PostgreSQL relacional + Elasticsearch | Aceptado |
| [010](docs/adr/010-contenerizacion-y-orquestacion.md) | Contenerización: Docker Compose, sin Kubernetes | Aceptado |

El enunciado exige un mínimo de seis; **hay diez aceptados**, y entre ellos están las ocho decisiones que la sección 4.2 enumera como obligatorias. Todos documentan decisiones aplicadas en el código y verificadas sobre el sistema en ejecución.

