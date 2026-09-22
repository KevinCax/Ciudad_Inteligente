# ADR-003: Organización del repositorio — monorepo con módulos independientes

**Estado:** aceptado
**Fecha:** 2026-09-21
**Decide:** equipo de arquitectura
**Relacionado con:** [ADR-002](002-lenguaje-y-runtime.md) (la elección de Go condiciona el mecanismo de módulos)

---

## 1. Contexto

El sistema tiene cinco microservicios —ingestor, correlator, persister, indexer
y jobs— más una herramienta de carga. Hay que decidir cómo se organizan en
control de versiones.

La decisión parece administrativa y no lo es: determina si los servicios pueden
desplegarse de forma independiente, que es la propiedad que distingue una
arquitectura de microservicios de un monolito repartido en carpetas.

### El problema concreto: el contrato compartido

Los cinco servicios hablan el mismo evento canónico v1.0. En cuanto se comparte
una definición entre servicios aparece la tentación de extraerla a una
biblioteca común, y con ella el riesgo de acoplarlos.

Es la pregunta de fondo de este ADR: **¿cómo se comparte un contrato sin
compartir un destino de despliegue?**

## 2. Opciones consideradas

### Opción A — Monorepo con un solo módulo Go

Un único `go.mod` en la raíz, los servicios en `cmd/` y el código compartido en
`internal/`.

**A favor**

- Lo más simple de arrancar: un `go build ./...` compila todo.
- El contrato vive una sola vez, en un paquete que todos importan. No puede
  haber divergencia.
- Refactorizar es trivial: el compilador verifica todo el sistema a la vez.

**En contra**

- **Los servicios dejan de ser independientes.** Comparten dependencias,
  versión de Go y ciclo de compilación. Actualizar una biblioteca para un
  servicio la actualiza para todos.
- Un cambio en el paquete compartido obliga a recompilar y redesplegar los
  cinco, aunque cuatro no lo necesiten.
- Es un **monolito distribuido**: se paga la complejidad operativa de tener
  cinco procesos sin obtener la autonomía que los justifica.

### Opción B — Monorepo con un módulo Go por servicio

Un repositorio, pero cada servicio con su propio `go.mod`, sus dependencias y
su ciclo de compilación.

**A favor**

- Cada servicio se compila, versiona y despliega por separado.
- Las dependencias son por servicio: el correlator no arrastra el cliente de
  Elasticsearch, y el indexer no arrastra el de Redis.
- Un solo `git clone` para el equipo, y los cambios que tocan varios servicios
  caben en un commit atómico.
- La configuración de integración continua es una sola.

**En contra**

- Compartir código entre módulos exige publicarlo o usar directivas `replace`,
  que ensucian los `go.mod`.
- Hay tentación permanente de extraer código común, y ceder reintroduce el
  acoplamiento de la opción A por la puerta de atrás.
- Tocar el contrato implica editar varios sitios.

### Opción C — Un repositorio por servicio

**A favor**

- Autonomía máxima: cada servicio tiene su historial, sus permisos y su
  cadencia de publicación.
- El acoplamiento accidental es imposible, porque no hay nada a mano que
  compartir.

**En contra**

- **Un cambio de contrato exige coordinar cinco repositorios.** En un equipo
  pequeño y con fecha de entrega fija, ese costo se paga en cada iteración.
- El contrato hay que publicarlo como artefacto versionado, con su propio
  ciclo de publicación.
- Integración continua duplicada cinco veces.
- Reproducir el sistema completo para una demostración exige clonar cinco
  repositorios en las versiones correctas.

## 3. Decisión

**Se adopta la opción B: un repositorio, un módulo Go por servicio.**

```
Ciudad_Inteligente_GO/
├── contracts/                   fuente de verdad de los contratos
│   ├── canonical-event/v1.0/
│   └── correlated-alert/v1.0/
├── services/
│   ├── ingestor/    go.mod → ciudadinteligente/ingestor
│   ├── correlator/  go.mod → ciudadinteligente/correlator
│   ├── persister/   go.mod → ciudadinteligente/persister
│   ├── indexer/     go.mod → ciudadinteligente/indexer
│   └── jobs/        go.mod → ciudadinteligente/jobs
├── deploy/
├── docs/adr/
└── test/load/       go.mod propio
```

### 3.1 El contrato es el schema, no una biblioteca

Esta es la decisión que resuelve la tensión del contexto.

**No hay módulo Go compartido.** El contrato es el **JSON Schema**, que vive una
sola vez en `contracts/`. Cada servicio:

