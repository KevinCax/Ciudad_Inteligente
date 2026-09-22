# ADR-010: Contenerización y orquestación — Docker Compose, sin Kubernetes

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el corte híbrido define las capas a desplegar), [ADR-002](002-lenguaje-y-runtime.md) (el binario estático determina el tamaño de la imagen), [ADR-003](003-organizacion-del-repositorio.md) (cada servicio se despliega por separado)

---

## 1. Contexto

El sistema son cinco servicios propios más seis componentes de infraestructura
—Kafka, Redis, PostgreSQL, Elasticsearch, Prometheus y Grafana— repartidos en
dos capas por el corte híbrido del
[ADR-001](001-plataforma-de-despliegue.md). Hay que decidir cómo se empaquetan
y cómo se ponen en marcha.

El enunciado plantea la disyuntiva en la sección 4.2 y añade una frase que vale
la pena tomar en serio: *"defender tanto la adopción como la decisión
consciente de no usar Kubernetes es válido"*. Es decir, lo que se evalúa no es
cuál se elige sino si la elección responde al caso de negocio declarado.

Ese caso está fijado en el [ADR-001](001-plataforma-de-despliegue.md):
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
| Memoria residente por servicio | 15 – 19 MB |
| Arranque de un servicio | 7 – 50 ms |

Es un dato que condiciona todo lo demás: **el escalado horizontal automático,
que es el problema central que Kubernetes resuelve, no es un problema que este
sistema tenga.** Y si lo tuviera, el límite no sería el número de procesos sino
las tres particiones de Kafka del [ADR-004](004-broker-de-eventos.md), porque
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
  [ADR-001](001-plataforma-de-despliegue.md) protege.** Con el enlace caído los
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
| `docker-compose.edge.yml` | LAN municipal | Kafka (KRaft), Redis, los cinco microservicios, Prometheus, Grafana, Kafka-UI |
| `docker-compose.cloud.yml` | Nube | Elasticsearch, Kibana |
| `docker-compose.dev.yml` | Máquina del desarrollador | PostgreSQL local, en lugar de la base gestionada |

Que el corte del [ADR-001](001-plataforma-de-despliegue.md) coincida con la
frontera entre dos archivos no es cosmético: hace que la pregunta *"¿qué pasa
si se cae el enlace?"* se responda leyendo cuál de los dos sigue arriba.

Los servicios que cruzan la frontera —persister e indexer— **corren en la capa
`edge`** aunque escriban en la nube. Es deliberado: así la lectura de Kafka es
local y solo la escritura, ya por lotes, viaja por el enlace.

### 3.3 Qué aporta Compose que es suficiente

| Mecanismo | Uso en el proyecto | Qué cubre |
|---|---|---|
| `restart: unless-stopped` | 8 servicios en `edge`, 4 en `cloud` | Un proceso que muere vuelve solo; sobrevive al reinicio del servidor |
| `healthcheck` | 7 servicios en `edge` | Distingue *arrancado* de *listo*, que no es lo mismo en Kafka ni en Elasticsearch |
| `depends_on: condition: service_healthy` | 9 dependencias | Arranque ordenado: el ingestor no arranca antes que Kafka esté listo |

La tercera fila resuelve un problema concreto y muy visible en una demostración
en vivo: sin ella, los servicios arrancan a la vez, fallan al conectar y
dependen de que el reinicio los salve. Con ella, el sistema sube en orden a la
primera.

### 3.4 Las imágenes

`distroless/static` sobre compilación en dos etapas. Las imágenes finales pesan
entre **22 y 29 MB**, frente a los 257 MB que ocupa una imagen base de Java
antes siquiera de añadir la aplicación ([ADR-002](002-lenguaje-y-runtime.md)).

Importa más allá del tamaño: una imagen `distroless` **no tiene intérprete de
comandos**. No se puede abrir una sesión dentro del contenedor, lo que reduce
lo que un atacante puede hacer si logra ejecutar algo. El precio es que
diagnosticar exige métricas y registros en lugar de entrar a mirar, que es
justamente la disciplina que sostiene el
[ADR-008](008-observabilidad-y-slo.md).

### 3.5 La decisión es reversible, y ese es el punto

Es lo que permite descartar Kubernetes sin descartarlo para siempre.

Los cinco servicios ya cumplen lo que Kubernetes espera de una aplicación:

| Propiedad | Estado hoy | Qué sería en Kubernetes |
|---|---|---|
| Sin estado en el proceso | Sí; el estado vive en Redis, Kafka y PostgreSQL | `Deployment` sin volúmenes |
| Configuración por variables de entorno | Sí, sin archivos de configuración | `ConfigMap` y `Secret` |
| Endpoint de salud | `/health` en los cinco | Sondas de vitalidad y disponibilidad |
| Métricas expuestas | `/metrics` en los cinco | Escrapeo por anotación |
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
- [ADR-001](001-plataforma-de-despliegue.md) — el corte híbrido que los tres
  archivos de despliegue materializan.
- [ADR-002](002-lenguaje-y-runtime.md) — el binario estático que permite la
  imagen distroless.
- [ADR-003](003-organizacion-del-repositorio.md) — cada servicio se construye y
  despliega por separado.
- [ADR-008](008-observabilidad-y-slo.md) — diagnosticar sin entrar al
  contenedor.
- `deploy/docker-compose.edge.yml`, `deploy/docker-compose.cloud.yml`,
  `deploy/docker-compose.dev.yml` — las tres capas.
- `deploy/README.md` — procedimiento de puesta en marcha.
