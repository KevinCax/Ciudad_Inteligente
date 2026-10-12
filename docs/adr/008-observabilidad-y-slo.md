# ADR-008: Observabilidad y objetivos de nivel de servicio

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (presupuesto de latencia), [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) (clasificación de fallos)

---

## 1. Contexto

El enunciado plantea este punto con una exigencia precisa (sección 4.2):
definir **umbrales concretos** —pone como ejemplo "alerta generada en menos de
2 segundos desde el evento"— y **qué ocurre cuando se incumplen, *más allá de
tener dashboards en Grafana***.

Esa última frase es la que decide el contenido de este ADR. Un tablero solo
avisa a quien lo está mirando. La pregunta no es qué se muestra, sino **qué
pasa a las tres de la madrugada cuando nadie mira**.

### El punto de partida: qué significa "tener métricas"

Exponer un endpoint `/metrics` es facilísimo y no significa nada por sí solo.
El antipatrón es un endpoint que devuelve un literal:

```
GET /metrics
    // pendiente: agregar la lógica real de métricas
    return {"alerts": 0, "events": 0}
```

Los registros eran `System.out.println` sin estructura, imposibles de filtrar o
agregar. Y aunque el evento canónico incluye `trace_id`, y el ingestor lo
generaba, **ese identificador nunca llegaba a los registros**: no había forma de
seguir un evento concreto desde su ingesta hasta la alerta que produjo.

El resultado práctico: era imposible responder *"¿cuánto tarda el sistema en
generar una alerta?"* con un número. Sin esa capacidad no hay SLO posible,
porque un objetivo que no se puede medir no es un objetivo, es una intención.

## 2. Opciones consideradas

### Opción A — Tableros de Grafana consultando la base de datos

Grafana conectado directamente a PostgreSQL, con consultas de agregación sobre
la tabla de alertas filtradas por fecha.

**A favor**

- Cero instrumentación: los datos ya están en la base.
- Suficiente para mostrar volumen y distribución por zona y tipo.

**En contra**

- **Mide el resultado, no el proceso.** Permite saber cuántas alertas hay, pero
  no cuánto tardaron en generarse, cuántos eventos se rechazaron ni por qué.
- **Depende de la nube.** Tras
  [ADR-001](001-plataforma-de-despliegue.md), la base está fuera de la LAN: si
  se cae el enlace, se pierde la visibilidad justo cuando más hace falta.
- Carga el sistema de archivo con consultas de monitoreo que se repiten cada
  pocos segundos.
- No permite alertar: Grafana avisa a quien lo mira.

### Opción B — Solo registros estructurados

Emitir todo en JSON y agregarlo en Elasticsearch, calculando lo que haga falta
con consultas.

**A favor**

- Un solo mecanismo que cubre diagnóstico y medición.
- Máximo detalle: cada línea conserva su contexto completo.

**En contra**

- Calcular un percentil de latencia sobre registros es caro y lento.
- El volumen crece con el tráfico; las métricas no.
- También depende de la nube.

### Opción C — Métricas Prometheus, registros estructurados y reglas de alerta

Instrumentar los servicios con métricas en el proceso, registros JSON con
`trace_id` propagado, y reglas que disparen sin intervención humana.

**A favor**

- El cálculo de percentiles es lo que Prometheus hace bien y barato.
- Las métricas viven **en la LAN**: la observabilidad sobrevive a una caída del
  enlace.
- Las reglas de alerta avisan sin que nadie mire.
- Los registros conservan el detalle para diagnosticar lo que las métricas
  señalan.

**En contra**

- Un componente más que desplegar y mantener.
- Instrumentar es trabajo explícito en cada servicio.
- Las métricas agregan: para entender un caso concreto hay que ir al registro.

### Opción D — Trazabilidad distribuida completa

OpenTelemetry con un recolector y un backend de trazas.

**A favor**

- Visión exacta del recorrido de cada evento, tramo por tramo.

**En contra**

- Complejidad operativa desproporcionada para cinco servicios y un equipo
  reducido.
- Buena parte del beneficio se obtiene propagando el `trace_id` por registros y
  cabeceras de Kafka, que es mucho más barato.

## 3. Decisión

