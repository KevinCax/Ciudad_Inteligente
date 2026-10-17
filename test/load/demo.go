package main

import (
	"context"
	"fmt"
	"time"
)

// Simulacion controlada: casos escritos uno a uno, deterministas.
//
// La simulacion de la ciudad deja que los incidentes ocurran al azar, y por eso
// necesita horas para que haya los suficientes. Esto es lo contrario: una lista de
// casos concretos, cada uno con lo que se espera de el, que se prepara en segundos
// y dura unos ocho minutos. Sirve para DEMOSTRAR, no para medir: cada caso explica
// una capacidad del sistema, o un limite suyo, y el resultado no depende del azar.
//
// Los casos se reparten en zonas distintas a proposito. Un pulsador de un caso y un
// coche rapido de otro, en la misma zona y a pocos minutos, se correlacionarian
// entre si, y la demostracion dejaria de poder explicarse caso por caso.
//
// El trafico de fondo sigue corriendo, ligero: la ciudad no esta vacia, y el
// evaluador separa lo que provocan los casos de lo que genera el trafico.

// pasoDemo es un caso del guion.
type pasoDemo struct {
	offset time.Duration // cuando ocurre, respecto al inicio
	clave  string
	nombre string
	espera []string // alertas que el sistema deberia producir
	limite string   // si no vacio, lo esperado es un LIMITE conocido del sistema
	hacer  func(c *ciudad, t time.Time) (string, bool)
}

func (c *ciudad) marcar(zonas ...string) {
	for _, z := range zonas {
		c.usadas[z] = true
	}
}

func (c *ciudad) lprEn(s sensor, placa string, vel float64, rojo bool, t time.Time) {
	payload := map[string]any{
		"placa_vehicular":    placa,
		"velocidad_estimada": vel,
		"modelo_vehiculo":    "sedan",
		"color_vehiculo":     "gris",
		"ubicacion_sensor":   s.ID,
	}
	if rojo {
		payload["infraccion"] = InfraccionSemaforoRojo
	}
	c.programar(c.evento("sensor.lpr", "gateway-camaras-lpr", s.Zona, s.Lat, s.Lon, t, "", payload))
}

func (c *ciudad) pulsador(b sensor, t time.Time) {
	c.programar(c.evento("panic.button", "gateway-botones-panico", b.Zona, b.Lat, b.Lon, t, "",
		map[string]any{
			"tipo_de_alerta":            "panico",
			"identificador_dispositivo": b.ID,
			"user_context":              "fijo",
		}))
}

// ---------------------------------------------------------------------------
// Busqueda de infraestructura adecuada para cada caso
// ---------------------------------------------------------------------------

// condicionHuida decide si una ruta sirve para un caso de huida.
type condicionHuida func(b sensor, r *rutaCalle, lprs []pasada) bool

// prepararHuida busca un pulsador libre y un destino cuya ruta cumpla la condicion.
//
// Es una busqueda y no una eleccion fija porque lo que hace falta depende del
// callejero: una huida "detectable" necesita pasar por tres camaras, la primera en
// la zona del pulsador y a menos de 100 segundos, y no todo pulsador tiene una ruta
// asi. Es determinista: recorre los sensores y las zonas siempre en el mismo orden.
func (c *ciudad) prepararHuida(cond condicionHuida) (sensor, *rutaCalle, []pasada, bool) {
	for _, b := range c.btn {
		if c.usadas[b.Zona] {
			continue
		}
		for _, z := range zonas {
			if z.nombre == b.Zona {
				continue
			}
			if km := distanciaRectaKm([2]float64{b.Lat, b.Lon}, [2]float64{z.lat, z.lon}); km < 2.5 || km > 8 {
				continue
			}
			r, err := pedirRuta([2]float64{b.Lat, b.Lon}, [2]float64{z.lat, z.lon})
			if err != nil || r.duracionS < 120 || r.duracionS > 400 {
				continue // se quiere una huida corta: la demostracion no puede durar media hora
			}

			var lprs []pasada
			for _, p := range c.pasadasDe(r) {
				if p.s.Tipo == "lpr" && p.along > 100 {
					lprs = append(lprs, p)
				}
			}
			if !cond(b, r, lprs) {
				continue
			}

			libre := true
			for _, p := range lprs {
				if p.s.Zona != b.Zona && c.usadas[p.s.Zona] {
					libre = false
				}
			}
			if libre {
				return b, r, lprs, true
			}
		}
	}
	return sensor{}, nil, nil, false
}

