# Sistema de correlación e inferencia de eventos

Documentación del motor que convierte señales sueltas en incidentes con
significado. Es el entregable que el enunciado pide en su sección 8,
*"Documentación del sistema de correlación e inferencia"*.

> Lo implementa el servicio [`correlator`](../services/correlator). Las
> decisiones de arquitectura que lo rodean están en
> [ADR-006](adr/006-resiliencia-y-tolerancia-a-fallos.md) (garantías de
> entrega) y [ADR-008](adr/008-observabilidad-y-slo.md) (qué se mide).

---

## 1. El problema

Un evento aislado casi nunca es un incidente.

Una placa que pasa a 85 km/h no es un delito. Un botón de pánico pulsado puede
ser un error del ciudadano. Un vidrio roto puede ser un accidente doméstico.
**Los tres juntos, en la misma zona y dentro del mismo minuto, son un robo en
curso.**

Correlacionar es responder, para cada evento que llega, una sola pregunta:
*¿junto con lo que acaba de pasar cerca, esto configura un patrón que merece
la atención de un operador?*

Eso exige tres cosas, y el resto de este documento las desarrolla:

1. **Memoria de lo reciente**, acotada, por zona.
2. **Reglas** que expresen los patrones y digan cuánta confianza merecen.
3. **Disciplina de emisión**, para no repetir la misma alerta mientras el
   incidente sigue en curso, y para dejar constancia de lo que casi fue.

## 2. La ventana de correlación

### Por qué la unidad es la zona

Dos eventos se correlacionan si son **próximos en espacio y en tiempo**. La
proximidad temporal la da la ventana; la espacial, la zona.

La zona no es una elección cosmética: es la misma clave con la que el ingestor
particiona el topic de Kafka ([ADR-004](adr/004-broker-de-eventos.md)). Eso
garantiza que los eventos de una zona llegan **en orden** al mismo consumidor,
que es exactamente la propiedad que la correlación por ventana necesita.

La alternativa evidente —particionar por tipo de evento— tiene dos
consecuencias a la vez: solo hay cinco valores posibles repartidos entre tres
particiones, con lo que el reparto queda desbalanceado, y los eventos de una
misma zona se dispersan, perdiendo su orden relativo.

### Cómo se guarda

Cada zona tiene un **conjunto ordenado de Redis** cuya puntuación es la marca
de tiempo del evento. Al llegar un evento:

1. Se inserta con su marca de tiempo como puntuación.
2. Se eliminan los que quedaron fuera de la ventana.
3. Se evalúan las reglas sobre lo que queda.

Dos detalles de implementación no son negociables:

**La puntuación va en milisegundos, no en nanosegundos.** La puntuación de un
conjunto ordenado de Redis es un número de coma flotante de doble precisión.
Una marca en nanosegundos excede la precisión entera que ese formato garantiza,
y dos eventos distintos acaban compartiendo puntuación. En milisegundos el
problema no existe hasta dentro de varios siglos.

**La poda ocurre en cada inserción.** Así el tamaño de la ventana queda acotado
por el tiempo y no por el volumen acumulado. Sin ella —confiando solo en un TTL
que se refresca al escribir— una zona con tráfico continuo tendría una ventana
que no expira nunca, y cada evaluación se encarecería hasta degradar la
latencia de todo el sistema.

Una métrica vigila esa cota: `correlator_window_events` por zona, con una
regla de alerta que salta si alguna supera los 5 000 eventos.

## 3. El motor

Por cada evento consumido, el motor evalúa **las cuatro reglas de zona**
contra la ventana de su zona. Si además el evento trae placa, recorre un
segundo camino independiente y evalúa la **regla de vehículo** contra una
ventana distinta, indexada por el seudónimo de la matrícula.

Cada regla devuelve una de tres cosas:

| Devuelve | Significa | Qué ocurre |
|---|---|---|
| Nada | El disparador de la regla no está presente | Se ignora |
| **Detección** | El patrón está completo | Se emite una alerta, salvo supresión |
| **Parcial** | El disparador está, falta el complemento | Se anota como candidato a falso positivo |

El tercer caso es el menos habitual en un motor de reglas, y el que hace
medible la tasa de falsos positivos. Se explica en el apartado 6.

## 4. Las cinco reglas

### 4.1 `possible_robbery` — posible robo en curso

**Disparador:** un botón de pánico.
**Complemento:** un vehículo a más de 80 km/h en la misma zona, detectado por
cámara de placas (`sensor.lpr`) **o** por sensor de velocidad
(`sensor.speed`).
**Ventana:** 2 minutos · **Confianza:** 0.85

