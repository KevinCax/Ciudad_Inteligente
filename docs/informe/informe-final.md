# Introducción

La seguridad ciudadana de una capital genera un volumen de señales que ningún operador humano puede vigilar simultáneamente. Cámaras de lectura de placas, botones de pánico, sensores de velocidad, micrófonos y reportes de ciudadanos producen, cada uno por su lado, datos que aisladamente dicen poco. Una placa que pasa a 85 km/h no es un delito. Un botón de pánico pulsado puede ser un error. Un sonido de vidrio roto puede ser un accidente doméstico. **Los tres juntos, en la misma zona y dentro del mismo minuto, son un robo en curso.**

Ese salto —de señales sueltas a incidentes con significado— es el problema que aborda este proyecto. No es un problema de sensores: es un problema de arquitectura.

## Planteamiento del problema

La Municipalidad de Guatemala, cliente hipotético que este grupo declara atender, opera una capital de área metropolitana extensa con un equipo de sistemas reducido. Enfrenta tres restricciones que condicionan cualquier solución técnica.

La primera es de **disponibilidad**: la detección de incidentes de seguridad no puede depender de un enlace a internet. Un corte de conectividad no puede traducirse en una ciudad que deja de ver lo que ocurre en sus calles.

La segunda es de **legalidad**: las placas vehiculares y las coordenadas geográficas son datos personales. Aunque en este proyecto sean sintéticos, tratarlos como si no lo fueran invalidaría el ejercicio, porque una institución pública guatemalteca está obligada por el Decreto 57-2008 a proteger los datos personales que custodia.

La tercera es de **sostenibilidad operativa**: el sistema lo heredará un equipo pequeño. Toda complejidad que se introduzca hoy se paga todos los meses durante años.

## Objetivos

### Objetivo general

Diseñar, justificar y construir una simulación funcional de una plataforma de monitoreo perimetral urbano basada en arquitectura distribuida orientada a eventos, de modo que cada decisión técnica relevante quede respaldada por un análisis de alternativas defendible ante actores no técnicos.

### Objetivos específicos

- Implementar una arquitectura de microservicios capaz de ingerir, validar, correlacionar, archivar e indexar eventos urbanos heterogéneos en tiempo real.
- Definir y verificar un objetivo de nivel de servicio cuantitativo para la latencia de detección, junto con la política de actuación cuando se incumpla.
- Registrar como *Architecture Decision Record* cada decisión de arquitectura costosa de revertir, con sus alternativas y sus consecuencias.
- Establecer una frontera de privacidad explícita que impida que datos identificables de ciudadanos salgan del control municipal.
- Evaluar el comportamiento del sistema ante picos de carga, ante la caída de un componente y ante el paso del tiempo sobre el archivo histórico.

## Alcance y limitaciones

El alcance es una **simulación lógica**. No se emplean dispositivos físicos de adquisición: los cinco tipos de sensor urbano se emulan programáticamente con un generador de carga escrito para este proyecto. Esta es una condición del enunciado, no una concesión.

Dentro del alcance quedan la ingesta, la validación contra contrato, la deduplicación, la correlación en ventanas temporales —tanto por zona como por vehículo—, la generación de alertas, el registro trazable de a qué organismo de respuesta se avisa de cada una, el archivado estructurado, la indexación para búsqueda histórica, la retención de datos, la observabilidad y la visualización.

Fuera del alcance quedan la **entrega real** a los sistemas de despacho de emergencias, el reconocimiento de imágenes, cualquier forma de aprendizaje automático sobre los eventos, y la infraestructura física de red. La distinción con el despacho importa: el sistema decide a qué cuerpo de respuesta corresponde cada alerta y lo registra con su canal, su hora y su resultado, pero el canal de entrega está simulado y ningún cuerpo recibe el aviso. Conectar esa decisión con una central de emergencias exige convenios y protocolos acordados con cada organismo, que no son decisiones de arquitectura. Las limitaciones técnicas concretas que el sistema conserva se enumeran sin adornos en la sección *Limitaciones y trabajo futuro*.

## Metodología de trabajo

El trabajo se ordenó alrededor de una regla: **ninguna decisión relevante se toma sin escribir antes por qué**. Cada elección costosa de revertir se registró como *Architecture Decision Record* con sus alternativas y sus consecuencias, y solo después se implementó.

Esa disciplina tuvo un efecto que conviene señalar, porque es el hallazgo metodológico del proyecto: **razonar cada decisión hizo visibles errores que el código habría ocultado**. Varias de las decisiones descritas en este informe existen para evitar fallos que no producen ningún error visible —una regla que no puede dispararse, una alerta que se redondea al persistir, un evento que desaparece sin dejar rastro— y que por tanto no se descubren probando, sino pensando.

El apartado de evaluación recoge además tres defectos que aparecieron al verificar, no al construir, y que comparten ese mismo rasgo.

<!-- salto -->

# Ciudades inteligentes: referentes y comparación

El enunciado pide comparar la solución con casos reales. La comparación tiene valor si se hace sobre decisiones de arquitectura y no sobre catálogos de funcionalidades, de modo que esta sección se organiza en torno a qué resolvió cada referente y qué se tomó o se descartó de él.

## Qué resuelve una plataforma de monitoreo urbano

Las plataformas de ciudad inteligente orientadas a seguridad comparten una misma forma: muchos productores heterogéneos, un transporte de eventos común, un motor que correlaciona y un centro donde alguien decide. Lo que las diferencia es **dónde ponen el centro de gravedad**: en la sala de control, en la plataforma de datos abiertos, o en la capacidad analítica.

## Centro de Operações do Rio de Janeiro

El referente más citado en la región es el Centro de Operações de Río de Janeiro, inaugurado en 2010 y construido con IBM, que integró en una sola sala de control a decenas de dependencias municipales que antes operaban por separado. Su centro de gravedad es **organizativo antes que técnico**: el logro principal fue poner a los responsables de tránsito, defensa civil, salud y policía a mirar la misma pantalla.

De ahí se tomó una idea y se descartó otra. Se tomó la noción de **incidente como unidad de trabajo**: lo que llega al operador no es un evento sino una correlación ya interpretada, con su evidencia adjunta. Se descartó la dependencia de una sala física permanentemente atendida; este sistema asume que **nadie está mirando** y por eso invierte en alertas automáticas que buscan a la persona, según se detalla en el ADR-008.

La literatura crítica sobre Río también deja una advertencia que se tomó en serio: una plataforma de este tipo concentra capacidad de vigilancia sobre la población, y esa concentración exige contrapesos explícitos. Ese es el origen del ADR-007.

## Barcelona y la plataforma Sentilo

