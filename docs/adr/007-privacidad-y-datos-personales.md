# ADR-007: Privacidad — seudonimización de placas en la frontera LAN–nube

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-001](001-plataforma-de-despliegue.md) (el punto de corte del despliegue es la frontera de privacidad)

---

## 1. Contexto

El sistema procesa datos que identifican indirectamente a personas. El
enunciado lo señala de forma explícita (secciones 4.2 y 9) y añade una
precisión que conviene no pasar por alto: **la obligación aplica aunque los
datos sean sintéticos**, porque forma parte del ejercicio de defensa ante el
área legal.

### Qué datos personales circulan

| Dato | De dónde viene | Por qué es personal |
|---|---|---|
| `placa_vehicular` | `sensor.lpr` | Identifica un vehículo y, por registro, a su propietario |
| `geo.lat` / `geo.lon` | Todos los eventos | Ubica a una persona en un momento concreto |
| `geo.zone` | Todos los eventos | Ubicación de menor precisión |
| `timestamp` | Todos los eventos | Combinado con lo anterior, construye un rastro de movimiento |
| `identificador_dispositivo` | `panic.button` | Puede asociarse a una persona o a un domicilio |

El elemento crítico no es ninguno por separado, sino su **combinación**: placa
más coordenadas más marca de tiempo, repetida a lo largo de días, es un
historial de desplazamientos de una persona identificable.

### El marco legal es delgado, y eso obliga a decidir

El cliente declarado ([ADR-001](001-plataforma-de-despliegue.md)) es la
**Municipalidad de Guatemala**, una entidad pública. Guatemala **no cuenta con
una ley general de protección de datos personales** comparable al RGPD europeo;
han existido iniciativas legislativas, pero no una norma integral vigente.

Lo que sí existe y resulta aplicable:

- La **Ley de Acceso a la Información Pública** (Decreto 57-2008) regula el
  tratamiento de datos personales en poder de sujetos obligados, categoría en
  la que entra una municipalidad, y restringe su difusión sin consentimiento
  del titular.
- La **Constitución Política de la República** reconoce el derecho de toda
  persona a conocer lo que de ella conste en archivos y registros estatales, y
  a que se corrija o actualice.

> **Pendiente de verificación:** el articulado concreto de ambas normas debe
> comprobarse y citarse en formato APA 7 en el informe final. Este ADR
> identifica el marco aplicable, no sustituye la revisión legal.

La delgadez del marco es en sí misma un argumento arquitectónico: **no basta
con "cumplir la ley", porque la ley dice poco.** La Municipalidad tiene que
definir su propia política, y esa política tiene que quedar inscrita en el
diseño del sistema, no en un documento aparte que nadie aplica.

## 2. Opciones consideradas

### Opción A — No hacer nada: son datos simulados

**A favor**

- Cero esfuerzo.
- Técnicamente cierto: ninguna placa del sistema corresponde a un vehículo real.

**En contra**

- El enunciado lo descarta expresamente.
- Y, más de fondo: el sistema está diseñado para operar con datos reales. Un
  diseño que solo protege los datos cuando son falsos no protege nada. Los
  controles de privacidad hay que construirlos cuando se construye el sistema,
  porque añadirlos después exige migrar el histórico ya acumulado.

### Opción B — Cifrar la base de datos en reposo

Activar el cifrado de disco que ofrece el proveedor de base gestionada.

**A favor**

- Trivial: suele ser una casilla en el panel del proveedor.
- Protege ante el robo físico del medio de almacenamiento.

**En contra**

- **No protege contra el escenario que importa.** El cifrado en reposo es
  transparente para cualquiera que se conecte con credenciales válidas: el
  proveedor, un administrador, una credencial filtrada o una consulta mal
  hecha ven los datos en claro.
- No es alternativa sino complemento: conviene tenerlo, pero no resuelve el
  problema.

### Opción C — Anonimización irreversible: descartar el campo

Sencillamente no archivar la placa.

**A favor**

- La protección más fuerte posible: lo que no se guarda no se puede filtrar.
- Elimina la obligación de custodiar una llave.

**En contra**

- **Destruye la utilidad del archivo histórico.** Sin placa no se puede
  investigar después la recurrencia de un vehículo en una zona, que es una de
  las razones por las que una municipalidad despliega cámaras de lectura de
  placas.