// tiempoPorCalle es lo que tarda un coche en ir de un sensor a otro por el
// callejero, a flujo libre, con unos segundos de margen.
//
// Los casos que colocan un vehiculo junto a un pulsador no pueden inventar la hora:
// 30 segundos para recorrer 640 m de calle son 77 km/h, y la verificacion contra
// el callejero lo detecta. El correlator solo mide la recta y lo habria aceptado.
func tiempoPorCalle(a, b sensor) (time.Duration, bool) {
	r, err := pedirRuta([2]float64{a.Lat, a.Lon}, [2]float64{b.Lat, b.Lon})
	if err != nil || r.duracionS <= 0 {
		return 0, false
	}
	return time.Duration(r.duracionS+8) * time.Second, true
}

// primeraEn dice cuanto tarda el vehiculo, tras el pulsador, en llegar a una camara.
func primeraEn(r *rutaCalle, p pasada) time.Duration {
	return 15*time.Second + time.Duration(p.along/(r.distanciaM/r.duracionS)*float64(time.Second))
}

// emitirHuida anota el pulsador y las lecturas de un vehiculo que se aleja.
func (c *ciudad) emitirHuida(b sensor, r *rutaCalle, lprs []pasada, placa string, t time.Time) {
	c.zonaDelIncidente(b.Zona)
	if c.etiq != nil {
		c.etiq.Placa = placa
	}
	c.pulsador(b, t)

	vavg := r.distanciaM / r.duracionS
	salida := t.Add(15 * time.Second)
	rojo := false
	zs := []string{b.Zona}
	for i, p := range lprs {
		aqui := p.s.Semaforo && !rojo
		if aqui {
			rojo = true
		}
		c.lprEn(p.s, placa, 88+7*float64(i), aqui, salida.Add(time.Duration(p.along/vavg*float64(time.Second))))
		zs = append(zs, p.s.Zona)
	}
	c.marcar(zs...)
}

// ---------------------------------------------------------------------------
// El guion
// ---------------------------------------------------------------------------