Barcelona desarrolló Sentilo, una plataforma de sensores publicada como software libre, construida sobre un modelo de publicación y suscripción con un catálogo explícito de sensores y sus tipos de dato. Su centro de gravedad es **el contrato**: el valor no está en el motor sino en que cualquier proveedor pueda conectar un sensor nuevo sin tocar la plataforma.

Esa es la idea que más influyó en este proyecto. El evento canónico versionado que se describe más adelante, publicado por los propios servicios en `/events/schema`, cumple exactamente esa función: un productor nuevo se integra leyendo el contrato, no leyendo el código.

## Singapur

El programa Smart Nation de Singapur, iniciado en 2014, opera a una escala y con un presupuesto que no admiten comparación directa con el caso que aquí se atiende. Su aporte al análisis es **por contraste**: es el ejemplo de la arquitectura que este proyecto conscientemente no construyó. Donde Singapur puede permitirse una plataforma nacional con elasticidad ilimitada y equipos dedicados, la Municipalidad de Guatemala necesita un sistema que un equipo reducido pueda operar.

Copiar la arquitectura de un referente sin copiar su contexto es el error que el ADR-001 trata de evitar al declarar explícitamente el cliente antes de decidir nada.

## El contexto guatemalteco

La Municipalidad de Guatemala ya opera infraestructura de monitoreo urbano —videovigilancia y gestión de tránsito a través de la Empresa Metropolitana Reguladora de Transporte y Tránsito— lo que significa que este sistema no llegaría a un vacío sino a un entorno con sistemas previos. Esa constatación tiene una consecuencia de diseño concreta: **la vía de integración es el contrato de eventos**, no la sustitución de lo existente. Un sistema previo se integra publicando en el ingestor, sin que nada más cambie.

:::tabla Comparación de referentes por centro de gravedad arquitectónico
| Referente | Centro de gravedad | Qué se tomó | Qué se descartó |
|---|---|---|---|
| Río de Janeiro (COR) | Sala de control integrada | El incidente correlacionado como unidad de trabajo | La dependencia de vigilancia humana permanente |
| Barcelona (Sentilo) | Contrato de sensores | Evento canónico versionado y publicado | El catálogo centralizado de dispositivos |
| Singapur (Smart Nation) | Escala y analítica nacional | Nada directamente | La arquitectura elástica de presupuesto abierto |
| Este proyecto | Autonomía del camino crítico | — | — |
:::nota Elaboración propia. Las características de cada referente deben respaldarse con su fuente primaria; véase el Anexo C.
:::

## Qué distingue a esta propuesta

Ninguno de los referentes anteriores declara como propiedad de diseño que **la detección sobreviva a un corte de internet**. En arquitecturas construidas para ciudades con conectividad redundante, esa propiedad se da por supuesta. Aquí no puede suponerse, y por eso se convirtió en el criterio que ordena todas las demás decisiones: el camino crítico completo —ingesta, transporte, correlación y alerta— se ejecuta dentro de la red municipal, y solo el archivo histórico y la analítica cruzan hacia la nube.

<!-- salto -->

# Arquitectura propuesta

## Modelo de despliegue híbrido

El sistema se despliega en dos capas con una frontera explícita, documentada en el ADR-001.

:::imagen Esquemático de infraestructura: nodos, servicios y segmentos de red
../diagramas/infraestructura.png
:::nota Elaboración propia. El diagrama se genera desde código (`docs/diagramas/infraestructura.py`) en SVG y PNG a partir de una misma definición, de modo que ambos formatos no puedan discrepar.
:::

La propiedad central es verificable con una sola pregunta: *¿qué deja de funcionar si se cae el enlace?* La respuesta es el archivo histórico y los tableros, que se ponen al día solos al volver. La detección de incidentes continúa.

Esa propiedad no se obtiene sola, y el diseño ingenuo la pierde. Si el servicio de ingesta escribiera en la base de datos de forma síncrona antes de responder —que es la forma natural de escribirlo— un corte del enlace detendría la ingesta por completo. Conservarla exigió **extraer la persistencia a servicios propios** que consumen de Kafka de forma asíncrona, y es la decisión estructural más importante de esta arquitectura.

## Los cinco microservicios

:::tabla Microservicios, responsabilidad y dependencia del enlace
| Servicio | Puerto | Responsabilidad | ¿Depende del enlace? |
|---|---|---|---|
| **ingestor** | 8000 | Valida contra contrato, enriquece, deduplica y publica en Kafka | No |
| **correlator** | 8080 | Mantiene ventanas en Redis, evalúa reglas y emite alertas | No |
| **persister** | 8002 | Archiva eventos y alertas en PostgreSQL por lotes | Sí |
| **indexer** | 8003 | Indexa en Elasticsearch por lotes, un índice por mes | Sí |
| **jobs** | 8004 | Agregados diarios y aplicación de la política de retención | Sí |
:::nota Los tres últimos escriben fuera de la LAN, pero ninguno participa en el camino crítico de detección.
:::

Cada servicio es un **módulo Go independiente**, con sus propias dependencias y su propio ciclo de compilación. No comparten una sola línea de código, lo que se justifica en el ADR-003: compartir una biblioteca habría obligado a redesplegar los cinco ante cualquier cambio, que es exactamente la propiedad que un microservicio debe evitar.

## Recorrido de un evento

El recorrido completo, desde que un sensor simulado emite hasta que la alerta está disponible, atraviesa seis etapas.

1. El productor envía el evento por `POST /events` con una clave de API. El ingestor **valida el JSON crudo** contra el esquema del contrato, de modo que un campo desconocido se rechace en lugar de descartarse en silencio.
2. El ingestor completa los campos que el productor omitió —identificador, marca de tiempo, clave de partición— y registra cuáles tuvo que completar, porque un productor que sistemáticamente omite un campo es un defecto que conviene ver.
3. Reserva el identificador del evento en Redis con una operación atómica. Si ya existía, responde `409` sin publicar: el reintento de un productor no duplica el evento.
4. Publica en el topic `events.standardized` con confirmación de todas las réplicas, particionando **por zona geográfica**, y solo entonces responde `202`.
5. El correlator consume, inserta el evento en la ventana de su zona —un conjunto ordenado de Redis cuya puntuación es la marca de tiempo— y evalúa las cuatro reglas de zona. Si el evento trae placa, lo inserta además en una segunda ventana indexada por el seudónimo del vehículo y evalúa la regla de persecución. Si alguna regla se cumple y no hay una alerta equivalente vigente, emite la alerta.
6. El persister y el indexer consumen el mismo flujo con grupos de consumo independientes y escriben por lotes en PostgreSQL y Elasticsearch, tokenizando la placa antes de que el dato salga de la red municipal.

