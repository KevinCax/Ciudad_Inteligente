# Ciudad Inteligente — Backend en Go

Sistema de monitoreo perimetral urbano para la **Municipalidad de Guatemala**. Reescritura en Go del backend de microservicios, con modelo de despliegue **híbrido**.

Proyecto final de Arquitectura de Computadores II (UMG). Entrega: 31 de octubre de 2026.

> El proyecto Java original vive en `../Ciudad_Inteligente` y es **solo de referencia**: no se modifica.

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
│                        Persister ◄── correlated.alerts      │
│                             │                               │
│        ▼ tokenización de la placa antes de salir ▼          │
└─────────────────────────────┬───────────────────────────────┘
                              │ TLS / egress controlado
┌─────────────────────────────▼──────────────── NUBE ─────────┐
│  PostgreSQL → Airflow ETL → Elasticsearch → Kibana          │
│            └───────────────────────────────→ Grafana        │
└─────────────────────────────────────────────────────────────┘
```

**Ningún tramo del camino crítico cruza el enlace a internet.** El ingestor solo habla con Redis y Kafka; el correlator, con Redis y Kafka. El único servicio que escribe en la nube es el **persister**, que archiva por lotes fuera del camino crítico.

Si se cae el enlace, la ciudad sigue detectando incidentes y generando alertas: lo único que se retrasa es el archivo histórico, que se pone al día solo cuando el servicio vuelve, leyendo desde el último offset confirmado.

Situar el persister exactamente sobre la frontera LAN→nube tiene una consecuencia afortunada: es también el lugar natural para tokenizar la placa vehicular antes de que el dato salga del control municipal.

## Microservicios

| Servicio | Módulo | Puerto | Rol | Depende del enlace |
|---|---|---|---|---|
| [ingestor](services/ingestor) | `ciudadinteligente/ingestor` | 8000 | Entrada REST: valida, enriquece, deduplica y publica | No |
| [correlator](services/correlator) | `ciudadinteligente/correlator` | 8080 | Correlación en ventanas sobre Redis y emisión de alertas | No |
| [persister](services/persister) | `ciudadinteligente/persister` | 8002 | Archiva eventos y alertas en PostgreSQL, por lotes | Sí |
| [indexer](services/indexer) | `ciudadinteligente/indexer` | 8003 | Indexa eventos y alertas en Elasticsearch, por lotes | Sí |

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

El escenario `alertas` envía las secuencias que disparan cada uno de los cuatro tipos de alerta, para verificar la correlación end-to-end.

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

## Decisiones de arquitectura

Las decisiones relevantes se documentan como ADR en [`docs/adr/`](docs/adr/), con el formato que exige el enunciado: contexto, opciones consideradas con sus trade-offs, decisión, consecuencias e impacto en el negocio.

| ADR | Decisión | Estado |
|---|---|---|
| [001](docs/adr/001-plataforma-de-despliegue.md) | Plataforma de despliegue: híbrido LAN + nube | Aceptado |
| [002](docs/adr/002-lenguaje-y-runtime.md) | Lenguaje y runtime: Go sobre Java/Spring Boot | Aceptado |
| [006](docs/adr/006-resiliencia-y-tolerancia-a-fallos.md) | Resiliencia: DLQ, offsets manuales y pérdida aceptable | Aceptado |
| [007](docs/adr/007-privacidad-y-datos-personales.md) | Privacidad: seudonimización en la frontera LAN–nube | Aceptado |
| [008](docs/adr/008-observabilidad-y-slo.md) | Observabilidad y objetivos de nivel de servicio | Aceptado |
| [009](docs/adr/009-orquestacion-de-tareas-periodicas.md) | Orquestación de tareas periódicas: retirar Airflow | Aceptado |
| 003–005 | Repositorio, broker, persistencia | Pendientes |

## Diferencias deliberadas con el sistema Java

La migración corrige defectos del sistema de referencia en lugar de traducirlos. Los principales:

| Sistema Java | Backend en Go |
|---|---|
| La regla `accident` era inalcanzable: filtraba LPR con velocidad > 80 y luego exigía < 40 sobre esa misma lista | La ventana conserva todos los LPR y la caída de velocidad se evalúa sobre el conjunto completo |
| `score` se persistía como `Integer`: 0.85, 0.90 y 0.95 se guardaban como `1` | `score` es `float64` sobre columna `numeric`, y el contrato lo acota entre 0 y 1 |
| Respondía `202` antes de confirmar la publicación en Kafka; sin DLQ | Publicación con `acks=all` confirmada antes de responder, y `events.dlq` para lo que no cumple contrato |
| Idempotencia con `hasKey` + `set`, no atómica | `SET NX EX` en una sola operación |
| Listas de Redis que nunca expiraban y se leían completas por evento | Sorted sets con poda por marca de tiempo |
| Cada evento re-disparaba la regla sobre la misma evidencia | Supresión de alertas por tipo, zona y ventana |
| `partition_key = event_type`: 5 claves para 3 particiones | `partition_key = geo.zone` por defecto, con afinidad real por zona |
| Validaba el objeto ya deserializado, así que `additionalProperties:false` nunca detectaba nada | Valida el JSON crudo antes de deserializar |
| `/metrics` devolvía `{"alerts":0,"events":0}` fijo | Métricas Prometheus reales, incluida la latencia que sostiene el SLO |
| `sensor.speed` en el contrato pero sin regla que lo consumiera | Incorporado a la regla de posible robo, como describe el enunciado |

La estrategia de particionado es configurable (`PARTITION_KEY_STRATEGY`) para poder correr ambos sistemas en paralelo durante la migración y comparar salidas evento por evento.

## Estado

| Componente | Estado |
|---|---|
| Contratos (evento canónico y alerta) | Listo |
| **Ingestor completo** — pipeline, persistencia, publicación, DLQ, auth, tokenización, métricas | **Listo** |
| **Correlator completo** — consumidor, ventanas, motor de reglas, supresión, falsos positivos, API | **Listo** |
| **Persister completo** — archivado por lotes en PostgreSQL, tokenización, DLQ | **Listo** |
| **Indexer completo** — indexación por lotes en Elasticsearch, plantillas, índices mensuales | **Listo** |
| **Despliegue** — compose edge, dev y nube | **Escrito, sin ejecutar** |
| **Pruebas de carga** | **Listo** |
| ADR-001 (plataforma de despliegue) | Listo |
| ADR-002 (lenguaje y runtime) | Listo |
| ADR-006 (resiliencia y tolerancia a fallos) | Listo |
| ADR-007 (privacidad y datos personales) | Listo |
| ADR-008 (observabilidad y SLO) | Listo |
| ADR-009 (orquestación de tareas periódicas) | Listo |
| **Mínimo de seis ADR exigido** | **Cumplido** |
| ADR-003 a 005 (registro completo) | Pendientes |
| Servicio `jobs` — retención y agregados nocturnos (ADR-009) | Pendiente |
| Informe final APA 7 | Pendiente |
| Informe final APA 7 | Pendiente |

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

El indexer sustituye al ETL horario del sistema Java. Crea **un índice por mes** (`events-2026.09`), de modo que aplicar la política de retención sea borrar un índice completo —operación inmediata— en lugar de un `DELETE BY QUERY` sobre millones de documentos. Las plantillas de índice se aplican solas al arrancar, y son las que hacen que `geo` sea consultable como `geo_point` y el mapa de calor funcione.

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

Hay cuatro SLO definidos y **once reglas de alerta** en [`deploy/prometheus/alertas.yml`](deploy/prometheus/alertas.yml), cada una con el primer paso de diagnóstico escrito en su anotación. El detalle, junto con la política de presupuesto de error, está en [ADR-008](docs/adr/008-observabilidad-y-slo.md).