Que la regla acepte ambas fuentes de velocidad importa. Consultando solo las
cámaras de placas, un sensor de velocidad disparado quedaría registrado sin
contribuir a ninguna correlación: una señal disponible que el sistema ignora.

Es la regla que reproduce el primer renglón de la tabla 2.4 del enunciado:
*zona 4, 22:05, placa XYZ123, 85 km/h → alerta de robo en curso*.

**El vehículo tiene que haber podido estar en el botón.** Comparar solo la zona
no basta: una zona mide varios kilómetros. Con tráfico realista apareció un robo
«con huida» formado por un coche rápido leído a 1.7 km del botón **un minuto
antes** de que lo pulsaran, es decir, un conductor cualquiera y un pulsador
accidental. La regla exige ahora que, por la distancia y el tiempo que los
separan, un mismo vehículo pudiera estar en ambos sitios (con un margen de 300 m,
porque las zonas y los sensores no tienen precisión de GPS). Se acepta tanto el
coche leído *después* —sale del botón— como el leído *antes* si le dio tiempo a
llegar —la víctima pulsa cuando ve arrancar al coche—. Sin coordenadas no se
puede juzgar y se acepta.

**La ventana de cada alerta empieza en lo primero que ocurrió.** Cada regla fijaba
el inicio en el de su disparador (el reporte en el accidente, el botón en el
robo), pero el disparador casi nunca es lo primero: el sensor acústico oye el
golpe y el reporte llega uno o dos minutos después. La alerta declaraba una
ventana que *empezaba después* de parte de su propia evidencia. Ahora el inicio es
el del evento más antiguo que sostiene la detección.

### 4.2 `accident` — accidente vial

**Disparador:** un reporte ciudadano de tipo `accidente`.
**Complemento:** una señal acústica de `explosion` o `vidrio_roto`.
**Refuerzo opcional:** una caída de velocidad, de más de 80 km/h a menos de 40
entre dos lecturas consecutivas.
**Ventana:** 5 minutos · **Confianza:** 0.85, o **0.95** con la caída.

Aquí está **el detalle más delicado de todo el motor**, y conviene explicarlo
porque el error es fácil de cometer y difícil de notar.

La forma intuitiva de escribir la regla es filtrar primero la lista de lecturas
de placa a las de velocidad superior a 80 km/h, y buscar después **sobre esa
misma lista ya filtrada** una caída por debajo de 40. Ninguna lectura puede
cumplir las dos condiciones a la vez, de modo que la condición se vuelve
imposible y **la alerta de accidente no se generaría nunca**: quedaría escrita,
documentada como funcional y muerta en la práctica.

La corrección tiene dos partes:

- **Conservar todas las lecturas** de la ventana, ordenarlas por momento y
  buscar la transición entre lecturas consecutivas. El filtro previo era
  justamente lo que hacía imposible la detección.
- **Hacer la caída opcional.** Un accidente sigue siendo un accidente aunque
  ningún sensor de velocidad lo haya registrado. La caída es evidencia
  adicional, no un requisito, y por eso sube la confianza de 0.85 a 0.95 en
  lugar de decidir si la alerta existe.

La lectura estricta de la guía se recupera con
`RULE_ACCIDENTE_EXIGE_CAIDA=true`.

### 4.3 `fire` — incendio

**Disparador:** un reporte ciudadano de tipo `incendio`.
**Complemento:** una señal acústica de `explosion`, **o** cualquier señal
acústica por encima de 100 decibelios.
**Ventana:** 5 minutos · **Confianza:** 0.95

Es la única regla donde un umbral cuantitativo sustituye a una clasificación:
un sonido suficientemente intenso vale como evidencia aunque el clasificador
no lo haya etiquetado como explosión.

### 4.4 `traffic_speed_violation` — exceso de velocidad sostenido

**Condición:** al menos **3** lecturas por encima de 80 km/h en la misma zona.
**Ventana:** 2 minutos · **Confianza:** 0.90

No tiene disparador ni complemento: es acumulativa. Exigir tres lecturas y no
una es lo que la distingue de un vehículo suelto que va rápido, que no es un
problema de la zona sino de ese conductor.

### 4.5 `vehicle_pursuit` — huida de un vehículo

**Condición:** al menos **2** infracciones del **mismo vehículo**, más un botón
de pánico en alguna de las zonas donde infringió.
**Ventana:** 15 minutos · **Confianza:** 0.92

