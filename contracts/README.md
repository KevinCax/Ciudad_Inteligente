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

Contrato de entrada del sistema. **No modificar sin versionar**: es el acuerdo con los productores (enunciado, seccion 2.3) y todos los simuladores dependen de el.

Nota sobre `additionalProperties: false`: la restriccion solo se aplica de verdad si se valida el **JSON crudo** antes de deserializar. Validando el objeto ya deserializado, el deserializador descarta los campos desconocidos antes de que el validador los vea, y la restriccion no detecta nada.

### correlated-alert v1.0

Contrato de salida. Formalizarlo —en lugar de dejar la alerta definida solo por el codigo que la construye— permite validarla antes de publicarla y deja explicitas dos cosas que de otro modo quedarian implicitas:

- `score` es `number` entre 0 y 1, no un entero: redondearlo guardaria 0.85, 0.90 y 0.95 como `1` y borraria la gradacion de confianza.
- `rule_version` es nuevo: permite auditar con que umbrales se genero cada alerta cuando las reglas se vuelven configurables.

## Cambiar un contrato

1. Crear `vX.Y/` nueva junto a la anterior. **Nunca editar una version publicada.**
2. Actualizar los servicios que la consumen, uno a uno.
3. Registrar el cambio como ADR si rompe compatibilidad.
