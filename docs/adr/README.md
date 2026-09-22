# Registro de decisiones de arquitectura

Cada decisión relevante del sistema queda documentada aquí con el formato que
exige el enunciado (sección 4.1): contexto, opciones consideradas con sus
trade-offs, decisión, consecuencias e impacto en el negocio.

El criterio de qué merece un ADR: toda decisión que sea **costosa de revertir**
o que alguien pueda cuestionar razonablemente en la defensa.

## Cliente y premisa

Todas las decisiones se sostienen sobre una misma premisa, declarada en
[ADR-001](001-plataforma-de-despliegue.md):

> **Municipalidad de Guatemala**, la capital. Presupuesto de una institución
> pública con capacidad real de inversión, pero con equipo de sistemas
> reducido. Trata datos personales de ciudadanos. No puede depender de un
> enlace a internet para detectar incidentes de seguridad.

Un cliente distinto —una municipalidad pequeña del interior, por ejemplo—
llevaría a decisiones distintas, y eso es exactamente lo que el enunciado pide
defender.

## Índice

| ADR | Decisión | Estado |
|---|---|---|
| [001](001-plataforma-de-despliegue.md) | Plataforma de despliegue: híbrido LAN + nube | Aceptado |
| [002](002-lenguaje-y-runtime.md) | Lenguaje y runtime: Go | Aceptado |
| [003](003-organizacion-del-repositorio.md) | Organización del repositorio: monorepo con módulos independientes | Aceptado |
| [004](004-broker-de-eventos.md) | Broker de eventos: Kafka en modo KRaft | Aceptado |
| [005](005-persistencia.md) | Persistencia: PostgreSQL relacional + Elasticsearch | Aceptado |
| [006](006-resiliencia-y-tolerancia-a-fallos.md) | Resiliencia: DLQ, confirmación manual de offsets y pérdida aceptable | Aceptado |
| [007](007-privacidad-y-datos-personales.md) | Privacidad: seudonimización de placas en la frontera LAN–nube | Aceptado |
| [008](008-observabilidad-y-slo.md) | Observabilidad y objetivos de nivel de servicio | Aceptado |
| [009](009-orquestacion-de-tareas-periodicas.md) | Orquestación de tareas periódicas: retirar Airflow | Aceptado |
| [010](010-contenerizacion-y-orquestacion.md) | Contenerización y orquestación: Docker Compose, sin Kubernetes | Aceptado |

El enunciado exige un mínimo de seis, incluido obligatoriamente el de
plataforma de despliegue. **Hay diez aceptados.**

## Cobertura de las decisiones obligatorias

La sección 4.2 del enunciado enumera ocho decisiones que deben quedar
registradas. Las diez de aquí las cubren todas:

| Decisión exigida (§4.2) | ADR |
|---|---|
| Plataforma de despliegue | [001](001-plataforma-de-despliegue.md) |
| Broker de eventos | [004](004-broker-de-eventos.md) |
| Persistencia de eventos | [005](005-persistencia.md) |
| Orquestación de tareas periódicas | [009](009-orquestacion-de-tareas-periodicas.md) |
| Contenerización y orquestación | [010](010-contenerizacion-y-orquestacion.md) |
| Resiliencia y tolerancia a fallos | [006](006-resiliencia-y-tolerancia-a-fallos.md) |
| Seguridad y privacidad de datos | [007](007-privacidad-y-datos-personales.md) |
| Observabilidad y SLO | [008](008-observabilidad-y-slo.md) |

Los otros dos —[002](002-lenguaje-y-runtime.md) y
[003](003-organizacion-del-repositorio.md)— no los exige el enunciado, pero
documentan decisiones que condicionan a todas las demás.

Todos documentan decisiones aplicadas en el código y verificadas sobre el
sistema en ejecución.
