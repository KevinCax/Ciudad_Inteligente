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
| [002](002-lenguaje-y-runtime.md) | Lenguaje y runtime: Go sobre Java/Spring Boot | Aceptado |
| 003 | Organización del repositorio: monorepo con módulos independientes | Pendiente |
| 004 | Broker de eventos: Kafka en modo KRaft | Pendiente |
| 005 | Persistencia: relacional frente a series de tiempo | Pendiente |
| [006](006-resiliencia-y-tolerancia-a-fallos.md) | Resiliencia: DLQ, confirmación manual de offsets y pérdida aceptable | Aceptado |
| [007](007-privacidad-y-datos-personales.md) | Privacidad: seudonimización de placas en la frontera LAN–nube | Aceptado |
| [008](008-observabilidad-y-slo.md) | Observabilidad y objetivos de nivel de servicio | Aceptado |
| [009](009-orquestacion-de-tareas-periodicas.md) | Orquestación de tareas periódicas: retirar Airflow | Aceptado |

El enunciado exige un mínimo de seis, incluido obligatoriamente el de
plataforma de despliegue. **Hay seis aceptados.**

Los tres pendientes (003, 004, 005) documentan decisiones ya tomadas y
aplicadas en el código; se redactan para completar el registro, no porque
falten por decidir.