El identificador de traza entra por la cabecera `X-Trace-Id`, viaja en las cabeceras del mensaje de Kafka y aparece en el registro de los cinco servicios, de modo que **un solo identificador reconstruye el recorrido completo** de cualquier evento.

## El contrato como frontera

El evento canónico versión 1.0 es un esquema JSON que define diez campos obligatorios y cierra el objeto a campos desconocidos. Vive una sola vez en el repositorio y cada servicio embebe su copia en el binario en tiempo de compilación.

No es una biblioteca compartida, y esa distinción es deliberada. Una biblioteca acopla el despliegue; un esquema acopla solo el formato. Cada servicio define sus propios tipos internos con los campos que necesita: el ingestor necesita el evento completo para validarlo, mientras que el correlator solo lee los campos que sus reglas consultan. El riesgo de que las copias diverjan se controla con una comprobación automática que forma parte de la verificación obligatoria del proyecto.

## Diseño de infraestructura

:::tabla Distribución por nodos y segmentos de red
| Nodo | Segmento | Componentes | Sistema operativo |
|---|---|---|---|
| A | LAN municipal | Kafka (KRaft), Redis | Ubuntu Server 22.04 |
| B1 · B2 · B3 | LAN municipal | ingestor · correlator · persister, indexer, jobs | Ubuntu Server 22.04 |
| **E** | **LAN municipal** | **Prometheus, Grafana** | Ubuntu Server 22.04 |
| C | Nube (VPC) | PostgreSQL gestionado, Elasticsearch | Gestionado / Linux |
| F | Nube (VPC) | Kibana | Gestionado / Linux |
| D | Estaciones de trabajo | Generador de carga y productores simulados | Windows / Linux |
:::nota Todo el tráfico que cruza la frontera es de salida, cifrado y originado en los nodos B3 y E.
:::

La ubicación del nodo E merece explicación, porque es contraintuitiva: la observabilidad suele situarse junto a la analítica, en la nube. Aquí vive en la LAN por dos razones.

La primera es de **seguridad**. Recolectar métricas exige que el recolector **abra conexiones hacia cada servicio**. Con Prometheus en la nube, esas serían conexiones de nube hacia la red municipal, es decir **tráfico de entrada**, que es precisamente lo que este modelo excluye. Todo lo que cruza la frontera debe ser de salida.

La segunda es de **utilidad**. Si el enlace se cae, la detección sigue funcionando, y es justo entonces cuando hacen falta las métricas y las alertas. Un recolector al otro lado del enlace se queda ciego en el único momento en que importa.

Grafana lo acompaña porque consulta a Prometheus, y separarlos reintroduciría el mismo tráfico de entrada. Sus paneles de PostgreSQL y Elasticsearch sí cruzan la frontera, pero como consultas puntuales de salida: durante un corte esos paneles quedan en blanco y los de tiempo real siguen vivos, que es la degradación correcta.

La segmentación es la que hace verificable la afirmación de disponibilidad: si se filtra todo el tráfico de salida del segmento A–B, el sistema debe seguir generando alertas. Esa es la prueba que se ejecutó y cuyos resultados se reportan más adelante.

<!-- salto -->

# Justificación técnica: las decisiones de arquitectura

## Criterio

El enunciado exige un mínimo de seis ADR. Se produjeron diez, pero el número importa menos que el criterio con el que se seleccionaron: **se documenta toda decisión que sea costosa de revertir o que alguien pueda cuestionar razonablemente en la defensa.** Una decisión fácil de cambiar no necesita ADR; una que condiciona a todas las demás sí.

Cada ADR sigue el formato obligatorio de la sección 4.1 del enunciado y añade un apartado de impacto en el negocio redactado sin términos técnicos. El conjunto completo se adjunta como Anexo A.

## Las diez decisiones

:::tabla Registro de decisiones de arquitectura
| ADR | Decisión | Alternativa principal descartada |
|---|---|---|
| 001 | Plataforma híbrida: LAN municipal más nube | Cloud-native íntegro |
| 002 | Go como lenguaje y tiempo de ejecución | Java con Spring Boot |
| 003 | Monorepo con un módulo independiente por servicio | Módulo único compartido |
| 004 | Kafka en modo KRaft, sin Zookeeper | RabbitMQ |
| 005 | PostgreSQL relacional más Elasticsearch | Base de series de tiempo |
| 006 | Cola de mensajes fallidos y confirmación manual de offsets | Reintento indefinido |
| 007 | Seudonimización HMAC en la frontera LAN–nube | Cifrado reversible en la nube |
| 008 | SLO cuantitativo con presupuesto de error | Tableros sin umbral |
| 009 | Planificador propio en lugar de Apache Airflow | Adoptar Airflow |
| 010 | Docker Compose, sin Kubernetes | Kubernetes gestionado |
:::nota Las ocho decisiones que el enunciado enumera como obligatorias en su sección 4.2 están cubiertas por los ADR 001, 004, 005, 006, 007, 008, 009 y 010.
:::

## La decisión que ordena a todas las demás

El ADR-001 declara el cliente antes de decidir la plataforma, y ese orden no es retórico. La pregunta *"¿LAN, híbrido o nube?"* no tiene respuesta sin saber quién paga y qué no puede permitirse que falle.

Declarado el cliente —una capital con equipo reducido, con datos personales bajo custodia y sin enlace redundante garantizado— el razonamiento se vuelve casi mecánico. Un modelo íntegramente en la nube pone la detección de incidentes al otro lado de un enlace que puede caerse. Un modelo íntegramente local obliga a la Municipalidad a operar Elasticsearch y a dimensionar hardware para un crecimiento de archivo de decenas de gigabytes mensuales. El modelo híbrido permite que **lo urgente sea local y lo voluminoso sea remoto**.

La consecuencia más interesante de esa decisión es que definió por sí sola dónde debía ir la frontera de privacidad, según se explica más abajo.

## Transporte y resiliencia

Kafka se eligió sobre RabbitMQ por una razón que no es el rendimiento: para el volumen estimado, ambos bastan. La razón es **la reproducción del histórico**. Como la persistencia se sacó del camino crítico, el persister y el indexer consumen de forma asíncrona, y que puedan recuperar su retraso tras una caída es una propiedad central del diseño. RabbitMQ descarta el mensaje al entregarlo; recuperar ese retraso exigiría construir a mano un mecanismo de reintento.

Sobre ese transporte, el ADR-006 define qué hacer cuando algo falla, y la decisión clave es **clasificar los fallos en dos tipos**:

- Un fallo **permanente** —un mensaje que no cumple el contrato, un campo imposible de interpretar— no mejora con reintentos. El mensaje se envía a la cola de fallidos y se confirma el offset, para que un solo mensaje corrupto no bloquee el flujo indefinidamente.
- Un fallo **transitorio** —la base no responde, la red falla— se reintenta **sin confirmar el offset**, de modo que al recuperarse el consumidor retome exactamente donde estaba.

