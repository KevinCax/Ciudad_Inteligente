package dispatch

import (
	"strings"
	"testing"

	"ciudadinteligente/notifier/internal/canonical"
)

// La matriz de fabrica debe cubrir todos los tipos que el correlator emite.
// Un tipo sin fila no produce ningun error en ejecucion: sencillamente no
// avisa a nadie, y eso solo se descubre cuando ocurre la emergencia.
func TestMatrizPorDefectoCubreTodosLosTipos(t *testing.T) {
	if faltan := MatrizPorDefecto().SinDestinatarios(); len(faltan) > 0 {
		t.Fatalf("estos tipos de alerta no avisarian a nadie: %v", faltan)
	}
}

func TestIncendioAvisaBomberosYAmbulancia(t *testing.T) {
	orgs, ok := MatrizPorDefecto().Destinatarios(canonical.AlertFire)
	if !ok {
		t.Fatal("el incendio deberia estar contemplado")
	}
	if len(orgs) != 2 || orgs[0] != Bomberos || orgs[1] != Ambulancia {
		t.Fatalf("destinatarios inesperados para incendio: %v", orgs)
	}
}

// Un tipo desconocido se distingue de uno contemplado sin destinatarios: el
// primero es un hueco de configuracion, el segundo una decision.
func TestTipoDesconocidoSeDistingueDeListaVacia(t *testing.T) {
	m := MatrizPorDefecto()

	if _, ok := m.Destinatarios("tipo_que_no_existe"); ok {
		t.Fatal("un tipo inexistente no deberia darse por contemplado")
	}

	vacia, err := m.AplicarEntorno(map[string]string{"DISPATCH_FIRE": SinDestinatariosExplicito})
	if err != nil {
		t.Fatalf("silenciar un tipo a proposito es legitimo: %v", err)
	}
	orgs, ok := vacia.Destinatarios(canonical.AlertFire)
	if !ok {
		t.Fatal("el tipo sigue contemplado aunque no tenga destinatarios")
	}
	if len(orgs) != 0 {
		t.Fatalf("se esperaba ningun destinatario, hay %v", orgs)
	}
}

func TestAplicarEntornoSustituyeLaFila(t *testing.T) {
	m, err := MatrizPorDefecto().AplicarEntorno(map[string]string{
		"DISPATCH_POSSIBLE_ROBBERY": "policia, transito",
	})
	if err != nil {
		t.Fatalf("no deberia fallar: %v", err)
	}

	orgs, _ := m.Destinatarios(canonical.AlertPossibleRobbery)
	if len(orgs) != 2 || orgs[0] != Policia || orgs[1] != Transito {
		t.Fatalf("la fila no se sustituyo: %v", orgs)
	}

	// El resto de la matriz no debe verse afectado.
	fuego, _ := m.Destinatarios(canonical.AlertFire)
	if len(fuego) != 2 {
		t.Fatalf("cambiar una fila altero otra: %v", fuego)
	}
}

// AplicarEntorno trabaja sobre una copia: alterar el resultado no debe tocar
// la matriz de origen, o dos instancias compartirian estado sin saberlo.
func TestAplicarEntornoNoMutaElOrigen(t *testing.T) {
	origen := MatrizPorDefecto()

	if _, err := origen.AplicarEntorno(map[string]string{
		"DISPATCH_FIRE": "policia",
	}); err != nil {
		t.Fatalf("no deberia fallar: %v", err)
	}

	orgs, _ := origen.Destinatarios(canonical.AlertFire)
	if len(orgs) != 2 || orgs[0] != Bomberos {
		t.Fatalf("la matriz de origen se modifico: %v", orgs)
	}
}

func TestEntornoInvalidoImpideArrancar(t *testing.T) {
	casos := map[string]string{
		"organismo vacio":    "policia,,transito",
		"organismo repetido": "policia,policia",
		"caracteres raros":   "policia nacional civil!",

		// Este es el caso que importa de verdad. docker compose escribe
		// DISPATCH_FIRE="" cuando el .env no define nada. Si la cadena vacia
		// significara "no avisar a nadie", el despliegue por defecto dejaria
		// de avisar de los incendios en silencio. Debe rechazarse: para
		// silenciar un tipo esta la palabra "ninguno".
		"cadena vacia": "",
	}

	for nombre, valor := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := MatrizPorDefecto().AplicarEntorno(map[string]string{
				"DISPATCH_FIRE": valor,
			})
			if err == nil {
				t.Fatalf("%q deberia rechazarse en lugar de producir un organismo que nadie atiende", valor)
			}
			if !strings.Contains(err.Error(), "DISPATCH_FIRE") {
				t.Fatalf("el error deberia nombrar la variable culpable: %v", err)
			}
		})
	}
}

// El resumen es lo unico que queda en el log cuando alguien cambia el criterio
// de despacho por variable de entorno.
func TestResumenMencionaCadaTipo(t *testing.T) {
	resumen := MatrizPorDefecto().Resumen()
	for _, tipo := range TiposConocidos() {
		if !strings.Contains(resumen, tipo) {
			t.Fatalf("el resumen omite %q: %s", tipo, resumen)
		}
	}
}