Esta regla rompe la premisa de todas las anteriores: **no agrupa por zona, sino
por vehículo**. Y tiene que hacerlo, porque la secuencia que modela es
precisamente la que una ventana de zona no puede ver.

El caso: alguien pulsa el botón de pánico por un robo en la zona 1. Dos minutos
después, una cámara de semáforo capta una placa cruzando en rojo en esa misma
zona. Dos minutos más tarde, la misma placa aparece a 112 km/h ya en la zona 4.
Para la ventana de la zona 1, el tercer evento no existe; para la de la zona 4,
no existe el primero. Por separado son una alerta de robo y una multa de
tránsito. Juntas son una huida.

Cuenta como infracción una lectura con el campo `infraccion` presente —que es
como se modela el cruce en rojo— o una velocidad por encima del umbral.

**El botón de pánico es el ancla, deliberadamente.** Sin él, dos infracciones
del mismo coche son solo un conductor imprudente, y emitir «persecución» por
eso llenaría el tablero de falsas huidas hasta que el operador dejara de
mirarlo. Y el pánico debe estar en una zona por la que el vehículo pasó: uno al
otro lado de la ciudad no guarda relación con ese coche, y aceptarlo
convertiría la regla en un generador de coincidencias.

De los botones de pánico candidatos se toma **el más próximo en el tiempo a la
primera infracción**, no el más antiguo. La diferencia aparece cuando hay dos
incidentes solapados en la misma zona: con «el más antiguo», una huida que
empieza a las 10:30 se anclaría en un pulsador de las 10:22 que no tuvo nada
que ver, y la alerta mezclaría dos incidentes en una sola cadena de evidencias.
Se vio ocurrir sobre el sistema en ejecución. Tampoco se exige que el pánico
*preceda* a la infracción, porque la víctima puede pulsar el botón después de
ver arrancar el coche.

**La regla se evalúa también al llegar un botón de pánico.** Un pulsador no
trae matrícula, de modo que no alimenta ninguna ventana de vehículo; pero es el
ancla de la regla, y al llegar hay que revisar si algún vehículo de esa zona ya
tenía una cadena a medias esperándolo. Sin esa segunda rama existe una carrera
real: Kafka reparte por zona, así que la lectura de la zona 9 y el pánico de la
zona 1 viajan en **particiones distintas** y su orden relativo no está
garantizado. Si la partición del pánico va retrasada —lo normal tras un
reinicio o un rebalanceo— la regla se evalúa antes de que exista su ancla y la
alerta no se emite nunca. Se observó con diecisiete segundos de desfase: la
cadena completa quedó solo como coincidencia parcial.

**La cadena se comprueba contra la física.** Antes de contar las infracciones,
el motor descarta aquellas a las que el vehículo no pudo llegar desde la
anterior. Se mide la distancia geodésica entre las dos apariciones y el tiempo
transcurrido; si la velocidad media que haría falta supera
`RULE_VELOCIDAD_MAXIMA_PLAUSIBLE` (70 km/h por defecto), el enlace se rompe.

No es un límite de velocidad, es un límite de **credibilidad**. Y el valor está
medido, no estimado: preguntando al callejero real de la capital —el mismo que
dibuja los recorridos— un automóvil **a flujo libre, sin un solo semáforo en
rojo**, promedia entre 26 y 34 km/h *medidos en línea recta*, porque las calles
no van rectas:

| Tramo | En recta | Por calle | Tiempo | Velocidad en recta |
|---|---|---|---|---|
| zona 1 → zona 4 | 1.69 km | 2.74 km | 204 s | 30 km/h |
| zona 4 → zona 9 | 2.56 km | 3.32 km | 325 s | 28 km/h |
| zona 10 → zona 6 | 6.57 km | 7.77 km | 699 s | 34 km/h |

El tope se fija en **el doble** de esa cota superior: un vehículo que huye
saltándose las normas cabe de sobra, y un salto imposible no.

El primer valor que se puso fue 150 km/h, y era un error de criterio: eso es un
límite *físico*, no de tráfico. Descartaba la teletransportación pero daba por
buenas cadenas que ningún coche podría recorrer en una ciudad con semáforos
—por ejemplo cruzar 6.57 km en 200 segundos, que son 118 km/h en recta y
exigirían 140 km/h de media por asfalto—. Sin él, el
motor encadenaba una lectura de la zona 10 con otra de la zona 6 dos segundos
después: **6.57 km que exigirían 11 834 km/h**. Se verificó sobre el sistema en
ejecución, y la alerta resultante situaba el incidente en una zona por la que el
coche nunca pasó.

