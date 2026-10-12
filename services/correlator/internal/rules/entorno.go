package rules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AplicarEntorno ajusta los umbrales del motor con lo que el operador haya
// definido en el entorno, y devuelve error si algun valor no tiene sentido.
//
// La validacion no es una formalidad. Un umbral de velocidad en cero no
// produce ningun error visible: produce un motor que considera "vehiculo a
// alta velocidad" a todo lo que pase, y la ciudad se llena de alertas de robo.
// Una ventana negativa produce lo contrario, un motor que no detecta nada.
// Ninguno de los dos casos se manifiesta como un fallo, asi que se rechazan al
// arrancar en lugar de descubrirse en produccion.
func (c Config) AplicarEntorno(vars map[string]string) (Config, error) {
	if len(vars) == 0 {
		return c, nil
	}

	var errs []string
	fallo := func(clave, valor, motivo string) {
		errs = append(errs, fmt.Sprintf("%s=%q: %s", clave, valor, motivo))
	}

	duracion := func(clave string, destino *time.Duration) {
		v, ok := vars[clave]
		if !ok {
			return
		}
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			fallo(clave, v, "no es una duracion valida (por ejemplo 2m o 90s)")
		case d <= 0:
			fallo(clave, v, "debe ser mayor que cero")
		default:
			*destino = d
		}
	}

	numero := func(clave string, destino *float64) {
		v, ok := vars[clave]
		if !ok {
			return
		}
		f, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil:
			fallo(clave, v, "no es un numero")
		case f <= 0:
			fallo(clave, v, "debe ser mayor que cero")
		default:
			*destino = f
		}
	}

	entero := func(clave string, destino *int) {
		v, ok := vars[clave]
		if !ok {
			return
		}
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			fallo(clave, v, "no es un entero")
		case n < 1:
			fallo(clave, v, "debe ser al menos 1")
		default:
			*destino = n
		}
	}

	booleano := func(clave string, destino *bool) {
		v, ok := vars[clave]
		if !ok {
			return
		}
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			fallo(clave, v, "debe ser true o false")
			return
		}
		*destino = b
	}

	duracion("RULE_VENTANA_ROBO", &c.VentanaRobo)
	duracion("RULE_VENTANA_ACCIDENTE", &c.VentanaAccidente)
	duracion("RULE_VENTANA_INCENDIO", &c.VentanaIncendio)
	duracion("RULE_VENTANA_VELOCIDAD", &c.VentanaVelocidad)
	duracion("RULE_VENTANA_PERSECUCION", &c.VentanaPersecucion)
	numero("RULE_UMBRAL_VELOCIDAD", &c.UmbralVelocidad)
	numero("RULE_VELOCIDAD_MAXIMA_PLAUSIBLE", &c.VelocidadMaximaPlausible)
	entero("RULE_MIN_LPR_VELOCIDAD", &c.MinLPRVelocidad)
	entero("RULE_MIN_INFRACCIONES_PERSECUCION", &c.MinInfraccionesPersecucion)
	numero("RULE_CAIDA_DESDE", &c.CaidaDesde)
	numero("RULE_CAIDA_HASTA", &c.CaidaHasta)
	numero("RULE_UMBRAL_DECIBELES", &c.UmbralDecibeles)
	booleano("RULE_ACCIDENTE_EXIGE_CAIDA", &c.AccidenteExigeCaida)

	// Una caida de velocidad exige que el punto de partida sea mas alto que
	// el de llegada. Invertidos, la transicion no se detecta nunca y la
	// alerta de accidente pierde su refuerzo de confianza sin avisar.
	if c.CaidaDesde <= c.CaidaHasta {
		errs = append(errs, fmt.Sprintf(
			"RULE_CAIDA_DESDE (%.0f) debe ser mayor que RULE_CAIDA_HASTA (%.0f): "+
				"una caida de velocidad va de rapido a lento",
			c.CaidaDesde, c.CaidaHasta))
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return c, fmt.Errorf("configuracion del motor de reglas invalida:\n  - %s",
			strings.Join(errs, "\n  - "))
	}
	return c, nil
}

// Resumen describe los umbrales vigentes, para registrarlos al arrancar.
//
// Sin esto, una alerta generada con parametros ajustados es indistinguible de
// una generada con los de fabrica, y la version de reglas sola no basta
// porque no cambia al mover una variable de entorno.
func (c Config) Resumen() string {
	return fmt.Sprintf(
		"ventanas robo=%s accidente=%s incendio=%s velocidad=%s; "+
			"umbral_velocidad=%.0f min_lpr=%d caida=%.0f->%.0f decibeles=%.0f "+
			"accidente_exige_caida=%t",
		c.VentanaRobo, c.VentanaAccidente, c.VentanaIncendio, c.VentanaVelocidad,
		c.UmbralVelocidad, c.MinLPRVelocidad, c.CaidaDesde, c.CaidaHasta,
		c.UmbralDecibeles, c.AccidenteExigeCaida)
}