Confundir ambos casos produce los dos peores comportamientos posibles: perder eventos válidos o detener el sistema por uno inválido.

## Privacidad

El ADR-007 parte de una constatación incómoda: una placa vehicular con marca de tiempo y coordenadas **es un dato personal**, y permite reconstruir los desplazamientos de una persona identificable. Que en este proyecto sean sintéticos no cambia el ejercicio de defensa.

La decisión fue seudonimizar la placa con HMAC-SHA256 **exactamente en la frontera hacia la nube**. El mensaje de Kafka conserva el valor en claro porque el broker vive dentro de la red municipal; los dos servicios que cruzan la frontera —persister e indexer— tokenizan antes de escribir.

Que el punto de corte del modelo híbrido coincidiera con el punto donde debe aplicarse la protección no fue planificado: fue una consecuencia afortunada de haber sacado la persistencia del camino crítico. Una vez advertida, se convirtió en un argumento adicional a favor de esa arquitectura.

Dos detalles de implementación resultaron decisivos. El primero es que ambos servicios deben usar **la misma llave**: si difirieran, el mismo vehículo tendría seudónimos distintos en cada destino y las búsquedas cruzadas fallarían en silencio. El segundo es que la tokenización **normaliza el formato antes de calcular**, de modo que `abc-123` y `ABC123` produzcan el mismo seudónimo; sin esa normalización, el vehículo dejaría de ser correlacionable.

La llave nunca se versiona y se custodia dentro de las instalaciones municipales. Esa es la propiedad que hace que la operación sea irreversible desde la nube: quien obtenga la base de datos completa no puede recuperar una sola placa.

## Lo que se decidió no hacer

Tres decisiones son negativas, y conviene señalarlas porque son las que más probablemente se cuestionen.

**No se adoptó Kubernetes** (ADR-010). Resuelve bien el escalado automático, la reprogramación entre muchos nodos y el despliegue sin interrupción. Este sistema tiene uno y medio de esos tres problemas: una sola instancia de cada servicio absorbió el doble del volumen esperado, y el despliegue en dos nodos no da margen real de reprogramación. Lo que sí exigiría es una superficie operativa permanente sobre un equipo reducido.

**No se adoptó Apache Airflow** (ADR-009). La pregunta previa a esa decisión fue cuánto trabajo periódico necesita realmente el sistema, y la respuesta redujo el caso a la mitad. Los chequeos de salud los cubre mejor la observabilidad; la limpieza de claves la resuelven los propios tiempos de vida; y la tarea que más naturalmente pediría un orquestador —copiar periódicamente los eventos hacia Elasticsearch— desapareció al sustituirla por un consumidor de Kafka, que además elimina hasta una hora de retraso en la búsqueda. Quedan dos tareas nocturnas, y dos tareas nocturnas no justifican el costo operativo de un orquestador.

**No se adoptó una base de series de tiempo** (ADR-005). El enunciado la plantea explícitamente como alternativa, y la conclusión honesta es que su ventaja principal —consultas agregadas eficientes sobre ventanas temporales— **ya la aporta Elasticsearch** en esta arquitectura. Añadirla supondría un tercer motor de almacenamiento que operar sin resolver un problema que estuviera abierto.

<!-- salto -->

# Simulación de sensores urbanos

## Los cinco tipos

El generador emula los cinco tipos que exige el enunciado, todos sobre el mismo contrato canónico.

:::tabla Tipos de sensor simulados y su carga útil
| Tipo de evento | Sensor urbano | Campos característicos |
|---|---|---|
| `panic.button` | Botón de pánico ciudadano | tipo de alerta, identificador de dispositivo, contexto de usuario |
| `sensor.lpr` | Cámara de lectura de placas | placa vehicular, velocidad estimada, modelo y color |
| `sensor.speed` | Sensor de velocidad o movimiento anómalo | velocidad detectada, orientación, identificador de sensor |
| `sensor.acoustic` | Sensor acústico | tipo de sonido, nivel de decibelios, probabilidad de evento crítico |
| `citizen.report` | Reporte ciudadano vía aplicación | tipo de evento, mensaje descriptivo, origen |
:::nota Todos comparten los campos comunes del evento canónico: identificador, versión, productor, origen, marca de tiempo, clave de partición, zona, coordenadas y severidad.
:::

## El generador de carga

Un generador que envía a ritmo constante permite comprobar que el sistema funciona, pero **no ejerce el objetivo de la sección 2.3 del enunciado**, que pide evaluar la reacción ante picos de carga.

Se construyó por tanto un generador propio en Go, sin dependencias externas, con cinco escenarios.

:::tabla Escenarios del generador de carga
| Escenario | Qué ejercita |
|---|---|
| `sostenido` | Ritmo constante configurable, para medir el comportamiento en régimen |
| `rafaga` | Calentamiento, pico de 100 ev/s durante 10 s y recuperación |
| `alertas` | Las secuencias que disparan cada uno de los cuatro tipos de alerta |
| `bulk` | El contrato del endpoint de lotes |
| `enunciado` | Los dos renglones de la tabla 2.4 del enunciado, el positivo y el falso positivo |
:::nota El generador usa solo la biblioteca estándar de Go: no requiere instalar Python, Artillery ni JMeter.
:::

El escenario `alertas` es el que sostiene la validación de correlaciones esperadas durante la jornada de simulación: no espera a que el azar produzca una coincidencia, la construye. Deja una alerta viva de cada tipo y muestra la consulta para comprobarla.

El escenario `bulk` merece una nota porque no mide volumen sino **comportamiento contractual**. La promesa del endpoint de lotes es que un evento inválido no invalide el lote entero y que la respuesta indique en qué índice falló y por qué. Una promesa así se sostiene con pruebas unitarias solo en apariencia: hay que verificarla contra el sistema levantado. El escenario envía cuatro casos: un lote limpio, uno con eventos inválidos entremezclados, uno con un duplicado dentro del propio lote, y un cuerpo mal formado que debe responder `400` y no `202`.

:::tabla Verificación del endpoint de lotes
| Caso | Resultado esperado | Resultado obtenido |
|---|---|---|
| Lote limpio de 20 eventos | 20 aceptados | 20 aceptados |
| Lote mixto de 6 eventos | 3 aceptados y 3 rechazados por contrato, cada uno en su índice | coincide |
| Duplicado dentro del lote | 2 aceptados, 1 rechazado por duplicado | coincide |
| Cuerpo que no es un arreglo | `400`, no `202` | `400` |
:::nota La comprobación se validó también en negativo, alterando deliberadamente una expectativa: el generador detectó la discrepancia, indicó el índice y el motivo, y terminó con código de salida distinto de cero.
:::