**Se adopta la opción C.** La D queda registrada como evolución natural si el
sistema creciera en número de servicios.

### 3.1 Objetivos de nivel de servicio

Cuatro objetivos, cada uno con su indicador y su ventana de evaluación:

| # | Objetivo | Umbral | Indicador | Ventana |
|---|---|---|---|---|
| 1 | **Latencia de detección** | 99 % de las alertas en menos de **2 s** desde el evento disparador | `correlator_correlation_latency_seconds` | 30 días |
| 2 | **Integridad de ingesta** | 99,9 % de los eventos con `202` quedan archivados | eventos en PostgreSQL / eventos aceptados | 30 días |
| 3 | **Disponibilidad de ingesta** | 99,5 % de las peticiones sin error `5xx` | `ingestor_request_duration_seconds{status}` | 30 días |
| 4 | **Frescura del archivo** | 95 % de los eventos archivados en menos de **60 s** | `persister_retraso_archivo_seconds` | 7 días |

El objetivo 1 es el que define el propósito del sistema; el 4 es deliberadamente
más laxo porque, tras
[ADR-001](001-plataforma-de-despliegue.md), el archivado está **fuera del camino
crítico**: que se retrase no impide detectar incidentes.

### 3.2 El indicador de latencia mide lo correcto

`correlator_correlation_latency_seconds` registra el tiempo entre el
**`timestamp` del evento que dispara la correlación** y el momento de emitir la
alerta. No mide el tiempo de proceso interno, sino el recorrido completo:
transporte por Kafka, espera en la cola, evaluación de reglas y publicación.

La distinción importa. Si el correlator acumula retraso, la latencia interna
seguiría siendo de milisegundos mientras la real crece a minutos. Medir desde
el timestamp del evento hace visible ese retraso; medir desde la recepción lo
escondería.

Es también la razón de que la herramienta de carga advierta explícitamente que
**la latencia de ingesta no es el SLO**: `/events` responde en 14 ms, pero eso
solo dice que el evento se aceptó, no que se haya detectado nada.

### 3.3 Qué ocurre cuando se incumplen

Aquí está el núcleo de lo que pide el enunciado.

**Catorce reglas de alerta** en `deploy/prometheus/alertas.yml`, validadas con
`promtool`, agrupadas en cinco familias: latencia, integridad, disponibilidad,
salud interna y tareas periodicas.

Cada alerta lleva tres anotaciones: `resumen`, `descripcion` y —la que importa—
**`accion`**, con el primer paso concreto de diagnóstico. El criterio es que
quien reciba la alerta de madrugada no tenga que deducir por dónde empezar.

Ejemplo, la del SLO 1:

```yaml
- alert: SLOLatenciaDeteccionIncumplido
  expr: |
    ( sum(rate(correlator_correlation_latency_seconds_count[30m]))
      - sum(rate(correlator_correlation_latency_seconds_bucket{le="2"}[30m])) )
    / sum(rate(correlator_correlation_latency_seconds_count[30m])) > 0.01
  for: 10m
  annotations:
    accion: >-
      1) Revisar el lag del consumidor: si crece, el correlator no sigue el
      ritmo de la ingesta. 2) Revisar correlator_window_events: una ventana
      desproporcionada encarece cada evaluacion. 3) Si ambos son normales,
      sospechar de Redis.
```

#### Escalamiento por severidad

| Severidad | Significado | Respuesta esperada |
|---|---|---|
| `critica` | El sistema no cumple su propósito | Inmediata, a cualquier hora |
| `aviso` | Se consume presupuesto de error | En horario laboral |
| `info` | Conviene saberlo | Revisión periódica |

La regla `ServicioCaido` distingue en su propia acción: si el caído es el
persister, la detección continúa y solo se retrasa el archivo; si es el
ingestor o el correlator, **la ciudad ha dejado de detectar incidentes** y es
la máxima prioridad. Esa jerarquía es consecuencia directa de
[ADR-001](001-plataforma-de-despliegue.md).

#### Presupuesto de error

El SLO 1 al 99 % sobre 30 días deja un **1 % de presupuesto**. Su consumo
gobierna decisiones:

| Presupuesto consumido | Qué se hace |
|---|---|
| Menos del 50 % | Operación normal; se pueden desplegar cambios |
| 50 % – 100 % | Se congelan los cambios no esenciales; prioridad a estabilizar |
| Más del 100 % | Se escala a la coordinación del proyecto; se revisa el umbral o la capacidad |

El valor de un presupuesto de error es que convierte "¿desplegamos el viernes?"
en una pregunta con respuesta objetiva.

### 3.4 Dos alertas que no son de infraestructura

Dos reglas vigilan el **dominio**, no la máquina, y conviene señalarlas porque
son las que un profesor probablemente pregunte:

- **`MuchasAlertasSuprimidas`** — si se suprime más del 90 % de las alertas,
  `ALERT_SUPPRESS_TTL` probablemente sea demasiado largo y esté ocultando
  incidentes distintos en la misma zona. La supresión evita ruido; en exceso,
  esconde.
- **`TasaAltaDeFalsosPositivos`** — si más de la mitad de las coincidencias
  parciales expira sin completarse, los umbrales o las ventanas están mal
  calibrados. El enunciado trata el análisis de falsos positivos como mérito
  adicional (sección 10.3); esta regla lo convierte en una señal operativa
  continua en lugar de un análisis puntual.

### 3.5 Trazabilidad extremo a extremo

El `trace_id` entra por la cabecera `X-Trace-Id` —o se genera si falta—, viaja
en el contexto de la petición, se emite en **cada línea de registro** del
ingestor, se adjunta como cabecera del mensaje de Kafka y el correlator lo
recupera y lo emite en sus propios registros.

Resultado: un solo identificador permite reconstruir el recorrido completo de
un evento a través de los cinco servicios. Es el 80 % del valor de una
trazabilidad distribuida al 5 % de su costo operativo.

### 3.5.1 Dónde vive la observabilidad, y por qué no en la nube

Prometheus y Grafana corren **en la LAN municipal**, no en la capa de nube,
pese a que la analítica es justamente lo que el
[ADR-001](001-plataforma-de-despliegue.md) sitúa fuera. La excepción tiene dos
razones y conviene dejarlas escritas, porque la ubicación intuitiva es la
contraria.

**La razón de seguridad.** Recolectar métricas no es que los servicios envíen
datos: es que el recolector **abre conexiones hacia cada servicio**. Con
Prometheus en la nube, esas serían conexiones de nube hacia la red municipal,
es decir **tráfico de entrada**, que es precisamente lo que el modelo híbrido
excluye. Todo lo que cruza la frontera debe ser de salida.

**La razón de utilidad.** Si el enlace se cae, la detección sigue funcionando
—esa es la propiedad central del ADR-001— y es justo entonces cuando hacen
falta las métricas y las alertas. Un recolector al otro lado del enlace se
queda ciego en el único momento en que importa.

Grafana acompaña a Prometheus porque lo consulta, y separarlos reintroduciría
el mismo tráfico de entrada. Sus paneles de PostgreSQL y Elasticsearch sí
cruzan la frontera, pero como **consultas puntuales de salida**: durante un
corte esos paneles quedan en blanco y los de tiempo real siguen vivos, que es
exactamente la degradación que se quiere.

El esquemático de `docs/diagramas/infraestructura.svg` lo muestra como nodo E,
dentro del segmento LAN.

### 3.6 Tres propiedades de Prometheus que condicionan las reglas

Las tres se observaron durante la verificación y afectan a cómo hay que
escribir las reglas:

**Los contadores se reinician al reiniciar el proceso.** Se comprobó al
reiniciar el correlator: sus métricas volvieron a cero. Por eso todas las
reglas usan `rate()` o `increase()`, que manejan correctamente los reinicios,
y ninguna compara valores absolutos acumulados.

**Las métricas con etiquetas no existen hasta su primer uso.** Un
`CounterVec` no publica ninguna serie hasta que se observa una combinación de
etiquetas. Una consulta como `correlator_alerts_generated_total` no devuelve
cero antes de la primera alerta: **no devuelve nada**. Por eso la detección de
ausencia de tráfico se hace sobre `ingestor_events_ingested_total`, que siempre
tiene actividad en operación normal, y no sobre métricas que pueden no existir.

**Prometheus 3 normaliza las etiquetas `le` de los histogramas.** Es la que más
cara salió, porque no produce ningún error: produce silencio.

