package esearch

import (
	"testing"
	"time"
)

func TestMesDeIndice(t *testing.T) {
	casos := []struct {
		nombre   string
		esperado string
		valido   bool
	}{
		{"events-2026.09", "2026-09", true},
		{"alerts-2025.12", "2025-12", true},
		{"events-2026.01", "2026-01", true},

		// Nombres que no siguen la convencion: no deben reconocerse, porque
		// borrar un indice que no entendemos es peor que conservarlo.
		{"events", "", false},
		{"events-001", "", false},
		{".kibana_1", "", false},
		{"events-2026", "", false},
		{"events-", "", false},
	}

	for _, c := range casos {
		mes, ok := mesDeIndice(c.nombre)
		if ok != c.valido {
			t.Errorf("%q: se esperaba valido=%v, se obtuvo %v", c.nombre, c.valido, ok)
			continue
		}
		if ok && mes.Format("2006-01") != c.esperado {
			t.Errorf("%q: se esperaba %s, se obtuvo %s", c.nombre, c.esperado, mes.Format("2006-01"))
		}
	}
}

// El limite se compara por mes completo, no por dia: un indice solo debe
// borrarse cuando el mes entero que cubre quedo fuera del plazo. Comparar por
// dia borraria el mes en curso del limite, con datos aun dentro de plazo.
func TestLimiteSeComparaPorMesCompleto(t *testing.T) {
	// Supongamos hoy 2026-09-21 y una retencion de 12 meses.
	// El limite cae en 2025-09-21, y su mes es 2025-09.
	ahora := time.Date(2026, 9, 21, 15, 0, 0, 0, time.UTC)
	limite := ahora.AddDate(0, -12, 0)
	limiteMes := time.Date(limite.Year(), limite.Month(), 1, 0, 0, 0, 0, time.UTC)

	casos := map[string]bool{
		"events-2025.08": true,  // agosto 2025: anterior al mes limite, se borra
		"events-2025.09": false, // septiembre 2025: es el mes limite, se conserva
		"events-2025.10": false,
		"events-2026.09": false,
	}

	for nombre, deberiaBorrarse := range casos {
		mes, ok := mesDeIndice(nombre)
		if !ok {
			t.Fatalf("%q deberia ser un nombre valido", nombre)
		}
		if got := mes.Before(limiteMes); got != deberiaBorrarse {
			t.Errorf("%q: se esperaba borrar=%v, se obtuvo %v", nombre, deberiaBorrarse, got)
		}
	}
}