- Rompe la correlación por vehículo entre distintos puntos de control.

### Opción D — Cifrado reversible con llave propia

Cifrar la placa con AES y una llave custodiada on-premise, de modo que se
pueda descifrar cuando haya una orden judicial.

**A favor**

- Conserva la utilidad completa: el dato original es recuperable.
- Modelo habitual cuando la legislación exige poder responder a requerimientos.

**En contra**

- **Un mismo texto cifrado no es estable** con modos seguros: cifrar dos veces
  la misma placa da resultados distintos, así que no sirve para correlacionar
  apariciones sin descifrar primero. Forzar la estabilidad —usando un modo
  determinista— reintroduce los problemas que se quieren evitar.
- Mantiene el dato personal recuperable: ante una filtración de la llave, la
  exposición es total.
- Exige un procedimiento formal de descifrado que la Municipalidad no tiene
  definido.

### Opción E — Seudonimización estable con HMAC en la frontera

Sustituir la placa por un seudónimo calculado con HMAC-SHA256 y una llave
secreta, justo antes de que el dato cruce hacia la nube.

**A favor**

- El seudónimo es **estable**: el mismo vehículo produce siempre el mismo
  token, de modo que la correlación histórica sigue siendo posible.
- Es **irreversible sin la llave**, y la llave no acompaña al dato.
- Se aplica en un punto único y verificable del sistema.

**En contra**

- El dato sigue siendo **personal**: un seudónimo estable permite rastrear,
  aunque no identificar directamente. No es anonimización.
- Introduce un secreto crítico que hay que custodiar, rotar y respaldar.
- Perder la llave vuelve el histórico irreversiblemente anónimo.

## 3. Decisión

**Se adopta la opción E**, complementada con B —cifrado en reposo del
proveedor, por ser gratuito— cuando esté disponible.

### 3.1 Dónde se aplica: la frontera es un componente

La decisión más importante de este ADR no es *qué* transformación se aplica,
sino **dónde**.

```
Productor ──en claro──▶ Ingestor ──en claro──▶ Kafka (LAN) ──▶ Correlator
                                                   │
                                                   ▼
                                              Persister ──seudónimo──▶ PostgreSQL (nube)
```

El **persister** es el único componente que escribe fuera de la LAN
([ADR-001](001-plataforma-de-despliegue.md)). Al situarse exactamente sobre la
frontera, se convierte en el lugar natural —y único— donde aplicar la
transformación.

Eso da una propiedad valiosa: **para auditar que ningún dato personal sale del
control municipal en claro, basta revisar un componente.** No hay que verificar
cada ruta de salida del sistema, porque solo hay una.

Dentro de la LAN la placa circula en claro a propósito. El broker no sale de la
red municipal, y la correlación necesita el valor original para agrupar
apariciones del mismo vehículo. Proteger el dato dentro del perímetro que ya
está protegido añade coste sin añadir seguridad.

### 3.2 Por qué HMAC y no un resumen simple

Un `SHA-256` de la placa sería inútil. **El espacio de placas es pequeño y
enumerable**: unos pocos millones de combinaciones posibles. Cualquiera con el
resumen puede generar todas las placas posibles, calcular sus resúmenes y
construir la tabla inversa completa en minutos.

HMAC-SHA256 con una llave secreta convierte ese ataque en inviable mientras la
llave esté protegida: sin ella no se puede calcular el resumen de una placa
candidata, de modo que no hay nada que comparar.

### 3.3 Normalización previa

La placa se normaliza antes de calcular el seudónimo: mayúsculas y solo
caracteres alfanuméricos. Sin eso, `abc-123`, `ABC123` y ` ABC 123 ` producirían
tres seudónimos distintos para el mismo vehículo, según cómo lo escribiera cada
sensor, y **se perdería precisamente la correlación que se quería conservar**.

### 3.4 Longitud del seudónimo

Se conservan 128 bits (32 caracteres hexadecimales) de los 256 que produce el
HMAC, con el prefijo `plate_`. Es suficiente para que las colisiones sean
despreciables y deja un identificador más manejable en registros y tableros.

### 3.5 Custodia de la llave