Esa última nota responde a una exigencia que el propio proyecto aprendió por las malas: una comprobación que no puede fallar no comprueba nada.

<!-- salto -->

# Modelo de correlación e inferencia

## El problema

La correlación consiste en responder, para cada evento que llega, si junto con lo ocurrido recientemente en su misma zona configura un patrón con significado. Eso exige tres cosas: memoria de lo reciente, reglas que expresen los patrones, y un mecanismo que evite repetir la misma alerta una y otra vez mientras el incidente sigue en curso.

## Ventanas deslizantes en Redis

Cada zona tiene un conjunto ordenado en Redis cuya puntuación es la marca de tiempo del evento en milisegundos. Al llegar un evento se inserta, se eliminan los que quedaron fuera de la ventana y se evalúan las reglas sobre lo que queda.

Dos detalles importan. El primero: la puntuación se guarda en **milisegundos y no en nanosegundos**, porque la puntuación de un conjunto ordenado es un número de coma flotante de doble precisión y una marca en nanosegundos excede la precisión entera que ese formato garantiza, produciendo colisiones. El segundo: la ventana se **poda en cada inserción**, de modo que su tamaño queda acotado por el tiempo y no por el volumen acumulado.

## Las cinco reglas

:::tabla Reglas de correlación implementadas
| Alerta | Condición | Confianza |
|---|---|---|
| `possible_robbery` | Botón de pánico más lectura de placa o sensor de velocidad por encima de 80 km/h en la misma zona y ventana | 0.85 |
| `accident` | Reporte ciudadano de accidente más evidencia acústica, sin caída de velocidad | 0.85 |
| `accident` | Lo anterior, además con una caída de velocidad por debajo de 40 km/h | 0.95 |
| `fire` | Sensor acústico de explosión o vidrio roto más reporte ciudadano de incendio | 0.95 |
| `traffic_speed_violation` | Velocidad sostenida por encima del umbral en la zona | 0.90 |
| `vehicle_pursuit` | Dos infracciones del mismo vehículo, en una o varias zonas, con un botón de pánico en alguna de ellas | 0.92 |
:::nota La confianza es un número real entre 0 y 1, no un entero. La columna correspondiente en la base de datos lo garantiza con una restricción de rango. Los valores se verificaron contra el motor de reglas en ejecución.
:::

El caso `possible_robbery` reproduce exactamente el ejemplo de correlación esperada del enunciado: zona 4, botón de pánico, placa XYZ123 y 85 km/h dentro de la misma ventana producen una alerta de robo en curso.

La regla `vehicle_pursuit` rompe la premisa de las otras cuatro: agrupa por vehículo y no por zona. Tiene que hacerlo, porque la secuencia que modela —un robo, el mismo coche cruzando un semáforo en rojo, y después circulando a velocidad excesiva ya en otra zona— es invisible para cualquier ventana de zona. Un coche que huye cambia de zona precisamente para dejar de aparecer en ella.

## Falsos positivos

El segundo ejemplo del enunciado —zona 10, un evento aislado sin señales complementarias— debe producir **ausencia de alerta**, y el sistema hace algo más que no emitirla: la registra.

Cuando una coincidencia parcial expira sin completarse, el correlator incrementa un contador etiquetado por regla y zona. Esa métrica permite distinguir dos situaciones muy distintas que sin ella se confunden: una zona donde no pasa nada, y una zona donde constantemente hay señales sueltas que nunca llegan a configurar un patrón. La segunda sugiere que la regla es demasiado estricta o que la ventana es demasiado corta.

Una regla de alerta vigila esa proporción: si más de la mitad de las coincidencias parciales expira sin completarse, el sistema avisa de que el modelo de correlación necesita revisión.

## Supresión

Un incidente en curso genera eventos continuamente. Sin control, un robo produciría decenas de alertas idénticas en un minuto, y el operador dejaría de mirarlas.

El correlator reserva en Redis una marca por tipo de alerta y zona con un tiempo de vida configurable. Mientras esa marca exista, una alerta equivalente no se emite: se cuenta como suprimida. El equilibrio es delicado, porque la supresión que evita ruido puede, en exceso, esconder incidentes distintos en la misma zona. Por eso una segunda regla vigila la proporción de alertas suprimidas y avisa si supera el 90 %.

## El error que esta regla existe para evitar

La regla de accidente concentra el detalle más delicado del motor, y conviene explicarlo porque el error es fácil de cometer y no produce ningún síntoma.

La forma intuitiva de escribirla es filtrar primero las lecturas de placa a las de velocidad superior a 80 km/h, y buscar después **sobre esa misma lista ya filtrada** una caída por debajo de 40 km/h. Ninguna lectura puede cumplir ambas condiciones a la vez, de modo que la regla quedaría escrita, aparentemente probada y **muerta en la práctica**: no fallaría, simplemente no se dispararía nunca.

El motor conserva por tanto todas las lecturas de la ventana y busca la transición entre lecturas consecutivas. Y va un paso más allá: la caída es **opcional pero reforzante**. Sin ella la alerta se emite con confianza 0.85; con ella asciende a 0.95. Un accidente sigue siendo detectable aunque ningún sensor de velocidad lo haya registrado.

Dos pruebas unitarias existen únicamente para impedir que ese filtro se reintroduzca.

<!-- salto -->

# Evaluación de resultados

## Metodología de medición

Todas las cifras de esta sección se obtuvieron sobre el sistema en ejecución, no sobre estimaciones. Conviene explicitar un problema metodológico que apareció durante la evaluación, porque condicionó cómo se midió.

Las primeras mediciones estuvieron **contaminadas por artefactos de recuperación**. Tras reiniciar un servicio, los histogramas acumulan el retraso de los eventos que el servicio procesa para ponerse al día, y ese retraso —que es correcto que exista— no describe el comportamiento en régimen. Una primera medición del cumplimiento del SLO arrojó un 60 % que hubo que retractar: no reflejaba la latencia del sistema, sino la de un servicio recuperándose.

La medición definitiva se hizo por **diferencia de histograma sobre una ventana sin reinicios**, que es lo que describe el comportamiento real en operación. Las cifras que siguen corresponden a ese método.

## Integridad del flujo

:::tabla Integridad extremo a extremo
| Etapa | Eventos | Alertas |
|---|---|---|
| Publicados en Kafka | 2 736 | 70 |
| Archivados en PostgreSQL | 2 736 | 70 |
| Indexados en Elasticsearch | 2 736 | 70 |
:::nota Correspondencia exacta en los tres destinos. Ningún evento aceptado con código 202 se perdió en el trayecto.
:::

Esta es la verificación del segundo objetivo de nivel de servicio: lo que el sistema acepta, termina archivado.

