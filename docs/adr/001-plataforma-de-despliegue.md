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
│                        Persister ◄── correlated.alerts      │
│                             │                               │
│        ▼ tokenización de la placa antes de salir ▼          │
└─────────────────────────────┬───────────────────────────────┘
                              │  TLS, egress controlado
┌─────────────────────────────▼──────────────── NUBE ─────────┐
│  PostgreSQL → Airflow ETL → Elasticsearch → Kibana          │
│            └───────────────────────────────→ Grafana        │
└─────────────────────────────────────────────────────────────┘
```

**En la LAN:** el ingestor, Kafka, Redis, el correlator y el persister. Todo el
recorrido evento → alerta.

**En la nube:** PostgreSQL gestionada, Elasticsearch, Kibana, Grafana y las
tareas periódicas de análisis.

**El persister es la pieza que hace posible el corte.** Es el único componente
que escribe en la nube: consume de Kafka y archiva por lotes. Ni el ingestor ni
el correlator escriben fuera de la LAN, de modo que la detección no depende del
enlace a internet.

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
- Lo único que se detiene es el **archivado**. El persister acumula retraso y
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
- **Custodia de la llave de tokenización.** Aparece un secreto crítico que hay
  que rotar, respaldar y proteger. Si se pierde, el histórico queda
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
