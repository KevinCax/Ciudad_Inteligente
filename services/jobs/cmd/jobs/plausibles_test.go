package main

import (
	"testing"
	"time"

	"ciudadinteligente/jobs/internal/store"
)

var t0 = time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)

func punto(seg int, lat, lon float64) store.Punto {
	return store.Punto{Lat: lat, Lon: lon, Momento: t0.Add(time.Duration(seg) * time.Second)}
}

// La placa clonada: la misma matricula a 8.8 km, 25 segundos despues. El recorrido
// no puede pasar por ahi.
func TestPlausiblesDescartaElSaltoImposible(t *testing.T) {
	ps := []store.Punto{
		punto(0, 14.6360, -90.5090),  // pulsador
		punto(30, 14.6330, -90.5110), // primera lectura, a ~400 m
		punto(55, 14.5760, -90.4960), // ~8.8 km, 25 s despues
	}

	out := plausibles(ps, 70)
	if len(out) != 2 {
		t.Fatalf("deberian quedar 2 puntos y quedan %d", len(out))
	}
}

// Una lectura falsa no debe arrastrar a las buenas que vienen despues: se mide
// contra el ultimo punto ACEPTADO.
func TestPlausiblesNoPierdeLasBuenasTrasUnaFalsa(t *testing.T) {
	ps := []store.Punto{
		punto(0, 14.6360, -90.5090),
		punto(40, 14.5760, -90.4960),  // falsa
		punto(240, 14.6280, -90.5220), // buena: ~1.7 km del origen en 4 min
	}

	out := plausibles(ps, 70)
	if len(out) != 2 || out[1].Momento != ps[2].Momento {
		t.Fatalf("debia conservar la tercera tras descartar la falsa: %v", out)
	}
}

func TestPlausiblesConservaUnRecorridoNormal(t *testing.T) {
	ps := []store.Punto{
		punto(0, 14.6407, -90.5133),
		punto(204, 14.6280, -90.5220), // 1.7 km en 3.4 min: 30 km/h
	}
	if len(plausibles(ps, 70)) != 2 {
		t.Fatal("un tramo a 30 km/h es perfectamente posible")
	}
}
