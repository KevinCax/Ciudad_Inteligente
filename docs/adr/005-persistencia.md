# ADR-005: Persistencia — PostgreSQL relacional, con Elasticsearch para búsqueda

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (la base vive en la nube), [ADR-007](007-privacidad-y-datos-personales.md) (retención), [ADR-009](009-orquestacion-de-tareas-periodicas.md) (agregados e indexación)

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

Dos observaciones que condicionan la decisión:

**Las alertas no son una serie de tiempo.** Son decenas al día, se consultan de
forma individual, llevan una lista de evidencias y alimentan investigaciones.
Es un caso relacional clásico.

**El acceso a los eventos cambió con [ADR-009](009-orquestacion-de-tareas-periodicas.md).**
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
  nocturna del [ADR-009](009-orquestacion-de-tareas-periodicas.md).
- Es PostgreSQL: mismo lenguaje, mismos clientes, misma curva de aprendizaje.

**En contra**

- **Es una extensión, y no todos los proveedores gestionados la permiten.** Eso
  ata la decisión a un proveedor concreto o a autogestionar la base, que es
  justo la carga operativa que [ADR-001](001-plataforma-de-despliegue.md) quiso
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
temporales. Pero tras [ADR-009](009-orquestacion-de-tareas-periodicas.md) esas
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

Ambos consumen del broker de forma independiente
([ADR-004](004-broker-de-eventos.md)). Esa separación tiene una consecuencia
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
([ADR-001](001-plataforma-de-despliegue.md)): el costo de la ida y vuelta contra
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
reconstruye solo a partir del sistema de transporte. En el diseño anterior, el
buscador se alimentaba copiando de la base de datos cada hora, lo que ataba el
funcionamiento de uno al del otro.

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
- [ADR-001](001-plataforma-de-despliegue.md) — la base gestionada vive en la
  nube y no está en el camino crítico.
- [ADR-004](004-broker-de-eventos.md) — ambos destinos consumen del broker de
  forma independiente.
- [ADR-007](007-privacidad-y-datos-personales.md) — la retención opera sobre el
  contenido del payload.
- [ADR-009](009-orquestacion-de-tareas-periodicas.md) — indexación en tiempo
  casi real y agregados diarios.
- `deploy/sql/001_schema.sql` — esquema, índices y restricciones.