## Latencia y cumplimiento del SLO

El objetivo declarado es que **el 99 % de las alertas se genere en menos de dos segundos** desde la marca de tiempo del evento que las dispara.

:::tabla Latencia de detección medida
| Métrica | Valor |
|---|---|
| Alertas generadas bajo el umbral de 2 s | 100 % |
| Latencia media de detección | 44,1 ms |
| Presupuesto de error consumido | 0 % |
:::nota Medición por diferencia de histograma sobre una ventana sin reinicios de servicio.
:::

El margen es amplio —dos órdenes de magnitud— y ese margen tiene una explicación arquitectónica: el camino crítico es íntegramente local. No hay ninguna llamada a la nube entre el evento y la alerta.

## Comportamiento ante picos de carga

:::tabla Resultados del escenario de ráfaga
| Métrica | Resultado |
|---|---|
| Ritmo del pico | 100 eventos/s durante 10 s |
| Peticiones enviadas | 1 192 |
| Respuestas distintas de 202 | ninguna |
| Latencia p99 de ingesta | 19,7 ms |
| Instancias por servicio | una |
:::nota El pico corresponde al doble del volumen máximo estimado en operación.
:::

Este resultado es el que sostiene la decisión del ADR-010 de no adoptar Kubernetes: el escalado horizontal automático resuelve un problema que este sistema, a esta escala, no tiene.

## Resiliencia ante la caída de un componente

Se detuvo deliberadamente el servicio persister mientras el sistema recibía eventos, y se volvió a levantar.

Durante la caída, **la ingesta y la detección continuaron con normalidad**: el ingestor siguió aceptando eventos y el correlator siguió emitiendo alertas, porque ninguno de los dos depende del persister. Al reactivarse, el servicio recuperó exactamente los once eventos pendientes leyendo desde su último offset confirmado, y el retraso volvió a cero sin intervención.

Es la prueba práctica de la propiedad que justifica todo el modelo híbrido: **un fallo en la capa de archivo no detiene la detección de incidentes**.

## Retención y agregados

La política de retención degrada el dato conforme envejece, en lugar de conservarlo intacto para siempre o eliminarlo de golpe. Se verificó con eventos sintéticos de distinta antigüedad.

:::tabla Verificación de la degradación progresiva del archivo
| Antigüedad del evento | Placa | Coordenadas | Zona | Registro |
|---|---|---|---|---|
| 100 días | eliminada | conservadas | conservada | conservado |
| 400 días | eliminada | eliminadas | conservada | conservado |
| 800 días | — | — | — | **borrado** |
| Del día (3 334 eventos) | intactas | intactas | intacta | intacto |
:::nota Los plazos son configurables. Los valores actuales son una propuesta técnica que la Municipalidad debe aprobar formalmente, porque determinan cuánto tiempo se conservan datos personales de ciudadanos.
:::

Los agregados diarios, que son lo que sobrevive al borrado, cuadraron exactamente con los datos crudos —3 334 eventos y 90 alertas— y resultaron idempotentes: repetir la ejecución no duplica filas.

El orden de ejecución importa y está fijado: **los agregados corren antes que la retención**. Si la purga borrara los eventos primero, el resumen del día saldría incompleto, y esa pérdida sería irreversible.

## Huella de recursos

:::tabla Huella de los servicios en Go
| Métrica | Valor |
|---|---|
| Tamaño de imagen de contenedor | 22 – 29 MB |
| Memoria residente por servicio | 15 – 19 MB |
| Tiempo de arranque | 7 – 50 ms |
:::nota A modo de contraste, una imagen base de Java habitual para este tipo de servicios ocupa 257 MB antes siquiera de añadir la aplicación.
:::

## Nueve decisiones que no son evidentes

Las siguientes decisiones comparten un rasgo: **la alternativa intuitiva no produce ningún error visible**. Funciona, pasa las pruebas superficiales y falla en silencio. Se enumeran porque son las que un evaluador probablemente pregunte, y porque constituyen el aporte analítico más concreto de este trabajo.

:::tabla Decisiones de diseño y el fallo silencioso que cada una evita
| Decisión | Qué pasaría con la alternativa intuitiva |
|---|---|
| Evaluar la caída de velocidad sobre la lista completa | Filtrar antes vuelve la regla inalcanzable: nunca se dispara, y no falla |
| Confianza como número real con rango [0, 1] | Declararla entera guarda 0,85, 0,90 y 0,95 las tres como 1 |
| Persistencia extraída a consumidores asíncronos | Escribir en la nube dentro del camino crítico hace que un corte de enlace detenga la ingesta |
| Reserva atómica en Redis para deduplicar | Comprobar y después escribir deja una carrera que duplica eventos al escalar |
| Poda de la ventana en cada inserción | Confiar en un TTL que se refresca deja ventanas que no expiran nunca |
| Supresión de alertas por tipo y zona | Un solo incidente en curso genera decenas de alertas idénticas |
| Partición por zona geográfica | Particionar por tipo desbalancea el reparto y pierde el orden dentro de la zona |
| Sensor de velocidad conectado a la regla de robo | La señal entra al sistema, se almacena y no produce ninguna inferencia |
| Confirmar el offset después de procesar | Con confirmación automática, un fallo avanza el offset y el evento se pierde sin rastro |
:::nota Cada una está documentada en el ADR correspondiente y defendida con al menos una prueba automática.
:::

A esas nueve se añade una décima de otra naturaleza: la búsqueda histórica se alimenta de un consumidor de Kafka y no de una copia periódica desde la base. La diferencia es medible —**1,01 segundos frente a hasta 3 600**— y es lo que hace que una búsqueda encuentre un incidente ocurrido hace un minuto.

## Defectos detectados durante la propia evaluación

Tres defectos aparecieron al verificar, no al construir, y los tres comparten un rasgo que conviene subrayar: **ninguno producía un error visible**. La prueba de carga reveló que, si el cliente abortaba la conexión, el camino de limpieza heredaba el contexto cancelado de la petición: la publicación fallaba, la liberación de la reserva fallaba por la misma razón, y el identificador del evento quedaba reservado durante veinticuatro horas. Un reintento posterior recibía `409` por un evento que nunca se había publicado. Se corrigió desacoplando el contexto de limpieza del contexto de la petición, y se verificó comparando el comportamiento antes y después sobre eventos concretos.