- embebe su copia con `go:embed`;
- define sus propios structs, con solo los campos que ese servicio necesita;
- puede migrar a una versión nueva del contrato sin esperar a los demás.

Que los structs sean propios de cada servicio no es duplicación por descuido:
el ingestor necesita el evento completo para validarlo y publicarlo, mientras
que el correlator solo lee los campos que sus reglas consultan. Obligarlos a
compartir una estructura los ataría a las necesidades del otro.

El precio es el riesgo de que las copias diverjan, y se paga con una
comprobación automática:

```
make check-contracts
```

Compara cada copia con la fuente de verdad y falla si difieren. Corre dentro de
`make check`, de modo que nadie puede olvidarla.

### 3.2 Por qué monorepo y no cinco repositorios

Lo que decide es el **costo de cambiar el contrato**.

En polyrepo, subir el evento canónico a la versión 1.1 significa: publicar el
contrato nuevo, abrir cinco pull requests, coordinar su orden de integración y
verificar que ninguna combinación intermedia rompa el sistema. En monorepo es
un commit que toca seis archivos y que la comprobación de contratos valida de
una vez.

Con un equipo de estudiantes y una fecha fija, esa diferencia es lo que decide.

### 3.3 La decisión es reversible

Importa dejarlo dicho porque es lo que permite elegir el monorepo sin
comprometerse para siempre.

Como **no hay código compartido entre servicios**, cada carpeta `services/X/` se
puede extraer a su propio repositorio con `git subtree split` conservando su
historial, y seguirá compilando sin tocar una línea. Lo único que haría falta
añadir es la sincronización del contrato, que hoy resuelve el `make`.

La propiedad que importa —independencia real— ya está garantizada por los
módulos separados. El monorepo es solo dónde viven hoy.

## 4. Consecuencias

### Positivas

- Los cinco servicios se compilan y despliegan por separado, verificado en el
  despliegue real.
- Dependencias mínimas por servicio, lo que reduce la superficie que auditar.
- Un cambio de contrato es un commit atómico y verificado.
- Un solo `git clone` reproduce el sistema completo.
- Pasar a un repositorio por servicio sigue siendo posible sin tocar código.

### Negativas aceptadas

- **El contrato está copiado en cinco sitios.** La comprobación automática
  impide la divergencia, pero sigue siendo duplicación que hay que sincronizar
  al cambiarlo.
- **Hay código repetido entre servicios**: el arranque, la carga de
  configuración, el registro estructurado y el bucle de lotes se parecen mucho
  entre el persister y el indexer. Es deliberado —extraerlo los acoplaría— pero
  significa que una mejora en uno no llega sola a los demás.
- **La independencia hay que sostenerla a propósito.** Nada impide técnicamente
  que alguien añada un `replace ../otro-servicio` y rompa la propiedad. Es una
  disciplina, no una garantía del compilador.
- **El repositorio crece.** Cinco módulos significan cinco `go.sum` y cinco
  árboles de dependencias que actualizar por separado.

## 5. Impacto en el negocio

*Redactado para lectura de dirección, sin términos técnicos.*

### Qué se decidió

Que los cinco programas del sistema vivan **en un mismo archivo de proyecto,
pero como piezas independientes**: se pueden actualizar y reemplazar uno por uno,
sin tocar los demás.

### Por qué importa

Si los programas estuvieran fundidos en uno solo, cualquier corrección —por
pequeña que fuera— obligaría a reinstalar el sistema completo, con el riesgo y
la ventana de interrupción que eso supone. Con esta organización se puede
corregir el componente que archiva la información sin detener el que detecta
incidentes.

La otra opción era el extremo contrario: cinco proyectos separados. Se descartó
porque cualquier cambio que afecte a varios obligaría a coordinar cinco
actualizaciones distintas, y el equipo que mantendrá el sistema es reducido.

### Qué exige

Una disciplina de trabajo: mantener los programas verdaderamente separados
aunque convivan en el mismo proyecto. Hay una comprobación automática que
detecta si alguien rompe esa separación en la parte más delicada —la definición
común de los datos—, pero el resto depende del criterio de quien programe.

### Costo

Ninguno adicional. Es una decisión de organización, no de infraestructura, y no
añade licencias ni equipos.

---

## Referencias

- Enunciado del proyecto final, sección 4 — toda decisión relevante se registra
  como ADR.
- [ADR-002](002-lenguaje-y-runtime.md) — la elección de Go determina que la
  unidad de independencia sea el módulo.
- [contracts/README.md](../../contracts/README.md) — por qué el contrato es un
  schema y no una biblioteca.
- `make.sh check-contracts` — la comprobación que impide la divergencia.