Un salto imposible casi nunca es un coche rápido. Es una placa **mal leída** por
la cámara —basta un dígito confundido— o una placa **clonada**, que son
problemas corrientes de los lectores automáticos. Por eso se descarta esa
lectura y la cadena continúa con la siguiente que sí sea alcanzable, en vez de
cortarse: lo que no encaja es el dato, no el incidente. Y se mide siempre contra
la última aparición *aceptada*, no contra la inmediatamente anterior, para que
una sola lectura falsa no desplace el punto de referencia y arrastre consigo a
las lecturas buenas que vengan después.

Una aparición sin coordenadas no se puede juzgar, y ante la duda se acepta:
descartar por falta de dato convertiría un hueco de información en una
correlación perdida, que es el más caro de los dos errores.

Esto tiene una consecuencia práctica en las pruebas. El escenario `persecucion`
envía los eventos **fechados hacia atrás**, y las separaciones **no están
escritas a mano: se le preguntan al motor de rutas** en cada ejecución. Entre la
zona 1 y la zona 4 el callejero dice 3 min 23 s, y entre la 4 y la 9 otros
5 min 25 s, de modo que el incidente completo abarca algo más de nueve minutos.
El resultado medido sobre el sistema: 34 km/h de media, que es exactamente lo
que da de sí ese tramo.

Enviados de corrido, como estaban al principio, la cadena exigía 5 083 km/h
entre la zona 1 y la zona 4. Con separaciones de 150 segundos escritas a ojo
—el intento intermedio— seguía siendo un 36 % más rápida que el flujo libre.
Un escenario así no prueba el sistema: prueba una situación que no puede
ocurrir, y si el motor la acepta nadie se entera hasta que alguien mira el mapa.

El multiplicador `-trafico` modela la congestión: `1.0` es flujo libre, que es
lo que hace un vehículo que huye saltándose las señales, y `1.8` se aproxima a
una hora pico en esta ciudad.

Dos detalles de implementación que condicionan el resto:

- **La ventana se indexa por el seudónimo HMAC de la placa, no por la placa.**
  Así la clave de Redis no es una matrícula legible con un `SCAN`, y además
  coincide con el identificador archivado en PostgreSQL, lo que permite cruzar
  la alerta con el histórico del vehículo. Ver
  [ADR-007 §3.6](adr/007-privacidad-y-datos-personales.md).
- **`RULE_VENTANA_PERSECUCION` no puede superar `CORRELATION_WINDOW_TTL`.** El
  ancla vive en la ventana de zona: mirar más atrás que ella sería buscar un
  evento que Redis ya podó, y la regla dejaría de disparar sin que nada lo
  indicara. El correlator comprueba la relación al arrancar y se niega a seguir
  si no se cumple.

### 4.6 Resumen

| Alerta | Disparador | Complemento | Agrupa por | Ventana | Confianza |
|---|---|---|---|---|---|
| `possible_robbery` | botón de pánico | vehículo > 80 km/h | zona | 2 min | 0.85 |
| `accident` | reporte de accidente | señal acústica | zona | 5 min | 0.85 |
| `accident` + caída | ídem, con caída 80→40 | ídem | zona | 5 min | **0.95** |
| `fire` | reporte de incendio | explosión o > 100 dB | zona | 5 min | 0.95 |
| `traffic_speed_violation` | — | 3 lecturas > 80 km/h | zona | 2 min | 0.90 |
| `vehicle_pursuit` | botón de pánico | 2 infracciones del mismo vehículo | **vehículo** | 10 min | 0.92 |

La confianza es un **número real entre 0 y 1**, no un entero. Declararla entera
parece inocuo y no lo es: al redondear, 0.85, 0.90 y 0.95 se guardarían las
tres como `1` y toda la gradación de confianza entre reglas se perdería al
persistir. La columna es `numeric`, con una restricción
`CHECK (score >= 0 AND score <= 1)`.

## 5. Supresión de alertas

Un incidente en curso genera eventos continuamente. Sin control, un robo
produciría decenas de alertas idénticas en un minuto y el operador dejaría de
mirarlas.

El correlator reserva en Redis una marca por **tipo de alerta y zona**, con un
tiempo de vida configurable. Mientras esa marca exista, una alerta equivalente
no se emite: se cuenta como suprimida.

