# ADR-004: Broker de eventos — Kafka en modo KRaft

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el broker vive en la LAN), [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) (garantías de entrega)

---

## 1. Contexto

El broker es la columna vertebral del sistema. Todo evento pasa por él, y las
decisiones de [ADR-001](001-plataforma-de-despliegue.md) lo convierten en algo
más: al sacar la persistencia del camino crítico, **el broker es la fuente de
verdad del transporte**. Si un evento está en el broker, llegará a su destino;
si no está, se perdió.

Eso le impone tres requisitos:

1. **Retención suficiente** para que un consumidor caído se ponga al día al
   volver. El persister recupera su retraso leyendo desde el último offset
   confirmado, y eso solo funciona si los mensajes siguen ahí.
2. **Particionado por clave**, para que los eventos de una misma zona conserven
   su orden relativo. La correlación por ventana depende de ello.
3. **Varios consumidores independientes** sobre el mismo flujo: correlator,
   persister e indexer leen `events.standardized` sin interferirse.

El enunciado lo plantea como decisión a documentar (sección 4.2): Kafka frente a
RabbitMQ frente a un servicio gestionado, con el trade-off de rendimiento,
complejidad operativa y costo.

## 2. Opciones consideradas

### Opción A — RabbitMQ

**A favor**

- Considerablemente más simple de operar: un proceso, sin coordinación de
  particiones ni gestión de offsets.
- Menor consumo de recursos, lo que pesa sobre el hardware propio y acotado del
  [ADR-001](001-plataforma-de-despliegue.md).
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

- **Contradice [ADR-001](001-plataforma-de-despliegue.md).** El broker es el
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

Tras [ADR-001](001-plataforma-de-despliegue.md), el persister y el indexer
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
| Creación automática de topics | desactivada | Un nombre mal escrito debe fallar, no crear un topic fantasma |

El factor de replicación de 1 merece una nota: **es una limitación consciente
del laboratorio**. Con un solo broker, su caída detiene el sistema. En el
despliegue real sobre los dos servidores del
[ADR-001](001-plataforma-de-despliegue.md) debería subirse a 3 con un mínimo de
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

Se usa `segmentio/kafka-go`, en Go puro, para conservar el binario estático que
justifica la imagen mínima del [ADR-002](002-lenguaje-y-runtime.md).

La contrapartida es que **no ofrece productor idempotente completo**, que la
guía recomienda. Se compensa con idempotencia en el consumidor —`SET NX` sobre
el identificador de evento, `ON CONFLICT DO NOTHING` al archivar—, que protege
además contra reprocesos por rebalanceo, no solo contra reintentos de escritura.
El detalle está en [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md).

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
- [ADR-001](001-plataforma-de-despliegue.md) — el broker vive en la LAN.
- [ADR-002](002-lenguaje-y-runtime.md) — la elección de cliente y el binario
  estático.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — garantías de entrega e
  idempotencia en el consumidor.
- `deploy/docker-compose.edge.yml` — configuración de KRaft.
- `deploy/scripts/create-topics.sh` — particiones y retención por topic.