El correlator publica su bucket como `le="2"`, tal como lo declara el código.
Prometheus 3 lo almacena en su representación canónica de coma flotante, es
decir `le="2.0"`. Un selector literal `{le="2"}` no encuentra serie alguna, de
modo que la regla del SLO **no podía dispararse nunca**: el objetivo estaba
declarado, documentado y sin vigilancia real.

Se detectó porque el panel de cumplimiento del tablero llegaba vacío, no porque
alguna comprobación fallara. `promtool check rules` valida la sintaxis y da
`SUCCESS` igual, porque la expresión es correcta; lo que no existe es la serie.

La corrección es usar una expresión regular que acepte ambas formas:

```
correlator_correlation_latency_seconds_bucket{le=~"2(\\.0)?"}
```

Funciona igual en Prometheus 2 y 3. Como consecuencia adicional, la imagen de
Prometheus dejó de usar la etiqueta `latest` y pasó a versión fija: un cambio
de versión silencioso puede dejar sin efecto una regla de SLO, y para una
demostración evaluada en vivo la reproducibilidad vale más que estar al día.

La lección general es la que más conviene retener: **una regla de alerta que
no encuentra datos no se distingue de una regla que no tiene nada que
reportar.** Ambas callan. Por eso la verificación no puede limitarse a validar
la sintaxis; hay que comprobar que cada expresión devuelve serie contra un
Prometheus con datos reales.

## 4. Consecuencias

### Positivas

- Los SLO son verificables con un número, no con una impresión.
- La observabilidad vive en la LAN y sobrevive a una caída del enlace.
- Las alertas avisan sin que nadie mire un tablero.
- El presupuesto de error convierte decisiones de despliegue en objetivas.
- Un `trace_id` reconstruye el recorrido completo de cualquier evento.

### Negativas aceptadas

- **Un componente más que operar.** Prometheus hay que desplegarlo, dimensionar
  su retención y mantenerlo.
- **Las alertas hay que atenderlas.** Un sistema que avisa y a quien nadie
  responde es peor que uno que no avisa: enseña a ignorar avisos. Exige definir
  quién recibe cada severidad.
- **Los umbrales están sin calibrar con datos reales.** Los de este ADR se
  fijaron sobre mediciones de laboratorio; en producción habrá que ajustarlos
  tras observar el comportamiento real, y probablemente generen falsas alarmas
  al principio.
- **Falta el canal de notificación.** Las reglas disparan dentro de Prometheus,
  pero **no hay Alertmanager configurado** para enviarlas a un correo o un
  teléfono. Está identificado como trabajo pendiente.

  Conviene no confundirlo con lo que resuelve
  [ADR-011](011-aviso-a-terceros.md). Son dos canales distintos y ninguno
  sustituye al otro: aquel avisa a **los cuerpos de respuesta** de que hay un
  incendio en la zona 12; este avisaría al **equipo que opera el sistema** de
  que el propio sistema está fallando. El segundo sigue pendiente.
- **Instrumentar es trabajo recurrente.** Cada funcionalidad nueva debería
  añadir sus métricas, y eso se olvida con facilidad.

## 5. Evidencia medida

Medido el 21 de septiembre de 2026 sobre el sistema en ejecución.

### SLO 1 — Latencia de detección

Medición limpia, con las métricas reiniciadas y sobre alertas generadas en el
momento:

| Métrica | Valor |
|---|---|
| Alertas medidas | 10 |
| Latencia media | **44,1 ms** |
| Bajo 50 ms | 70 % |
| Bajo 100 ms | 100 % |
| **Bajo 2 s (objetivo)** | **100 %** |

Una medición anterior arrojó un 60 % bajo el umbral. Esa cifra estaba
contaminada por el *catch-up* posterior a reiniciar el correlator: al ponerse al
día procesa eventos con `timestamp` antiguo, y la latencia medida —que va desde
el timestamp del evento— refleja correctamente ese retraso. Es el indicador
funcionando como debe; no era representativo de la operación normal.

Merece quedar anotado porque es el tipo de matiz que distingue una medición de
una cifra.

### SLO 3 — Disponibilidad bajo carga

