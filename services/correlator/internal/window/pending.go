package window

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	prefijoPendiente = "corr:pending:"
	indicePendientes = "corr:pending:index"
)

// Pendiente es una coincidencia parcial a la espera de completarse.
//
// El enunciado incluye los falsos positivos como caso de uso a simular
// (seccion 7) y su analisis como merito adicional (seccion 10.3). Sin este
// registro, un boton de panico que nunca encuentra su evento complementario
// simplemente no genera nada y el sistema no puede decir cuantas veces paso:
// no hay forma de medir la tasa de falsos positivos.
type Pendiente struct {
	Regla      string
	Zona       string
	Disparador string
	Faltante   string
	Expira     time.Time
}

// RegistrarParcial anota una coincidencia parcial con su fecha limite.
//
// Es idempotente por disparador: si el mismo evento vuelve a quedar parcial en
// evaluaciones sucesivas, no se cuenta dos veces.
func (s *Store) RegistrarParcial(ctx context.Context, p Pendiente) error {
	clave := clavePendiente(p.Regla, p.Zona)

	pipe := s.rdb.TxPipeline()
	pipe.ZAdd(ctx, clave, redis.Z{
		Score:  float64(p.Expira.UnixMilli()),
		Member: p.Disparador + "|" + p.Faltante,
	})
	pipe.Expire(ctx, clave, s.ttlVentana*3)
	pipe.SAdd(ctx, indicePendientes, clave)
	pipe.Expire(ctx, indicePendientes, s.ttlVentana*3)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("no se pudo registrar la coincidencia parcial: %w", err)
	}
	return nil
}

// ResolverParciales descarta las parciales de una regla y zona porque la
// alerta acabo generandose: dejaron de ser candidatas a falso positivo.
func (s *Store) ResolverParciales(ctx context.Context, regla, zona string) error {
	clave := clavePendiente(regla, zona)

	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, clave)
	pipe.SRem(ctx, indicePendientes, clave)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("no se pudieron resolver las parciales: %w", err)
	}
	return nil
}

// BarrerExpiradas devuelve y elimina las parciales cuya ventana ya vencio.
//
// Son los falsos positivos confirmados: la regla encontro su disparador, la
// ventana se cerro y el evento complementario nunca llego.
func (s *Store) BarrerExpiradas(ctx context.Context, ahora time.Time) ([]Pendiente, error) {
	claves, err := s.rdb.SMembers(ctx, indicePendientes).Result()
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer el indice de parciales: %w", err)
	}

	limite := fmt.Sprintf("%d", ahora.UnixMilli())
	var expiradas []Pendiente

	for _, clave := range claves {
		miembros, err := s.rdb.ZRangeByScore(ctx, clave, &redis.ZRangeBy{
			Min: "-inf",
			Max: limite,
		}).Result()
		if err != nil {
			continue
		}

		if len(miembros) == 0 {
			// Limpieza del indice: la clave ya no tiene nada vigente.
			if n, err := s.rdb.ZCard(ctx, clave).Result(); err == nil && n == 0 {
				s.rdb.SRem(ctx, indicePendientes, clave)
			}
			continue
		}

		regla, zona := partesDeClave(clave)
		for _, m := range miembros {
			disparador, faltante := dividir(m)
			expiradas = append(expiradas, Pendiente{
				Regla:      regla,
				Zona:       zona,
				Disparador: disparador,
				Faltante:   faltante,
				Expira:     ahora,
			})
		}

		// Se eliminan para no contarlas de nuevo en el siguiente barrido.
		s.rdb.ZRemRangeByScore(ctx, clave, "-inf", limite)
	}

	return expiradas, nil
}

func clavePendiente(regla, zona string) string {
	return prefijoPendiente + regla + ":" + zona
}

// partesDeClave deshace clavePendiente. La zona puede contener ":" aunque no
// es lo habitual, asi que se corta solo por el primer separador tras la regla.
func partesDeClave(clave string) (regla, zona string) {
	resto := strings.TrimPrefix(clave, prefijoPendiente)
	i := strings.Index(resto, ":")
	if i < 0 {
		return resto, ""
	}
	return resto[:i], resto[i+1:]
}

func dividir(miembro string) (disparador, faltante string) {
	i := strings.Index(miembro, "|")
	if i < 0 {
		return miembro, ""
	}
	return miembro[:i], miembro[i+1:]
}