| Aspecto | Decisión |
|---|---|
| Ubicación | On-premise, en el entorno del persister |
| Versionado | **Nunca** en el repositorio; `.env` está en `.gitignore` |
| Generación | `openssl rand -hex 32` |
| Respaldo | Cifrado, con procedimiento documentado y custodia separada |
| Rotación | Invalida la correlación con el histórico previo (ver consecuencias) |

Si la llave no está configurada, los servicios **arrancan igual pero lo
advierten en el registro**: `"PLATE_HMAC_KEY no configurada: las placas se
archivarán en claro en la nube"`. Se prefirió advertir a impedir el arranque
para no convertir un descuido de configuración en una caída del sistema de
seguridad ciudadana.

## 4. Lo que esta decisión NO protege

Esta sección existe porque un ADR de privacidad que solo enumere sus aciertos
es propaganda.

### 4.1 El rastro de movimiento permanece

La placa se seudonimiza, pero **`geo.lat`, `geo.lon`, `geo.zone` y `ts_utc`
viajan a la nube sin transformar**. Un seudónimo estable más coordenadas más
marca de tiempo sigue siendo un historial de desplazamientos.

En terminología del RGPD —que no rige en Guatemala pero sirve de referencia
conceptual— esto es **dato seudonimizado, que sigue siendo dato personal**. No
es anonimización, y llamarlo así sería incorrecto.

Se verificó empíricamente sobre el sistema en ejecución: de **537 eventos de
lectura de placas, 526 seudónimos distintos**. Los repetidos corresponden al
mismo vehículo visto varias veces, que es justamente la propiedad buscada —y
justamente lo que hace rastreable el dato.

Es un compromiso consciente: sin seudónimo estable no hay correlación por
vehículo, y sin correlación por vehículo la cámara de lectura de placas pierde
su propósito.

### 4.2 No hay política de retención implementada

El enunciado pregunta cómo se **protegen, anonimizan o retienen** los datos.
Los dos primeros están resueltos; **la retención no está implementada**.

Hoy el archivo histórico crece sin límite: entre sesenta y noventa gigabytes al
mes según la estimación del [ADR-001](001-plataforma-de-despliegue.md). No hay
proceso que purgue ni que degrade la precisión de los datos antiguos.

Lo que debería implementarse, y queda propuesto:

| Antigüedad | Tratamiento propuesto |
|---|---|
| 0 – 90 días | Completo, tal como se archiva hoy |
| 90 días – 1 año | Eliminar el seudónimo de placa; conservar zona, sin coordenadas |
| Más de 1 año | Solo agregados por zona y tipo de evento |

Es trabajo pendiente, no una decisión tomada. Reconocerlo ahora es preferible a
que aparezca como omisión en la defensa.

### 4.3 Sin control de acceso granular

Cualquiera con credenciales de la base ve todo el histórico. No hay roles que
distingan entre consultar agregados y consultar eventos individuales. Los
endpoints de consulta del correlator sí exigen autenticación, pero el acceso
directo a la base no está segmentado.

## 5. Consecuencias

### Positivas

- La nube nunca recibe placas identificables.
- Auditable en un solo punto del sistema.
- La correlación por vehículo se conserva íntegra.
- El punto de corte del despliegue y la frontera de privacidad coinciden: una
  sola decisión arquitectónica resuelve dos problemas.
- Ante una filtración de la base, el atacante obtiene seudónimos inútiles sin
  la llave.

### Negativas aceptadas

- **Aparece un secreto crítico.** Hay que custodiarlo, respaldarlo y rotarlo.
  Es una responsabilidad operativa nueva para un equipo reducido.
- **Rotar la llave rompe la continuidad del histórico.** Los seudónimos
  anteriores y los posteriores no coinciden, de modo que un vehículo aparece
  como dos. Una rotación exige re-seudonimizar el archivo o aceptar un corte en
  la serie.
- **Perder la llave anonimiza el histórico de forma irreversible.** Deja de ser
  útil para investigación. Es el reverso exacto de la propiedad de seguridad.
- **Los datos siguen siendo personales.** Todas las obligaciones de tratamiento
  aplican igual; la seudonimización reduce el riesgo, no la responsabilidad.
- **La retención queda pendiente**, con el archivo creciendo sin límite mientras
  tanto.

## 6. Evidencia medida

Verificado el 21 de septiembre de 2026 sobre el sistema en ejecución.