El equilibrio es delicado, porque la supresión que evita ruido puede, en
exceso, **esconder incidentes distintos en la misma zona**. Por eso una regla
de alerta vigila la proporción y avisa si se suprime más del 90 %: es la señal
de que `ALERT_SUPPRESS_TTL` probablemente sea demasiado largo.

## 6. Falsos positivos

El segundo renglón de la tabla 2.4 del enunciado —*zona 10, 17:22, sin eventos
complementarios*— debe producir **ausencia de alerta**. El sistema hace algo
más que no emitirla: **la registra**.

### Cómo funciona

Cuando una regla encuentra su disparador pero no su complemento, anota una
**coincidencia parcial** en Redis, con la regla, la zona, el evento disparador,
qué falta y cuándo vence la ventana. La anotación es idempotente por
disparador: si el mismo evento vuelve a quedar parcial en evaluaciones
sucesivas, no se cuenta dos veces.

A partir de ahí hay dos desenlaces:

- **Llega el complemento** → se emite la alerta y las parciales de esa regla y
  zona se descartan: dejaron de ser candidatas.
- **Vence la ventana sin que llegue** → un barrido periódico, cada 30 segundos,
  lo confirma como falso positivo, incrementa
  `correlator_false_positive_candidates_total{rule,zone}` y lo registra
  nombrando **qué complemento faltó**.

### Por qué importa

Sin ese registro, un botón de pánico que nunca encuentra su complemento
simplemente no genera nada, y **una zona tranquila es indistinguible de una
zona llena de señales incompletas**. La primera está bien; la segunda indica
que la regla es demasiado estricta, que la ventana es demasiado corta, o que
hay sensores mal calibrados.

Una regla de alerta vigila esa proporción: si más de la mitad de las
coincidencias parciales expira sin completarse, el modelo de correlación
necesita revisión.

El enunciado lo pide como caso de uso a simular en su sección 7, y su sección
10.3 añade que *"el análisis de falsos positivos o correlación compleja puede
considerarse mérito adicional"*.

## 7. La evidencia

Toda alerta lleva adjunta la lista de identificadores de los eventos que la
sustentan, junto con la ventana temporal en la que ocurrieron y la versión de
las reglas con que se evaluó.

Eso permite tres cosas que sin ello serían imposibles:

- **Reconstruir el incidente** desde la alerta, evento por evento.
- **Auditar una alerta discutida**: si alguien cuestiona una detección, la
  evidencia dice exactamente qué la produjo.
- **Comparar entre versiones de reglas**: `rule_version` viaja en la alerta, de
  modo que un cambio de umbrales queda trazable en los datos históricos.

Además, el `trace_id` entra por cabecera HTTP, viaja en las cabeceras del
mensaje de Kafka y aparece en el registro de los cinco servicios: **un solo
identificador reconstruye el recorrido completo** de cualquier evento.

## 8. Parámetros

Los umbrales se ajustan **sin recompilar**, por variable de entorno. Un valor
sin sentido **impide arrancar el servicio**.

| Variable | Por defecto | Qué controla |
|---|---|---|
| `RULE_VENTANA_ROBO` | `2m` | Ventana de `possible_robbery` |
| `RULE_VENTANA_ACCIDENTE` | `5m` | Ventana de `accident` |
| `RULE_VENTANA_INCENDIO` | `5m` | Ventana de `fire` |
| `RULE_VENTANA_VELOCIDAD` | `2m` | Ventana de `traffic_speed_violation` |
| `RULE_UMBRAL_VELOCIDAD` | `80` | km/h a partir de los cuales un vehículo va "rápido" |
| `RULE_MIN_LPR_VELOCIDAD` | `3` | Lecturas rápidas necesarias para el exceso sostenido |
| `RULE_CAIDA_DESDE` | `80` | km/h de partida de una caída de velocidad |
| `RULE_CAIDA_HASTA` | `40` | km/h de llegada de una caída de velocidad |
| `RULE_UMBRAL_DECIBELES` | `100` | dB que bastan por sí solos como señal de incendio |
| `RULE_ACCIDENTE_EXIGE_CAIDA` | `false` | Si la caída es obligatoria o solo refuerza |
| `ALERT_SUPPRESS_TTL` | `5m` | Cuánto se suprime una alerta equivalente |
| `CORRELATION_WINDOW_TTL` | `10m` | Vida de la ventana de una zona en Redis |
| `CORRELATION_SEEN_TTL` | `10m` | Vida del registro de idempotencia por evento |
| `RULE_VERSION` | `1.0` | Versión que se estampa en cada alerta emitida |

