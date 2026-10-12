# ADR-012: Recorridos por calle — OSRM en la capa de borde

**Estado:** aceptado
**Fecha:** 2026-10-07
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (por qué el motor vive en la LAN), [ADR-007](007-privacidad-y-datos-personales.md) (las coordenadas son dato personal), [ADR-009](009-orquestacion-de-tareas-periodicas.md) (quién calcula y cuándo)

---

## 1. Contexto

El mapa de incidentes unía los puntos donde una cámara vio un vehículo con
líneas rectas, que cortan por encima de las manzanas. La petición es que el
recorrido siga las calles, como una ruta de navegador.

Antes de resolverlo técnicamente hay que decir qué se está dibujando, porque
determina el resto de la decisión:

> **El sistema no sabe por dónde circuló el vehículo.** Sabe que una cámara lo
> captó en tres puntos. Cualquier línea entre ellos —recta o por calles— es una
> interpolación.

La diferencia es que la recta *se nota* que es una simplificación, y la ruta por
calles no: parece un recorrido medido. Esa es la contrapartida real de esta
decisión y conviene que figure por escrito.

## 2. Opciones consideradas

### Opción A — Dejar la línea recta

Cero coste. Y una virtud que se pierde al cambiar: nadie confunde una recta
que atraviesa seis manzanas con el camino que tomó un coche.

Se descartó porque no responde a la pregunta operativa. Un agente que mira el
mapa necesita saber por qué calles pudo escapar el vehículo para decidir dónde
interceptarlo, y una recta no se lo dice.

### Opción B — API externa de rutas

Un servicio como OpenRouteService o el OSRM público. Se monta en una tarde.

Se descartó por dos razones, y la segunda pesa más que la primera.

La primera es de disponibilidad: el [ADR-001](001-plataforma-de-despliegue.md)
sostiene que la detección no depende del enlace a internet, y buena parte de la
defensa del diseño híbrido descansa en eso. Un mapa que deja de funcionar
cuando se cae la conexión contradice el argumento.

La segunda es de privacidad. Pedir una ruta significa **enviar a un tercero las
coordenadas exactas y la hora de un incidente de seguridad ciudadana**. El
[ADR-007](007-privacidad-y-datos-personales.md) se toma el trabajo de que la
placa no cruce a la nube sin seudonimizar; mandar las ubicaciones a un servicio
externo por comodidad habría vaciado ese esfuerzo por otro lado.

### Opción C — OSRM en la capa de borde

Un contenedor con el mapa de Guatemala de OpenStreetMap, preprocesado una vez.
Calcula rutas reales sin salir de la red municipal.

Cuesta un contenedor más, 131 MB de descarga inicial y unos minutos de
preprocesado.

## 3. Decisión

**Opción C.** El motor de rutas vive en la LAN, junto al correlator.

### 3.1 Los datos se preparan aparte del arranque

`./make.sh rutas-preparar` descarga el mapa y construye los índices. **No** es
parte de `up`.

Meterlo en el arranque habría significado que levantar el sistema para una
demostración depende de poder descargar 131 MB, y que un volumen recreado
obliga a esperar varios minutos antes de ver nada. Los datos viven en
`deploy/osrm-data`, que está en `.gitignore`: un mapa de OpenStreetMap no se
versiona, se descarga.

Se usa el algoritmo MLD y no CH porque permite recalcular los pesos sin rehacer
el índice entero, que es lo que haría falta el día que se quisieran incorporar
cortes de calle o tráfico.

### 3.2 Calcula `jobs`, no el correlator

Pedir una ruta es una llamada HTTP a un motor externo. Ponerla en el camino
crítico de la detección significaría que **un fallo del motor de rutas retrasa
la emisión de una alerta de incendio**. El recorrido es presentación; la alerta
es la función del sistema.

Por eso lo calcula una tarea periódica. Eso obligó a extender el planificador
del [ADR-009](009-orquestacion-de-tareas-periodicas.md), que solo sabía de
tareas diarias a hora fija, con tareas **por intervalo**. La distinción no es
caprichosa: una tarea diaria cierra el día, una tarea por intervalo acompaña al
flujo. Calcular a las tres de la madrugada el recorrido de un incidente de media
tarde no le sirve a nadie, porque el operador ya miró el mapa y lo vio vacío.