### La frontera funciona

| Dónde | Valor de la placa |
|---|---|
| Kafka, topic `events.standardized` (LAN) | `P123ABC` |
| PostgreSQL, tabla `events` (nube) | `plate_161f238e9d6f70a3986ebb6ffb670b8a` |

Consulta de control sobre el total del archivo:

```
placas en claro en la nube: 0
```

### El seudónimo es estable y, por tanto, rastreable

537 eventos de lectura de placas produjeron 526 seudónimos distintos. La
repetición es intencionada y es lo que permite correlacionar; también es lo que
mantiene el dato dentro de la categoría de dato personal.

### Lo que igualmente cruza

Una fila real del archivo, tal como queda en la nube:

```
zona=zona_18  lat=14.672  lon=-90.471  placa=plate_4f78e4c6…  ts=15:01:59
```

Las coordenadas y la marca de tiempo están sin transformar. Es la limitación
descrita en la sección 4.1.

## 7. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Las placas de los vehículos **nunca salen de las instalaciones de la
Municipalidad en forma legible**. Antes de enviarse al archivo contratado por
internet se convierten en un código, y la clave para relacionarlo con la placa
real se queda en las instalaciones municipales.

Si alguien obtuviera acceso indebido al archivo externo, vería códigos sin
valor. Sin la clave, que nunca sale de la Municipalidad, no hay forma de saber
a qué vehículo corresponde cada uno.

### Qué se conserva

El código es **siempre el mismo para el mismo vehículo**. Eso permite seguir
investigando si un vehículo apareció varias veces en una zona, que es una de
las razones por las que se instalan cámaras de lectura de placas. Se protege el
dato sin perder su utilidad.

### Qué no cubre, y conviene saberlo

**La ubicación y la hora sí se envían sin transformar.** Aunque la placa esté
protegida, el archivo permite reconstruir los desplazamientos de un vehículo
—sin saber cuál— a lo largo del tiempo. Es información sensible y sigue
sujeta a las obligaciones de protección de datos.

**No hay todavía una política de borrado.** El archivo crece de forma
indefinida. Se propone conservar el detalle completo tres meses, reducirlo
después y quedarse solo con estadísticas pasado un año. **Está pendiente de
decisión y de implementación.**

### Qué exige

| Obligación | Frecuencia |
|---|---|
| Custodiar la clave de protección, con respaldo cifrado | Permanente |
| Designar responsable de su resguardo | Una vez |
| Definir y aprobar la política de borrado | Pendiente |
| Revisar quién tiene acceso al archivo | Semestral |

### Riesgo

| Riesgo | Probabilidad | Impacto | Cómo se atiende |
|---|---|---|---|
| Pérdida de la clave | Baja | **Alto**: el archivo queda inservible para investigación | Respaldo cifrado con custodia separada |
| Filtración de la clave | Baja | Alto: la protección deja de tener efecto | Nunca se versiona; rotación ante sospecha |
| Reconstrucción de desplazamientos a partir de ubicación y hora | Media | Medio | **No mitigado.** Requiere reducir la precisión de las coordenadas en el archivo |
| Crecimiento indefinido del archivo | Alta | Medio | Pendiente: política de retención |

---

## Fuentes a citar en el informe

- Ley de Acceso a la Información Pública de Guatemala, Decreto 57-2008 —
  articulado sobre datos personales en poder de sujetos obligados. **Verificar
  los artículos concretos.**
- Constitución Política de la República de Guatemala — derecho de acceso a los
  registros estatales. **Verificar el artículo.**
- Estado de las iniciativas legislativas guatemaltecas sobre protección de
  datos personales, para sustentar la afirmación de que no existe norma general
  vigente.
- RGPD (Unión Europea), artículo 4 — se cita únicamente como **referencia
  conceptual** de la distinción entre seudonimización y anonimización; no rige
  en Guatemala.

## Referencias

- Enunciado del proyecto final, secciones 4.2 y 9.
- [ADR-001](001-plataforma-de-despliegue.md) — el punto de corte del despliegue.
- [ADR-006](006-resiliencia-y-tolerancia-a-fallos.md) — el persister como único
  componente que escribe en la nube.
- `services/persister/internal/privacy/plate.go` — implementación y pruebas.