func (c *ciudad) guion() []pasoDemo {
	s := time.Second

	return []pasoDemo{
		{
			offset: 0, clave: "huida_detectable", nombre: "Robo con huida, bien cubierto",
			espera: []string{"possible_robbery", "vehicle_pursuit"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				b, r, lprs, ok := c.prepararHuida(func(b sensor, r *rutaCalle, l []pasada) bool {
					return len(l) >= 3 && l[0].s.Zona == b.Zona && primeraEn(r, l[0]) <= 100*s
				})
				if !ok {
					return "no hay un pulsador con una ruta que pase por tres camaras", false
				}
				c.emitirHuida(b, r, lprs, "P801DEM", t)
				return fmt.Sprintf("pulsador %s, %d camaras en la ruta, %.1f km", b.ID, len(lprs), r.distanciaM/1000), true
			},
		},
		{
			offset: 10 * s, clave: "accidente_completo", nombre: "Accidente con sensor y caida de velocidad",
			espera: []string{"accident"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					cam, ok := c.camaraCerca(a.Lat, a.Lon, 0.7)
					if !ok || cam.Zona != a.Zona || c.usadas[a.Zona] {
						continue
					}
					impacto := t.Add(12 * s)
					c.zonaDelIncidente(a.Zona)
					c.lprEn(cam, "P412ACC", 92, false, t.Add(2*s))
					c.lprEn(cam, "P412ACC", 18, false, impacto.Add(3*s))
					c.acustico(a, "vidrio_roto", 104, 0.88, impacto)
					c.reporte(a, "accidente", "choque entre dos vehiculos en el cruce", "warning", impacto.Add(30*s))
					c.marcar(a.Zona)
					c.usadasAcu[a.Zona] = true
					sitio := a
					c.sitioAccidente = &sitio
					return fmt.Sprintf("sensor %s y camara %s en %s: el coche entra a 92 km/h y sale a 18", a.ID, cam.ID, a.Zona), true
				}
				return "no hay un sensor acustico con una camara cerca", false
			},
		},
		{
			offset: 20 * s, clave: "incendio_completo", nombre: "Incendio con sensor acustico",
			espera: []string{"fire"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					if c.usadasAcu[a.Zona] {
						continue
					}
					c.zonaDelIncidente(a.Zona)
					c.acustico(a, "explosion", 118, 0.95, t.Add(8*s))
					c.reporte(a, "incendio", "incendio en una bodega", "critical", t.Add(48*s))
					c.usadasAcu[a.Zona] = true
					return fmt.Sprintf("explosion de 118 dB en %s y reporte ciudadano 40 s despues", a.Zona), true
				}
				return "no quedan sensores acusticos libres", false
			},
		},
		{
			offset: 30 * s, clave: "exceso_velocidad", nombre: "Tres coches a mas de 80 km/h en una zona",
			espera: []string{"traffic_speed_violation"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, cam := range c.lpr {
					if c.usadas[cam.Zona] {
						continue
					}
					c.zonaDelIncidente(cam.Zona)
					for i, placa := range []string{"P610VEL", "P611VEL", "P612VEL"} {
						c.lprEn(cam, placa, 91+6*float64(i), false, t.Add(time.Duration(30*i)*s))
					}
					c.marcar(cam.Zona)
					return fmt.Sprintf("camara %s en %s: 91, 97 y 103 km/h en un minuto", cam.ID, cam.Zona), true
				}
				return "no quedan camaras libres", false
			},
		},
		{
			offset: 40 * s, clave: "huida_una_camara", nombre: "Huida que solo pasa por una camara",
			espera: []string{"possible_robbery"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				b, r, lprs, ok := c.prepararHuida(func(b sensor, r *rutaCalle, l []pasada) bool {
					return len(l) == 1 && l[0].s.Zona == b.Zona && primeraEn(r, l[0]) <= 100*s
				})
				if !ok {
					return "no hay una ruta que pase por exactamente una camara", false
				}
				c.emitirHuida(b, r, lprs, "P802DEM", t)
				return fmt.Sprintf("pulsador %s, una sola camara: hay robo pero no hay persecucion", b.ID), true
			},
		},
		{
			offset: 50 * s, clave: "huida_sin_camaras", nombre: "Huida por calles sin ninguna camara",
			espera: nil, limite: "LIMITE: sin cobertura de sensores no hay nada que correlacionar",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				b, r, lprs, ok := c.prepararHuida(func(b sensor, r *rutaCalle, l []pasada) bool { return len(l) == 0 })
				if !ok {
					return "no hay una ruta sin camaras", false
				}
				c.emitirHuida(b, r, lprs, "P803DEM", t)
				return fmt.Sprintf("pulsador %s: el coche no pasa por ninguna camara", b.ID), true
			},
		},
		{
			offset: 60 * s, clave: "panico_falso", nombre: "Boton pulsado por error",
			espera: nil,
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, b := range c.btn {
					if c.usadas[b.Zona] {
						continue
					}
					c.zonaDelIncidente(b.Zona)
					c.pulsador(b, t)
					c.marcar(b.Zona)
					return fmt.Sprintf("pulsador %s sin nada mas alrededor", b.ID), true
				}
				return "no quedan pulsadores libres", false
			},
		},
		{
			offset: 70 * s, clave: "coche_previo_lejano", nombre: "Coche rapido a 2 km, un minuto antes del pulsador",
			espera: nil,
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, b := range c.btn {
					for _, cam := range c.lpr {
						d := distanciaRectaKm([2]float64{b.Lat, b.Lon}, [2]float64{cam.Lat, cam.Lon})
						if cam.Zona != b.Zona || c.usadas[b.Zona] || d < 2.0 || d > 4.0 {
							continue
						}
						c.zonaDelIncidente(b.Zona)
						c.lprEn(cam, "P620LEJ", 96, false, t)
						c.pulsador(b, t.Add(55*s))
						c.marcar(b.Zona)
						return fmt.Sprintf("camara %s a %.1f km de %s: no puede estar huyendo de el", cam.ID, d, b.ID), true
					}
				}
				return "no hay un pulsador con una camara a 2-4 km en su zona", false
			},
		},
		{
			offset: 80 * s, clave: "coche_junto_al_pulsador", nombre: "Coche rapido junto a un pulsador accidental",
			espera: []string{"possible_robbery"}, limite: "FALSA ALARMA prevista: una coincidencia que ningun dato permite distinguir de un robo",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, b := range c.btn {
					for _, cam := range c.lpr {
						d := distanciaRectaKm([2]float64{b.Lat, b.Lon}, [2]float64{cam.Lat, cam.Lon})
						if cam.Zona != b.Zona || c.usadas[b.Zona] || d > 1.0 {
							continue
						}
						llega, ok := tiempoPorCalle(b, cam)
						if !ok || llega > 100*s {
							continue // la regla de robo solo mira dos minutos
						}
						c.zonaDelIncidente(b.Zona)
						c.pulsador(b, t)
						c.lprEn(cam, "P621COI", 96, false, t.Add(llega))
						c.marcar(b.Zona)
						return fmt.Sprintf("camara %s a %.1f km de %s, %.0f s despues: parece un robo y no lo es", cam.ID, d, b.ID, llega.Seconds()), true
					}
				}
				return "no hay un pulsador con una camara a menos de 1 km", false
			},
		},
		{
			offset: 90 * s, clave: "placa_clonada", nombre: "Misma placa a 7 km de distancia en 25 segundos",
			espera: []string{"possible_robbery"},
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, b := range c.btn {
					if c.usadas[b.Zona] {
						continue
					}
					for _, c1 := range c.lpr {
						d1 := distanciaRectaKm([2]float64{b.Lat, b.Lon}, [2]float64{c1.Lat, c1.Lon})
						if c1.Zona != b.Zona || d1 > 0.8 {
							continue
						}
						llega, ok := tiempoPorCalle(b, c1)
						if !ok || llega > 100*s {
							continue
						}
						for _, c2 := range c.lpr {
							if distanciaRectaKm([2]float64{c1.Lat, c1.Lon}, [2]float64{c2.Lat, c2.Lon}) < 6 || c.usadas[c2.Zona] {
								continue
							}
							c.zonaDelIncidente(b.Zona)
							c.pulsador(b, t)
							c.lprEn(c1, "P555CLN", 85, false, t.Add(llega))
							c.lprEn(c2, "P555CLN", 110, false, t.Add(llega+25*s))
							c.marcar(b.Zona, c2.Zona)
							return fmt.Sprintf("%s y %s a %.1f km en 25 s: la segunda lectura se descarta", c1.ID, c2.ID,
								distanciaRectaKm([2]float64{c1.Lat, c1.Lon}, [2]float64{c2.Lat, c2.Lon})), true
						}
					}
				}
				return "no hay un par de camaras a mas de 6 km", false
			},
		},
		{
			offset: 100 * s, clave: "accidente_sin_acustico", nombre: "Accidente sin sensor acustico cerca",
			espera: nil, limite: "LIMITE: sin sensor acustico solo hay un reporte ciudadano, que ninguna regla confirma",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					if c.usadasAcu[a.Zona] {
						continue
					}
					c.zonaDelIncidente(a.Zona)
					c.reporte(a, "accidente", "motocicleta derribada", "warning", t.Add(20*s))
					c.usadasAcu[a.Zona] = true
					return fmt.Sprintf("solo un reporte ciudadano en %s", a.Zona), true
				}
				return "no quedan sensores acusticos libres", false
			},
		},
		{
			offset: 110 * s, clave: "incendio_sin_acustico", nombre: "Incendio sin sensor acustico",
			espera: nil, limite: "LIMITE: sin sensor acustico solo hay un reporte ciudadano, que ninguna regla confirma",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					if c.usadasAcu[a.Zona] {
						continue
					}
					c.zonaDelIncidente(a.Zona)
					c.reporte(a, "incendio", "humo saliendo de un local", "critical", t.Add(20*s))
					c.usadasAcu[a.Zona] = true
					return fmt.Sprintf("solo un reporte ciudadano en %s", a.Zona), true
				}
				return "no quedan sensores acusticos libres", false
			},
		},
		{
			offset: 120 * s, clave: "altercado", nombre: "Reporte de un altercado",
			espera: nil,
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					c.zonaDelIncidente(a.Zona)
					c.reporte(a, "altercado", "discusion que escala entre conductores", "info", t)
					return fmt.Sprintf("reporte en %s: ninguna regla lo correlaciona", a.Zona), true
				}
				return "no hay sensores", false
			},
		},
		{
			offset: 130 * s, clave: "ruido_falso", nombre: "Un sensor acustico oye un petardo",
			espera: nil,
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				for _, a := range c.acu {
					if c.usadasAcu[a.Zona] {
						continue
					}
					c.zonaDelIncidente(a.Zona)
					c.acustico(a, "explosion", 84, 0.2, t)
					c.usadasAcu[a.Zona] = true
					return fmt.Sprintf("84 dB en %s, por debajo del umbral de incendio", a.Zona), true
				}
				return "no quedan sensores acusticos libres", false
			},
		},
		{
			offset: 160 * s, clave: "accidente_repetido", nombre: "Segundo accidente en el mismo cruce, 2 minutos despues",
			espera: nil, limite: "supresion de repetidas: el sistema lo da por el mismo incidente que la alerta vigente",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				a := c.sitioAccidente
				if a == nil {
					return "el accidente del primer caso no se pudo preparar", false
				}
				c.zonaDelIncidente(a.Zona)
				c.acustico(*a, "vidrio_roto", 99, 0.8, t.Add(12*s))
				c.reporte(*a, "accidente", "colision multiple, hay heridos", "warning", t.Add(42*s))
				return fmt.Sprintf("otro choque en %s a los 2 min: la alerta anterior sigue vigente", a.Zona), true
			},
		},
		{
			offset: 150 * s, clave: "huida_fuera_de_zona", nombre: "Huida cuyas infracciones ocurren fuera de la zona del boton",
			espera: nil,
			limite: "LIMITE: la regla ancla la huida a una infraccion EN LA ZONA DEL BOTON, y aqui todas ocurren fuera",
			hacer: func(c *ciudad, t time.Time) (string, bool) {
				b, r, lprs, ok := c.prepararHuida(func(b sensor, r *rutaCalle, l []pasada) bool {
					if len(l) < 2 {
						return false
					}
					for _, p := range l {
						if p.s.Zona == b.Zona {
							return false
						}
					}
					return true
				})
				if !ok {
					return "no hay una ruta cuyas camaras queden todas fuera de la zona del boton", false
				}
				c.emitirHuida(b, r, lprs, "P804DEM", t)
				return fmt.Sprintf("pulsador %s: las %d camaras estan en otras zonas", b.ID, len(lprs)), true
			},
		},
	}
}