El cerrojo también cambia de forma. El de las tareas diarias lleva la fecha y
dura casi un día, para que la purga corra una sola vez. El de las tareas por
intervalo lleva el **número de ventana** y dura lo que el intervalo, de modo
que el propio cerrojo marca el ritmo: el planificador consulta cada minuto y
solo entra quien toma la ventana. Sin eso habría que guardar en memoria la
última ejecución, que se pierde en cada reinicio y no sirve con varias
instancias.

### 3.3 Se guarda el resultado, incluidos los fracasos

La tabla `alert_routes` registra tres estados: `calculada`, `sin_ruta` y
`fallida`.

Guardar solo los éxitos habría hecho que la tarea reintentara lo mismo cada
minuto para siempre, y nadie podría distinguir una alerta cuyo recorrido aún no
se ha calculado de otra para la que no existe. `sin_ruta` no se reintenta
—ese resultado no va a cambiar—; `fallida` sí, hasta tres veces, porque el
fallo típico es que el motor estaba arrancando.

La geometría se guarda como un array JSON de pares `[lon, lat]` en **una sola
fila por alerta**, no una fila por vértice. Un recorrido urbano tiene cientos de
vértices y desgranarlos multiplicaría el tamaño de la tabla sin que nadie
consulte jamás un vértice suelto: siempre se lee la ruta entera.

### 3.4 Qué se dibuja y qué no

El panel superpone dos capas:

| Capa | Qué es | Origen |
|---|---|---|
| Marcas | Los puntos donde una cámara vio el vehículo | `events.geo_lat/geo_lon` — **medido** |
| Línea | El camino más probable entre esos puntos | `alert_routes.geometria` — **estimado** |

La distinción está en el esquema, en la descripción del panel y en este
documento. No está en el color de la línea: se decidió dibujarla como una ruta
normal, sólida, porque es lo que hace legible el recorrido. Es una decisión
consciente y tiene un riesgo asociado, que se enumera abajo.

## 4. Consecuencias

### Positivas

- El recorrido de un incidente se lee como una ruta de navegador: se ve por
  dónde pudo escapar el vehículo y hacia dónde iba.
- Funciona sin internet, igual que la detección.
- Las coordenadas de los incidentes no salen de la red municipal.
- El planificador gana tareas por intervalo, que es una capacidad que faltaba
  y que otras tareas podrán usar.

### Negativas aceptadas

- **La ruta es una hipótesis dibujada como un hecho.** El sistema no midió ese
  camino. Si alguien pregunta en la defensa «¿cómo saben que fue por ahí?», la
  respuesta honesta es: no lo sabemos, es el camino más probable entre tres
  puntos que sí medimos. Mitigarlo visualmente —línea punteada, leyenda de
  «estimado»— se consideró y se descartó a favor de la legibilidad.
- **Un contenedor y 131 MB más.** El preprocesado tarda varios minutos y hay
  que acordarse de ejecutarlo en una instalación nueva.
- **El mapa envejece.** OpenStreetMap cambia; el extracto descargado no. Una
  calle nueva no aparecerá hasta que alguien rehaga los índices.
- **No hay contraste entre la ruta y el tiempo real.** La tabla guarda la
  distancia y la duración que estima el motor, pero nada compara todavía esa
  duración con el tiempo transcurrido entre las dos lecturas. Sería la forma
  natural de detectar que el recorrido propuesto no es creíble.

## 5. Impacto en el negocio

### Qué se decidió

Que el recorrido de un incidente se dibuje siguiendo las calles, calculado
dentro de la red municipal.

### Por qué

Porque un mapa con líneas rectas sirve para contar incidentes y no para
perseguir uno. La diferencia entre «el vehículo fue visto en estos tres puntos»
y «el vehículo pudo tomar esta ruta» es la diferencia entre un informe y una
herramienta operativa.

### Costo

Ninguno recurrente. OpenStreetMap y OSRM son libres y el servicio corre sobre
el mismo equipo que el resto de la capa de borde. El único coste es el tiempo
de preparar los índices cuando se instala, y rehacerlos cuando se quiera un
mapa actualizado.

### Si el sistema crece

El extracto de Guatemala entero ocupa poco y el motor responde en
milisegundos. Si un día se quisieran incorporar cortes de calle o estado del
tráfico, el algoritmo elegido (MLD) permite recalcular los pesos sin rehacer el
índice.

---

## Referencias

- `deploy/preparar-rutas.sh` — descarga y preprocesado
- `deploy/sql/003_rutas.sql` — tabla `alert_routes`
- `services/jobs/internal/rutas/osrm.go` — cliente del motor
- `services/jobs/internal/scheduler/scheduler.go` — tareas por intervalo
