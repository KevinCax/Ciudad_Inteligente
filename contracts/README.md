# Contratos

Esta carpeta es la **fuente de verdad** de los contratos de datos del sistema. No es un modulo Go y ningun servicio la importa como dependencia: cada microservicio mantiene su propia copia embebida del schema que necesita.

## Por que una copia por servicio y no una libreria compartida

Una libreria Go compartida entre el ingestor y el correlator obligaria a compilar y versionar ambos servicios juntos: un cambio en el contrato forzaria un redespliegue coordinado. Eso es un monolito distribuido, no microservicios.

Aqui el contrato es el **JSON Schema**, no un tipo de Go. Cada servicio:

- embebe su copia con `go:embed`,
- define sus propios structs con solo los campos que ese servicio necesita,
- puede evolucionar a una version nueva del contrato sin esperar al otro.

El precio es el riesgo de drift entre copias, y se mitiga con `make sync-contracts`, que copia desde aqui y falla si alguna copia quedo desincronizada. Ese check debe correr en CI.

## Contratos vigentes

| Contrato | Version | Productor | Consumidor |
|---|---|---|---|
| `canonical-event/v1.0/schema.json` | 1.0 | Simuladores | Ingestor, Correlator |
| `correlated-alert/v1.0/schema.json` | 1.0 | Correlator | Persistencia, Grafana, ETL |

### canonical-event v1.0

Copia literal del schema del proyecto Java de referencia. **No modificar**: es el contrato acordado de la clase (`Final-Project-Guide.md` seccion 2.3) y los simuladores existentes dependen de el.

Nota sobre `additionalProperties: false`: en el proyecto Java la validacion corria sobre el objeto ya deserializado, asi que Jackson descartaba los campos desconocidos antes de validar y esta restriccion nunca detectaba nada. En Go validamos el **JSON crudo** antes de deserializar, con lo cual la restriccion si se aplica de verdad.

### correlated-alert v1.0

Contrato nuevo. En el sistema Java la alerta no tenia schema formal, solo una clase. Formalizarlo permite validar la salida antes de publicarla y documenta dos cosas que hoy estan rotas o implicitas:

- `score` es `number` entre 0 y 1. En Java se persistia como `Integer`, asi que 0.85 / 0.90 / 0.95 se guardaban como `1`.
- `rule_version` es nuevo: permite auditar con que umbrales se genero cada alerta cuando las reglas se vuelven configurables.

## Cambiar un contrato

1. Crear `vX.Y/` nueva junto a la anterior. **Nunca editar una version publicada.**
2. Actualizar los servicios que la consumen, uno a uno.
3. Registrar el cambio como ADR si rompe compatibilidad.