El segundo apareció al verificar los tableros. **La regla de alerta del objetivo de nivel de servicio no podía dispararse nunca.** El correlator publica el bucket de su histograma como `le="2"`, tal como lo declara el código, pero Prometheus 3 normaliza esa etiqueta a su representación canónica de coma flotante y la almacena como `le="2.0"`. El selector literal no encontraba serie alguna, de modo que el SLO estaba declarado, documentado y sin vigilancia real. La validación de sintaxis daba `SUCCESS`, porque la expresión es correcta; lo que no existía era la serie. Se corrigió con una expresión regular que acepta ambas formas y funciona igual en Prometheus 2 y 3, y se fijó la versión de la imagen para que un cambio silencioso no vuelva a desactivar una regla.

El tercero es de la misma familia. El archivo de despliegue no pasaba las credenciales de PostgreSQL al contenedor de Grafana, de modo que el origen de datos se provisionaba con usuario y base vacíos. El tablero de incidentes habría llegado vacío a la demostración sin un solo mensaje de error.

La lección metodológica que dejan los tres es la misma: **un componente que no encuentra datos no se distingue de un componente que no tiene nada que reportar.** Ambos callan. Verificar la sintaxis no basta; hay que comprobar que cada consulta devuelve algo contra un sistema con datos reales.

<!-- salto -->

# Visualización

## Tableros

La visualización se provisiona automáticamente: los orígenes de datos y los tableros se versionan en el repositorio y Grafana los carga al arrancar. Dejarlos como plantillas de ejemplo fuera del control de versiones —que es lo habitual— obliga a que cada integrante los configure a mano, y el resultado previsible es que los tableros lleguen vacíos a la demostración.

:::tabla Tableros provisionados
| Tablero | Origen de datos | Contenido |
|---|---|---|
| Operación en tiempo real | Prometheus | Ritmo de ingesta por tipo de sensor, alertas por tipo, rechazos por motivo, ventanas de correlación, retraso de archivado, estado de los cinco servicios |
| Incidentes y mapa de calor | PostgreSQL | Mapa de calor georreferenciado, lista de incidentes correlacionados, alertas críticas, reparto por tipo de sensor |
| SLO y presupuesto de error | Prometheus | Cumplimiento a 30 días, presupuesto de error consumido, percentiles de latencia con el umbral marcado, estado de las tareas nocturnas |
:::nota Los tres cubren los tres casos de visualización que pide la sección 7 del enunciado: mapas de calor, listas de incidentes y alertas críticas.
:::

Los treinta paneles se verificaron enviando a sus respectivos motores el texto exacto de cada consulta generada, no revisándolos a ojo.

:::tabla Verificación de los tableros
| Comprobación | Resultado |
|---|---|
| Expresiones PromQL de paneles y reglas de alerta | 45 comprobadas, 0 rechazadas |
| Consultas SQL de los paneles | 11 comprobadas, 0 con error |
| Orígenes de datos provisionados | Prometheus, PostgreSQL y Elasticsearch, los tres conectando |
| Reglas de alerta evaluando sin error | 14 de 14 |
| Tipos de alerta observados en los tableros | los cuatro |
:::nota Esa comprobación es la que detectó los dos defectos silenciosos reportados en la sección de evaluación.
:::

El segundo tablero es el que responde directamente al caso de uso de visualización: el mapa de calor sitúa la densidad de eventos sobre el área metropolitana y la lista de incidentes muestra cada alerta con su ventana temporal, su confianza y el número de eventos que la sustentan.

## Alertas que no dependen de que alguien mire

El enunciado es explícito al pedir que se defina qué ocurre cuando se incumple un umbral, **más allá de tener tableros en Grafana**. Esa frase determinó el contenido del ADR-008: un tablero solo avisa a quien lo está mirando, y la hipótesis de trabajo de este sistema es que nadie está mirando.

Se definieron por tanto catorce reglas de alerta, cada una con **el primer paso de diagnóstico escrito en su propia anotación**, de modo que quien la reciba a las tres de la madrugada no tenga que deducir por dónde empezar.

Dos de ellas merecen mención porque no vigilan la infraestructura sino el dominio: una avisa si se suprimen demasiadas alertas, lo que indicaría que la supresión está ocultando incidentes distintos; otra avisa si la tasa de falsos positivos es alta, lo que indicaría que el modelo de correlación necesita ajuste.

Una tercera funciona **por ausencia**: si una tarea nocturna deja de ejecutarse, no emite ningún error, simplemente no emite nada. Lo que se vigila es que el marcador de su último éxito siga avanzando. Es el único modo de detectar un fallo silencioso.

## Presupuesto de error

El cumplimiento del SLO al 99 % sobre treinta días deja un 1 % de presupuesto, y su consumo gobierna decisiones concretas: por debajo del 50 % se despliega con normalidad; entre el 50 % y el 100 % se congelan los cambios no esenciales; por encima del 100 % se escala a la coordinación del proyecto.

El valor de esa política es que convierte una pregunta habitualmente subjetiva —*¿desplegamos el viernes?*— en una pregunta con respuesta objetiva.

<!-- salto -->

# Limitaciones y trabajo futuro

Enumerar las limitaciones es parte del ejercicio de arquitectura. Las siguientes son reales y están documentadas en los ADR correspondientes.

**Punto único de fallo en el transporte.** El despliegue de laboratorio usa un solo broker con factor de replicación 1. Su caída detiene el sistema. En producción debería elevarse a tres réplicas con un mínimo de dos sincronizadas, lo que está previsto sobre los servidores contemplados y no supone costo de licencias.

**Sin reprogramación automática entre nodos.** Es la consecuencia aceptada de no adoptar Kubernetes. Si el servidor que hospeda la capa local falla por avería física, alguien debe intervenir. La mitigación disponible —levantar el sistema en un servidor de repuesto en minutos, porque cada componente es autocontenido— debe quedar escrita como procedimiento y ensayarse al menos una vez.

**La tabla de eventos no está particionada por tiempo.** A medida que el archivo crezca, las consultas y el borrado se degradarán. Particionar por mes es trabajo pendiente y de alcance acotado.

**Elasticsearch opera sin réplicas** en el laboratorio, lo que es aceptable para una demostración pero no para producción.

**Seudonimizar no es anonimizar.** El ADR-007 lo declara explícitamente: las coordenadas y las marcas de tiempo cruzan a la nube sin transformar, y un conjunto suficientemente grande de posiciones seudonimizadas puede permitir la reidentificación por patrón de desplazamiento. Reducir la precisión geográfica de los datos archivados es la línea de trabajo natural.

**Los plazos de retención carecen de aprobación institucional.** Los valores implementados son una propuesta técnica. Determinan cuánto tiempo una institución pública conserva datos personales de ciudadanos, y esa no es una decisión de ingeniería.

**El aviso a terceros se registra, pero no se entrega.** El servicio `notifier` decide a qué organismos corresponde cada alerta —bomberos y ambulancia en un incendio, policía en un robo— y deja constancia de cada aviso con su canal, su hora y su resultado. El canal por defecto es `simulado`: anota la fila y escribe en el registro. **Nadie acude.** La trazabilidad que el requisito pedía está completa; la entrega real no existe y no se disimula en ningún tablero.

