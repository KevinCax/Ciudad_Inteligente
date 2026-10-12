// Package dispatch decide a que organismos se avisa de cada alerta y deja
// constancia de cada aviso.
package dispatch

import (
	"fmt"
	"sort"
	"strings"

	"ciudadinteligente/notifier/internal/canonical"
)

// Organismos a los que el sistema puede avisar.
//
// Son los cuerpos de respuesta de la Municipalidad. Se declaran como
// constantes y no como literales para que un error de escritura no produzca
// un organismo fantasma al que nadie atiende.
const (
	Bomberos   = "bomberos"
	Ambulancia = "ambulancia"
	Policia    = "policia"
	Transito   = "transito"
)

// Matriz asocia cada tipo de alerta con los organismos que deben recibirla.
type Matriz map[string][]string

// MatrizPorDefecto es el criterio de despacho de fabrica.
//
// El orden dentro de cada lista no implica prioridad: todos los avisos de una
// alerta se emiten a la vez. Se mantiene estable solo para que el registro sea
// comparable entre ejecuciones.
//
// El criterio es deliberadamente generoso en incendio y accidente: el coste de
// movilizar una ambulancia que no hacia falta es muy inferior al de no
// movilizarla. En las infracciones de transito ocurre lo contrario, y por eso
// solo interviene transito.
func MatrizPorDefecto() Matriz {
	return Matriz{
		canonical.AlertFire:                  {Bomberos, Ambulancia},
		canonical.AlertAccident:              {Ambulancia, Policia, Transito},
		canonical.AlertPossibleRobbery:       {Policia},
		canonical.AlertTrafficSpeedViolation: {Transito},
		canonical.AlertVehiclePursuit:        {Policia, Transito},
	}
}

// TiposConocidos devuelve los tipos de alerta que el correlator puede emitir.
//
// Sirve para comprobar al arrancar que ninguno se quedo sin destinatarios: una
// alerta sin entrada en la matriz no avisa a nadie, y ese es un fallo que no
// se manifiesta hasta que ocurre la emergencia.
func TiposConocidos() []string {
	return []string{
		canonical.AlertPossibleRobbery,
		canonical.AlertAccident,
		canonical.AlertFire,
		canonical.AlertTrafficSpeedViolation,
		canonical.AlertVehiclePursuit,
	}
}

// Destinatarios devuelve los organismos a avisar de un tipo de alerta.
//
// El segundo valor indica si el tipo estaba contemplado. Se distingue el caso
// "contemplado y sin destinatarios" del caso "tipo desconocido": el primero es
// una decision, el segundo es un hueco en la configuracion.
func (m Matriz) Destinatarios(tipo string) ([]string, bool) {
	orgs, ok := m[tipo]
	return orgs, ok
}

// SinDestinatarios enumera los tipos conocidos que la matriz no cubre.
func (m Matriz) SinDestinatarios() []string {
	var faltan []string
	for _, t := range TiposConocidos() {
		if orgs, ok := m[t]; !ok || len(orgs) == 0 {
			faltan = append(faltan, t)
		}
	}
	sort.Strings(faltan)
	return faltan
}

// AplicarEntorno sustituye filas de la matriz desde variables de entorno.
//
// La clave es DISPATCH_<TIPO_EN_MAYUSCULAS> y el valor una lista separada por
// comas: DISPATCH_FIRE=bomberos,ambulancia. Permite que la Municipalidad
// cambie el criterio de despacho sin recompilar, que es justo lo que cambia
// cuando se reorganiza un cuerpo de respuesta.
//
// Para silenciar un tipo a proposito se escribe el valor "ninguno". No se usa
// la cadena vacia para eso: docker compose define las variables no declaradas
// como cadena vacia, de modo que "vacio significa no avisar a nadie" habria
// convertido un despliegue por defecto en un sistema que no avisa de los
// incendios. La palabra obliga a que silenciar sea una decision escrita.
//
// Lo que no se acepta es un valor sin sentido: un organismo mal escrito
// produce avisos que nadie recoge, y el sistema pareceria estar despachando.
func (m Matriz) AplicarEntorno(valores map[string]string) (Matriz, error) {
	salida := Matriz{}
	for k, v := range m {
		salida[k] = append([]string(nil), v...)
	}

	for _, tipo := range TiposConocidos() {
		crudo, presente := valores["DISPATCH_"+strings.ToUpper(tipo)]
		if !presente {
			continue
		}

		orgs, err := parsearOrganismos(crudo)
		if err != nil {
			return nil, fmt.Errorf("DISPATCH_%s: %w", strings.ToUpper(tipo), err)
		}
		salida[tipo] = orgs
	}

	return salida, nil
}

// SinDestinatariosExplicito silencia un tipo de alerta a proposito.
const SinDestinatariosExplicito = "ninguno"

func parsearOrganismos(crudo string) ([]string, error) {
	crudo = strings.TrimSpace(crudo)
	if crudo == "" {
		return nil, fmt.Errorf("valor vacio; para no avisar a nadie escriba %q", SinDestinatariosExplicito)
	}
	if strings.EqualFold(crudo, SinDestinatariosExplicito) {
		return []string{}, nil
	}

	var orgs []string
	visto := map[string]bool{}

	for _, parte := range strings.Split(crudo, ",") {
		org := strings.ToLower(strings.TrimSpace(parte))
		if org == "" {
			return nil, fmt.Errorf("hay un organismo vacio en la lista")
		}
		if !esNombreValido(org) {
			return nil, fmt.Errorf("%q no es un nombre de organismo valido: se esperan letras, digitos, guion o guion bajo", org)
		}
		if visto[org] {
			return nil, fmt.Errorf("%q aparece repetido", org)
		}
		visto[org] = true
		orgs = append(orgs, org)
	}

	return orgs, nil
}

// esNombreValido no comprueba contra una lista cerrada a proposito: la
// Municipalidad puede incorporar un cuerpo de respuesta nuevo sin que haya que
// tocar el codigo. Lo que si se exige es que el nombre sea utilizable como
// etiqueta de metrica y como valor de columna.
func esNombreValido(org string) bool {
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// Resumen describe la matriz vigente en una linea, para registrarla al
// arrancar. Sin esto, cambiar DISPATCH_FIRE por entorno no deja rastro y
// despues nadie puede explicar por que no se aviso a los bomberos.
func (m Matriz) Resumen() string {
	tipos := make([]string, 0, len(m))
	for t := range m {
		tipos = append(tipos, t)
	}
	sort.Strings(tipos)

	partes := make([]string, 0, len(tipos))
	for _, t := range tipos {
		orgs := m[t]
		if len(orgs) == 0 {
			partes = append(partes, t+"=(ninguno)")
			continue
		}
		partes = append(partes, t+"="+strings.Join(orgs, "+"))
	}
	return strings.Join(partes, " ")
}