// demo prepara el guion y lo reproduce.
func (g *generador) demo(ctx context.Context, rutaCatalogo string, semilla uint64, rutaVerdad string) *resultados {
	res := nuevosResultados()

	cat, err := cargarCatalogo(rutaCatalogo)
	if err != nil {
		fmt.Println("  no hay catalogo de sensores: ejecute antes ./make.sh catalogo")
		res.registrarError("catalogo")
		return res
	}

	c := nuevaCiudad(g, res, cat, semilla)
	c.rutaVerdad = rutaVerdad
	c.porMinuto = 18 // trafico de fondo ligero: la ciudad no esta vacia, pero no tapa los casos

	base := time.Now().Add(20 * time.Second)
	fmt.Println("  Simulacion controlada: preparando los casos")
	fmt.Println("")

	fallidos := 0
	for _, p := range c.guion() {
		inicio := base.Add(p.offset)
		c.etiq = &verdad{
			ID:       fmt.Sprintf("CASO-%02d", len(c.verdades)+1),
			Clase:    p.clave,
			Caso:     p.nombre,
			Esperado: p.espera,
			Limite:   p.limite,
			Inicio:   inicio,
		}
		nota, ok := p.hacer(c, inicio)
		if !ok {
			fmt.Printf("  - %-52s NO SE PUDO PREPARAR: %s\n", p.nombre, nota)
			c.etiq = nil
			fallidos++
			continue
		}
		c.etiq.Nota = nota
		c.verdades = append(c.verdades, *c.etiq)
		c.etiq = nil
		fmt.Printf("  + %-52s %s\n", p.nombre, nota)
	}
	fmt.Println("")

	if len(c.verdades) == 0 {
		res.registrarError("sin_casos")
		return res
	}
	if fallidos > 0 {
		fmt.Printf("  %d casos no se pudieron preparar con la red de sensores actual\n\n", fallidos)
	}

	// Se corre hasta que se haya enviado el ultimo evento de un caso.
	hasta := c.ultimoEtiq.Add(30 * time.Second)
	fmt.Printf("  durara unos %s\n\n", time.Until(hasta).Round(time.Second))
	return c.correr(ctx, time.Until(hasta), 0)
}