**No hay reintento ni acuse de recibo del aviso.** Un aviso fallido queda en estado `fallido` y ahí permanece. La política de reintento —cuántas veces, con qué espera— depende de cada cuerpo de respuesta y no tiene sentido inventarla antes de que exista el convenio.

**El canal de notificación al equipo de operación sigue sin configurar.** Es un hueco distinto del anterior y conviene no confundirlos: las reglas de Prometheus disparan y mueren dentro de Prometheus, porque no hay Alertmanager. Si el propio sistema falla de madrugada, nadie se entera.

**El generador de carga no reintenta.** En operación real, un productor debería reintentar ante un rechazo transitorio; el simulador no lo hace, lo que hace visible el hueco de segundos durante un redespliegue.

<!-- salto -->

# Conclusiones

**La arquitectura orientada a eventos permitió separar lo urgente de lo voluminoso.** Situar la ingesta, el transporte y la correlación dentro de la red municipal, y dejar fuera únicamente el archivo y la analítica, produjo un sistema cuya función esencial —detectar incidentes— no depende de un enlace a internet. Esa propiedad se verificó deteniendo deliberadamente un componente de la capa remota: la detección continuó y el archivo se puso al día solo.

**Las decisiones de arquitectura se sostienen sobre el cliente declarado, no sobre las buenas prácticas en abstracto.** Descartar Kubernetes, descartar Airflow y descartar una base de series de tiempo son decisiones defendibles para una municipalidad con equipo reducido y volumen acotado, y serían probablemente incorrectas para un operador nacional. Declarar el cliente antes de decidir fue lo que hizo posible argumentar en lugar de opinar.

**Los fallos que importan no dan error.** Nueve de las decisiones de este sistema existen para evitar comportamientos que funcionan en apariencia y fallan en silencio: una regla de correlación cuyas condiciones no pueden cumplirse a la vez, una confianza que se redondea al persistir, un evento que desaparece porque el offset avanzó igual. Ninguno se descubre probando que el sistema responde; se descubren razonando qué podría estar mal sin que nadie lo note.

**Un objetivo de nivel de servicio solo sirve si define qué ocurre al incumplirse.** El SLO de dos segundos se cumple con dos órdenes de magnitud de margen, pero lo que lo convierte en una herramienta de gobierno no es el número sino el presupuesto de error asociado, que transforma decisiones de despliegue habitualmente subjetivas en decisiones con respuesta objetiva.

**Medir es más difícil que construir.** Tres mediciones sucesivas de este proyecto resultaron contaminadas por artefactos de recuperación tras reinicios, y hubo que retractarlas. A eso se suma el caso de la regla de alerta que no encontraba serie: una comprobación que no puede fallar no comprueba nada. La lección metodológica —que una cifra sin las condiciones en que se obtuvo no es un resultado— tiene tanto valor formativo como el sistema construido.

**La privacidad se diseña, no se añade.** Que la frontera de seudonimización coincidiera con el punto de corte del modelo híbrido no fue planificado, pero una vez advertido se convirtió en un argumento adicional a favor de esa arquitectura. La llave de protección se custodia dentro de las instalaciones municipales, de modo que quien obtuviera la base completa no podría recuperar una sola placa.

<!-- salto -->

# Referencias

Beyer, B., Jones, C., Petoff, J., & Murphy, N. R. (Eds.). (2016). *Site reliability engineering: How Google runs production systems*. O'Reilly Media.

Congreso de la República de Guatemala. (2008). *Decreto número 57-2008, Ley de Acceso a la Información Pública*. Diario de Centro América.

Hohpe, G., & Woolf, B. (2003). *Enterprise integration patterns: Designing, building, and deploying messaging solutions*. Addison-Wesley.

Kleppmann, M. (2017). *Designing data-intensive applications: The big ideas behind reliable, scalable, and maintainable systems*. O'Reilly Media.

Kreps, J., Narkhede, N., & Rao, J. (2011). Kafka: A distributed messaging system for log processing. *Proceedings of the NetDB Workshop*.

Newman, S. (2021). *Building microservices: Designing fine-grained systems* (2nd ed.). O'Reilly Media.

Nygard, M. (2011, 15 de noviembre). *Documenting architecture decisions*. Cognitect Blog.

Richardson, C. (2018). *Microservices patterns: With examples in Java*. Manning Publications.

Universidad Mariano Gálvez de Guatemala. (2026). *Proyecto final: Arquitectura de Computadores II – 040* [Documento de curso].

<!-- salto -->

# Anexos

## Anexo A — Registro consolidado de decisiones de arquitectura

Se adjunta como documento independiente el registro completo de las diez decisiones de arquitectura, cada una con su contexto, opciones consideradas, decisión, consecuencias e impacto en el negocio, conforme al formato de la sección 4.1 del enunciado.

## Anexo B — Repositorio y artefactos técnicos

:::tabla Artefactos entregados
| Artefacto | Ubicación en el repositorio |
|---|---|
| Código de los cinco microservicios | `services/` |
| Contratos de evento y de alerta | `contracts/` |
| Configuración de despliegue por capa | `deploy/docker-compose.*.yml` |
| Esquema de base de datos | `deploy/sql/001_schema.sql` |
| Reglas de alerta | `deploy/prometheus/alertas.yml` |
| Tableros de visualización | `deploy/grafana/provisioning/dashboards/` |
| Generador de carga y escenarios | `test/load/` |
| Decisiones de arquitectura | `docs/adr/` |
:::nota Todo el sistema se construyó desde cero para este proyecto. El repositorio no depende de ningún artefacto externo salvo las bibliotecas declaradas en cada `go.mod`.
:::

## Anexo C — Fuentes que el equipo debe verificar y completar

Las afirmaciones siguientes se incluyeron en el informe por ser de conocimiento general en la materia, pero **deben respaldarse con su fuente primaria antes de la entrega**, conforme al estándar APA 7. Esta lista se incluye deliberadamente para que ninguna quede sin cita.

- Datos del Centro de Operações do Rio de Janeiro: año de inauguración, entidad constructora y número de dependencias integradas.
- Características y licencia de la plataforma Sentilo de Barcelona.
- Año de inicio y alcance del programa Smart Nation de Singapur.
- Infraestructura de monitoreo urbano que opera actualmente la Municipalidad de Guatemala.
- Artículos concretos del Decreto 57-2008 aplicables al tratamiento de datos personales por instituciones públicas.
- Datos demográficos y de zonificación del área metropolitana de Guatemala, con su fuente oficial.
- Referencias de precio empleadas en la estimación de costo del ADR-001.