### Por qué se validan al arrancar

Un umbral de velocidad en cero no produce ningún error: produce un motor que
considera *"vehículo a alta velocidad"* a todo lo que pase, y la ciudad se
llena de alertas de robo. Una ventana negativa produce lo contrario: un motor
que no detecta nada. Una caída invertida —de lento a rápido— hace que la
transición no se detecte jamás y que el accidente pierda su refuerzo de
confianza en silencio.

Ninguno de los tres se manifiesta como un fallo, así que se rechazan antes de
arrancar y el error nombra **todos** los problemas a la vez:

```
fallo al arrancar el correlator: configuracion del motor de reglas invalida:
  - RULE_CAIDA_DESDE (30) debe ser mayor que RULE_CAIDA_HASTA (80): una caida de velocidad va de rapido a lento
  - RULE_UMBRAL_VELOCIDAD="0": debe ser mayor que cero
```

Los umbrales vigentes se registran al arrancar, porque una alerta generada con
parámetros ajustados es indistinguible de una generada con los de fábrica, y
`rule_version` no cambia al mover una variable de entorno.

## 9. Cómo verificarlo

```bash
./make.sh load enunciado
```

Reproduce los **dos** renglones de la tabla 2.4 del enunciado y comprueba
ambos contra las métricas del correlator:

```
Renglon 1 - zona_4, 22:05: panico + placa XYZ123 a 85 km/h
           esperado: alerta possible_robbery
           -> alerta emitida  [OK]

Renglon 2 - zona_10, 17:22: boton de panico sin eventos complementarios
           esperado: ninguna alerta, y un candidato a falso positivo
           -> ninguna alerta emitida  [OK]
           -> falso positivo confirmado y contabilizado  [OK]
```

El segundo renglón tarda unos dos minutos: hay que esperar a que cierre la
ventana y pase el barrido. Con `-espera 0` se omite esa espera.

```bash
./make.sh load alertas
```

Dispara los cuatro tipos de alerta, uno por zona, y deja una vigente de cada
uno.

Las **26 pruebas unitarias del motor** cubren cada regla, sus casos límite y la
validación de umbrales, sin necesidad de levantar nada. Varias existen para
impedir que se reintroduzcan los errores sutiles descritos arriba, como el
filtro que vuelve inalcanzable la regla de accidente.

## 10. Límites conocidos del modelo

Conviene enumerarlos porque son las preguntas previsibles en la defensa.

**Las reglas están escritas a mano.** No hay aprendizaje automático: los
umbrales los fijó una persona y solo cambian si alguien los cambia. Es una
decisión consciente —un modelo entrenado exigiría datos etiquetados que no
existen— pero significa que el sistema no mejora solo.

**La zona es una unidad espacial gruesa.** Dos eventos en extremos opuestos de
una zona grande se correlacionan igual que dos que ocurren en la misma esquina.
Las coordenadas están disponibles en el evento y no se usan para ponderar la
proximidad.

**La confianza es una constante por regla, no un cálculo.** Salvo el refuerzo
de la caída de velocidad, el valor no depende de cuánta evidencia haya. Tres
botones de pánico producen la misma confianza que uno.

**No hay correlación entre zonas.** Un vehículo que cruza tres zonas seguidas
genera tres evaluaciones independientes. Seguir una trayectoria exigiría una
ventana por placa además de por zona.

**Las ventanas no distinguen la hora.** Un umbral de velocidad razonable a las
tres de la madrugada no lo es a las seis de la tarde, y el modelo no lo tiene
en cuenta.

---

## Referencias

- Enunciado del proyecto final, secciones 2.4, 7 y 10.3.
- [ADR-004](adr/004-broker-de-eventos.md) — particionado por zona, que es lo
  que hace viable la correlación por ventana.
- [ADR-006](adr/006-resiliencia-y-tolerancia-a-fallos.md) — idempotencia y
  garantías de entrega del flujo que alimenta al motor.
- [ADR-008](adr/008-observabilidad-y-slo.md) — las métricas y las reglas de
  alerta que vigilan el comportamiento del modelo.
- [`services/correlator/internal/rules`](../services/correlator/internal/rules) —
  implementación y pruebas.
- [`services/correlator/internal/window`](../services/correlator/internal/window) —
  ventanas y coincidencias parciales.
