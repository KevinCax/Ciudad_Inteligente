# Simulación de la ciudad

Hay dos formas de usarla, y sirven para cosas distintas:

| | Simulación controlada (`demo`) | Simulación en tiempo real (`ciudad`) |
|---|---|---|
| Para qué sirve | **Demostrar**: cada caso explica una capacidad o un límite | **Probar con carga**: tráfico, hora pico, incidentes al azar |
| Cuánto dura | ~7 minutos | Lo que se le deje (horas para que haya incidentes suficientes) |
| Resultado | Determinista | Depende del azar |
| Se evalúa | Caso por caso | Por cobertura y precisión |

```bash
./make.sh limpiar-datos && ./make.sh demo     # controlada: prepara, reproduce y evalúa
./make.sh ciudad 45m 3                         # en tiempo real, con el triple de incidentes
```

## Simulación controlada

Una lista de casos escritos uno a uno, cada uno con lo que el sistema **debe
producir por diseño**. Al terminar, `./make.sh evaluar` contrasta lo que produjo
con lo esperado y devuelve una tabla que se puede explicar fila por fila.

El sistema **no ve** lo esperado: la simulación lo guarda en un archivo aparte
(`test/load/verdad-simulacion.json`), no viaja en ningún evento. Un detector que
supiera la respuesta no demostraría nada.

| Caso | Debe producir | Qué demuestra |
|---|---|---|
| Robo con huida, bien cubierto | robo + persecución | La cadena completa, de extremo a extremo |
| Accidente con sensor y caída de velocidad | accidente (0.95) | La confianza sube cuando hay caída de velocidad |
| Incendio con sensor acústico | incendio | Correlación de un reporte con un sensor |
| Tres coches a más de 80 km/h | exceso de velocidad | Regla acumulativa por zona |
| Huida que solo pasa por una cámara | robo, **no** persecución | La persecución exige dos infracciones |
| Botón pulsado por error | nada | Un pulsador solo no es un incidente |
| Coche rápido a 2 km, un minuto antes del pulsador | nada | Un coche que no pudo estar en el botón no huye de él |
| Coche rápido junto a un pulsador accidental | robo (**falsa alarma**) | Límite: una coincidencia indistinguible de un robo |
| Misma placa a 7 km en 25 segundos | robo, **no** persecución | El filtro de plausibilidad descarta la lectura imposible |
| Accidente / incendio sin sensor acústico | nada | Límite: un reporte ciudadano solo no lo confirma ninguna regla |
| Altercado, petardo | nada | Ruido que ninguna regla correlaciona |
| Segundo accidente en el mismo cruce a los 2 min | nada | Supresión de repetidas |

Los casos se reparten en zonas distintas a propósito. Un pulsador de un caso y un
coche rápido de otro, en la misma zona y a pocos minutos, se correlacionarían
entre sí y la tabla dejaría de poder explicarse fila por fila.

**Dos casos no se pudieron preparar con esta red de sensores**, y lo dice la propia
salida: una huida por calles sin ninguna cámara (todas las rutas de entre 2.5 y 8 km
pasan por alguna, lo que habla de la densidad de la red) y una huida cuyas cámaras
queden todas fuera de la zona del botón.

Todos los casos son **físicamente coherentes**: los tiempos de un coche entre un
pulsador y una cámara salen del callejero, no de un número escrito a mano. Un guion
inicial puso 30 segundos para recorrer 640 m de calle (77 km/h) y la verificación
contra el callejero lo detectó, aunque en línea recta cuadraba.

## Simulación en tiempo real

Un mundo que sigue funcionando, para probar el sistema como si estuviera en
producción en Ciudad de Guatemala.

```bash
./make.sh catalogo                 # una vez: genera la red fija de sensores
./make.sh ciudad                   # corre hasta Ctrl+C
./make.sh ciudad 45m 3             # 45 minutos, con el triple de incidentes
./make.sh ciudad 45m 3 07:30       # igual, con la actividad de la hora pico
./make.sh verificar                # comprueba que todo lo correlacionado es coherente
```

## Qué es, y qué no es

**Es** una simulación de alta fidelidad. Los vehículos recorren calles reales,
las cámaras los leen cuando pasan por delante y a la hora a la que pasan, hay
hora pico y hora valle, y de vez en cuando ocurre algo sin que nadie lo ordene.

**No es** una conexión a sensores reales, y conviene decirlo con estas palabras
en cualquier presentación:

- **Los eventos llevan `source: "simulated"`**, el único valor que admite el
  contrato. Conectar sensores reales exigiría cambiar el contrato y escribir los
  productores de cada fabricante.
- **La red de sensores es inventada.** Las posiciones están sobre calles reales y
  en los cruces más transitados según el callejero, pero **no es el inventario
  de cámaras de la Municipalidad**, que no se tiene. La advertencia está escrita
  dentro del propio `deploy/sensores/catalogo-guatemala.json`.
- **La frecuencia de incidentes es plausible, no medida.** No hay estadísticas de
  la Municipalidad de las que partir.
- **Los centros de las 22 zonas son aproximados**, como ya constaba.

## Cómo está hecha

### La red de sensores (`deploy/sensores/catalogo-guatemala.json`)

Se genera una vez y se guarda en disco, porque **un sensor real no cambia de
sitio**. Antes, cada escenario inventaba las posiciones al vuelo y la cámara
«cruce_6a_avenida» aparecía en cuatro sitios distintos.