| Escenario | Peticiones | `5xx` | p50 | p99 |
|---|---|---|---|---|
| Ráfaga de 100 ev/s durante 10 s | 1 192 | **0** | — | 19,7 ms |
| Sostenido a 25 ev/s durante 12 s | 299 | **0** | 14,3 ms | 18,8 ms |

### SLO 2 — Integridad

2 403 eventos transportados por Kafka, 2 403 archivados en PostgreSQL. 40
alertas, 40 archivadas. Cero discrepancia.

### Reglas de alerta

```
Checking alertas.yml
  SUCCESS: 14 rules found
```

Las catorce evalúan sin error contra el Prometheus en ejecución: ninguna quedó
en estado `err` ni con expresión que no encuentre serie.

### Tableros

Tres tableros provisionados con **30 paneles**. Se comprobó enviando a sus
respectivos motores el texto exacto de cada consulta generada:

| Comprobación | Resultado |
|---|---|
| Expresiones PromQL de paneles y reglas | 45 comprobadas, 0 rechazadas |
| Consultas SQL de los paneles | 11 comprobadas, 0 con error |
| Orígenes de datos provisionados | Prometheus, PostgreSQL y Elasticsearch, los tres con conexión correcta |

Esa comprobación encontró dos defectos que ninguna validación de sintaxis
habría detectado: el selector de bucket descrito en el apartado 3.6, y unas
credenciales de PostgreSQL que el archivo de despliegue no pasaba al contenedor
de Grafana, de modo que el origen de datos se provisionaba con usuario y base
vacíos.

## 6. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Que el sistema **se vigile a sí mismo y avise solo** cuando deja de cumplir lo
prometido, en lugar de depender de que alguien esté mirando una pantalla.

### El compromiso que se asume

**Una alerta de seguridad se genera en menos de 2 segundos desde que ocurre el
evento**, en el 99 % de los casos. Hoy se cumple con amplio margen: el promedio
medido es de 44 milésimas de segundo.

Se asumen otros tres compromisos: que todo evento aceptado quede archivado, que
el sistema esté disponible, y que el archivo histórico no se atrase más de un
minuto.

### Qué pasa cuando no se cumple

Catorce reglas automáticas vigilan esos compromisos. Cada una lleva escrito el
primer paso a seguir, para que quien la reciba —incluso de madrugada— sepa por
dónde empezar sin tener que averiguarlo.

Las alertas se clasifican en tres niveles. Solo el más grave —que el sistema
haya dejado de detectar incidentes— exige atención inmediata a cualquier hora.
El resto se atiende en horario laboral.

### El concepto de "presupuesto de fallos"

Se acepta de antemano que el sistema falle un 1 % de las veces. Mientras se
consuma menos de la mitad de ese margen, se pueden hacer cambios con
normalidad. Si se supera la mitad, se congelan los cambios no urgentes hasta
estabilizarlo.

Esto evita la discusión recurrente de *"¿es buen momento para actualizar?"*:
la respuesta es un número, no una opinión.

### Lo que falta

**El canal de aviso no está conectado.** Las reglas funcionan y detectan, pero
todavía no envían la notificación a un correo o un teléfono. Es configuración
pendiente, de pocas horas de trabajo.

Además, **los umbrales están calibrados con pruebas de laboratorio**. Durante
las primeras semanas en operación real habrá que ajustarlos, y es previsible
que al principio se generen avisos que no correspondan a problemas reales.

### Qué exige

| Obligación | Frecuencia |
|---|---|
| Definir quién recibe cada nivel de alerta | Una vez |
| Conectar el canal de notificación | Pendiente |
| Revisar y ajustar umbrales | Mensual los primeros meses |
| Revisar el consumo del presupuesto de fallos | Mensual |

---

## Referencias

- Enunciado del proyecto final, sección 4.2 — observabilidad y SLOs; sección
  10.3 — análisis de falsos positivos.
- [ADR-001](001-plataforma-de-despliegue.md) — presupuesto de latencia y
  jerarquía de criticidad entre servicios.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — la clasificación de
  fallos alimenta las métricas por motivo.
- `deploy/prometheus/alertas.yml` — las catorce reglas.
- `services/*/internal/obs/obs.go` — instrumentación de cada servicio.
