package rules

import (
	"strings"
	"testing"
	"time"
)

func TestEntornoVacioNoCambiaNada(t *testing.T) {
	base := ConfigPorDefecto()

	got, err := base.AplicarEntorno(nil)
	if err != nil {
		t.Fatalf("un entorno vacio no deberia fallar: %v", err)
	}
	if got != base {
		t.Error("sin variables definidas, la configuracion debe quedar intacta")
	}

	got, err = base.AplicarEntorno(map[string]string{})
	if err != nil || got != base {
		t.Error("un mapa vacio tampoco deberia cambiar nada")
	}
}

func TestEntornoAjustaCadaUmbral(t *testing.T) {
	got, err := ConfigPorDefecto().AplicarEntorno(map[string]string{
		"RULE_VENTANA_ROBO":          "90s",
		"RULE_VENTANA_ACCIDENTE":     "10m",
		"RULE_VENTANA_INCENDIO":      "3m",
		"RULE_VENTANA_VELOCIDAD":     "4m",
		"RULE_UMBRAL_VELOCIDAD":      "70",
		"RULE_MIN_LPR_VELOCIDAD":     "5",
		"RULE_CAIDA_DESDE":           "75",
		"RULE_CAIDA_HASTA":           "30",
		"RULE_UMBRAL_DECIBELES":      "95",
		"RULE_ACCIDENTE_EXIGE_CAIDA": "true",
	})
	if err != nil {
		t.Fatalf("valores validos no deberian fallar: %v", err)
	}

	casos := []struct {
		campo         string
		tengo, quiero any
	}{
		{"VentanaRobo", got.VentanaRobo, 90 * time.Second},
		{"VentanaAccidente", got.VentanaAccidente, 10 * time.Minute},
		{"VentanaIncendio", got.VentanaIncendio, 3 * time.Minute},
		{"VentanaVelocidad", got.VentanaVelocidad, 4 * time.Minute},
		{"UmbralVelocidad", got.UmbralVelocidad, 70.0},
		{"MinLPRVelocidad", got.MinLPRVelocidad, 5},
		{"CaidaDesde", got.CaidaDesde, 75.0},
		{"CaidaHasta", got.CaidaHasta, 30.0},
		{"UmbralDecibeles", got.UmbralDecibeles, 95.0},
		{"AccidenteExigeCaida", got.AccidenteExigeCaida, true},
	}
	for _, c := range casos {
		if c.tengo != c.quiero {
			t.Errorf("%s: se obtuvo %v, se esperaba %v", c.campo, c.tengo, c.quiero)
		}
	}
}

// Una variable suelta no debe arrastrar a las demas a sus valores de fabrica.
func TestEntornoParcialConservaElResto(t *testing.T) {
	base := ConfigPorDefecto()

	got, err := base.AplicarEntorno(map[string]string{"RULE_UMBRAL_VELOCIDAD": "60"})
	if err != nil {
		t.Fatalf("no deberia fallar: %v", err)
	}
	if got.UmbralVelocidad != 60 {
		t.Errorf("el umbral debio cambiar a 60, quedo en %v", got.UmbralVelocidad)
	}
	if got.VentanaRobo != base.VentanaRobo || got.MinLPRVelocidad != base.MinLPRVelocidad {
		t.Error("los demas umbrales debieron conservar su valor por defecto")
	}
}

// Estos valores no producen un error en tiempo de ejecucion: producen un motor
// que no detecta nada o que alerta por todo. Deben rechazarse al arrancar.
func TestEntornoRechazaValoresSinSentido(t *testing.T) {
	casos := map[string]map[string]string{
		"ventana en cero":              {"RULE_VENTANA_ROBO": "0s"},
		"ventana negativa":             {"RULE_VENTANA_ACCIDENTE": "-5m"},
		"ventana sin unidad":           {"RULE_VENTANA_INCENDIO": "300"},
		"umbral de velocidad en cero":  {"RULE_UMBRAL_VELOCIDAD": "0"},
		"umbral de velocidad negativo": {"RULE_UMBRAL_VELOCIDAD": "-80"},
		"umbral no numerico":           {"RULE_UMBRAL_VELOCIDAD": "rapido"},
		"minimo de lecturas en cero":   {"RULE_MIN_LPR_VELOCIDAD": "0"},
		"minimo de lecturas decimal":   {"RULE_MIN_LPR_VELOCIDAD": "2.5"},
		"booleano invalido":            {"RULE_ACCIDENTE_EXIGE_CAIDA": "puede"},
		"decibeles negativos":          {"RULE_UMBRAL_DECIBELES": "-10"},
	}

	for nombre, vars := range casos {
		if _, err := ConfigPorDefecto().AplicarEntorno(vars); err == nil {
			t.Errorf("%s: deberia rechazarse", nombre)
		}
	}
}

// Invertir la caida no da error en ejecucion: la transicion simplemente no se
// detecta nunca y el accidente pierde su refuerzo de confianza en silencio.
func TestEntornoRechazaCaidaInvertida(t *testing.T) {
	_, err := ConfigPorDefecto().AplicarEntorno(map[string]string{
		"RULE_CAIDA_DESDE": "30",
		"RULE_CAIDA_HASTA": "80",
	})
	if err == nil {
		t.Fatal("una caida de lento a rapido deberia rechazarse")
	}
	if !strings.Contains(err.Error(), "RULE_CAIDA_DESDE") {
		t.Errorf("el error deberia nombrar la variable culpable: %v", err)
	}
}

// El error debe enumerar TODOS los problemas: corregir uno y volver a fallar
// por el siguiente es una forma lenta de arrancar un servicio.
func TestEntornoReportaTodosLosErrores(t *testing.T) {
	_, err := ConfigPorDefecto().AplicarEntorno(map[string]string{
		"RULE_VENTANA_ROBO":      "0s",
		"RULE_UMBRAL_VELOCIDAD":  "-1",
		"RULE_MIN_LPR_VELOCIDAD": "0",
	})
	if err == nil {
		t.Fatal("se esperaba error")
	}
	for _, clave := range []string{"RULE_VENTANA_ROBO", "RULE_UMBRAL_VELOCIDAD", "RULE_MIN_LPR_VELOCIDAD"} {
		if !strings.Contains(err.Error(), clave) {
			t.Errorf("el error deberia mencionar %s: %v", clave, err)
		}
	}
}

func TestResumenNombraLosUmbrales(t *testing.T) {
	r := ConfigPorDefecto().Resumen()
	for _, fragmento := range []string{"robo=2m", "umbral_velocidad=80", "min_lpr=3", "accidente_exige_caida=false"} {
		if !strings.Contains(r, fragmento) {
			t.Errorf("el resumen deberia contener %q: %s", fragmento, r)
		}
	}
}
