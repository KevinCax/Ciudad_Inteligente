// Package dedupe rechaza eventos repetidos usando Redis.
//
// Antes, la deteccion de duplicados la daba la clave primaria de PostgreSQL al
// insertar. Al sacar la persistencia del camino critico, el ingestor ya no
// escribe en la base, asi que necesita su propia memoria de lo que ya vio.
//
// Redis esta en la LAN, de modo que la comprobacion cuesta cerca de un
// milisegundo y no reintroduce la dependencia del enlace a internet que
// justamente se queria eliminar.
package dedupe

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const prefijo = "ingest:seen:"

// Store recuerda que eventos ya se aceptaron.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// Opciones de construccion.
type Opciones struct {
	Addr     string
	Password string
	DB       int
	TTL      time.Duration
}

// New abre la conexion y verifica que Redis responda.
func New(ctx context.Context, o Opciones) (*Store, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         o.Addr,
		Password:     o.Password,
		DB:           o.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     20,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis no responde en %s: %w", o.Addr, err)
	}

	return &Store{rdb: rdb, ttl: o.TTL}, nil
}

// Close cierra la conexion.
func (s *Store) Close() error { return s.rdb.Close() }

// Ping verifica que Redis siga respondiendo.
func (s *Store) Ping(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }

// Reservar devuelve true si el evento es nuevo y queda registrado.
//
// Es un SET NX EX: una sola operacion atomica. Comprobar y despues escribir
// dejaria una ventana de carrera por la que dos peticiones simultaneas con el
// mismo event_id pasarian ambas.
func (s *Store) Reservar(ctx context.Context, eventID string) (bool, error) {
	nuevo, err := s.rdb.SetNX(ctx, prefijo+eventID, "1", s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("no se pudo reservar el event_id: %w", err)
	}
	return nuevo, nil
}

// Liberar deshace una reserva.
//
// Se usa cuando la publicacion en Kafka falla despues de haber reservado: sin
// esto, el reintento del cliente chocaria contra su propia reserva y recibiria
// un 409 por un evento que en realidad nunca se publico.
func (s *Store) Liberar(ctx context.Context, eventID string) error {
	return s.rdb.Del(ctx, prefijo+eventID).Err()
}