El criterio es que las cámaras estén donde pasa la gente. Se calculan rutas
entre cada zona y sus tres vecinas más cercanas, y se cuenta cuántas pasan por
cada cruce: los más transitados reciben cámara. Así la red queda concentrada en
las arterias y sin huecos en el centro, como se instalaría de verdad, y no
repartida al azar.

| Sensor | Cantidad | Dónde |
|---|---|---|
| Cámaras de placas (LPR) | 82 | Cruces más transitados; 27 en cruces con semáforo |
| Radares de velocidad | 10 | Tramos rápidos |
| Sensores acústicos | 28 | Repartidos por la ciudad |
| Botones de pánico | 41 | Dos por zona, en puntos públicos |

La misma semilla da siempre la misma red.

### El tráfico

Cada vehículo sale de un punto y llega a otro por la ruta que calcula el motor de
rutas. Cuando pasa a menos de 40 m de una cámara, esta lo lee **a la hora a la
que llega a su altura**, a la velocidad a la que circula en ese tramo.

- Hay un parque de 4 000 vehículos con placas de formato guatemalteco (`P123ABC`,
  `C…` comercial, `M…` moto). Los mismos coches repiten viaje, como quien va a
  trabajar cada día, y por eso una misma placa aparece varias veces.
- Un 4 % conduce bastante más rápido que el resto.
- **Las cámaras no son perfectas**: se les escapa un 3 % de los coches y confunden
  un carácter de la placa el 0.5 % de las veces, generando matrículas que no
  existen o que son las de otro coche. Es lo que hace un lector automático real, y
  pone a prueba el filtro de plausibilidad del correlator.
- Solo una cámara de un cruce con semáforo puede ver un cruce en rojo.

### Hora pico y hora valle

La actividad sigue una curva de dos picos (de 7 a 8 de la mañana y de 17 a 18 de
la tarde) y un valle profundo de madrugada, en hora de Guatemala. En hora pico se
circula a dos tercios de la velocidad normal. Las cifras son plausibles, no
medidas.

`-hora 07:30` desplaza **solo la curva**, no el reloj: los eventos siguen llevando
la hora real de generación, que es lo que el sistema comprueba.

### Los incidentes

Ocurren solos, como procesos de Poisson cuya frecuencia sube con la actividad
pero menos que ella: un robo no depende tanto de la hora pico como el tráfico.

| Incidente | Frecuencia base | Qué genera |
|---|---|---|
| Robo con huida | 3 / hora | Botón de pánico y un vehículo que se aleja saltándose semáforos y por encima de la velocidad |
| Pánico falso | 10 / hora | Un botón pulsado y nada más |
| Accidente | 5 / hora | Una cámara ve el coche entrar rápido y salir casi parado, un sensor acústico oye el golpe y llega un reporte ciudadano |
| Incendio | 1.5 / hora | Una explosión en un sensor acústico y un reporte ciudadano |
| Altercado | 12 / hora | Un reporte que ninguna regla correlaciona: es ruido |
| Ruido falso | 6 / hora | Un sensor acústico que oye algo que no es un incidente |

El multiplicador `intensidad` los escala todos.

Algunos incidentes **no se detectan**, y es a propósito: un accidente donde no hay
sensor acústico cerca, o un fugitivo que escoge una ruta sin cámaras. En la vida
real tampoco se detecta todo.

Y algunas alertas **no serán reales**: un pánico falso que coincide con un coche
rápido en la misma zona disparará una alerta de robo. Es lo que hacen las reglas
y es la falsa alarma que un operador tendrá que descartar.

## Por qué corre en tiempo real y a ritmo 1:1

Acelerar el reloj no es posible sin falsear el resultado. La supresión de alertas
repetidas se mide en tiempo de pared, de modo que dos robos separados por veinte
minutos *simulados* pero por doce segundos *reales* se tomarían por el mismo
incidente. Un sistema en producción tampoco admite que se le acelere el tiempo.

La consecuencia es que **empieza vacío**, como un sistema recién desplegado: para
tener un día de histórico hay que dejarlo correr un día.

## Lo que encontró

Correr el sistema con tráfico realista sacó tres defectos que los escenarios
sintéticos no podían encontrar, porque en estos el orden de los eventos y la
cercanía entre ellos eran siempre los cómodos:

1. **En un Kafka recién creado, el persister y el notificador no consumían las
   alertas.** Arrancaban antes de que `kafka-init` creara el topic
   `correlated.alerts`, se unían al grupo con un topic inexistente, y el grupo
   quedaba «estable» con **cero particiones asignadas**. `kafka-go` no se entera
   de que el topic aparece después. Ningún error, ningún aviso: las alertas
   no se archivaban ni se notificaban. Ahora cinco servicios esperan a
   `kafka-init` (`service_completed_successfully`).
2. **Las alertas declaraban una ventana que empezaba después de parte de su
   evidencia.** El disparador no es lo primero que ocurre.
3. **Un robo «con huida» formado por un coche que estaba a 1.7 km del botón.** La
   regla solo comparaba zonas.

Los tres los cazó `./make.sh verificar` o la inspección de los grupos de consumo,
no un test unitario.

## Qué comprobar después

```bash
./make.sh verificar
```

Comprueba que las evidencias existen y caen dentro de la ventana de su alerta, que
ningún tramo exige una velocidad imposible, que toda alerta generó aviso sin
duplicados, que los recorridos caben en el tiempo transcurrido y que las consultas
de los paneles devuelven datos.
