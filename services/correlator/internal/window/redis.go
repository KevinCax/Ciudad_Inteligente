// Package window mantiene el estado transitorio de la correlacion en Redis:
// las ventanas temporales por zona, la idempotencia y la supresion de alertas.
package window

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Prefijos de las claves. Llevan el proposito en el nombre para que un
// SCAN sobre Redis sea legible sin consultar el codigo.
const (
	prefijoVisto = "corr:seen:"
	prefijoZona  = "corr:zone:"

	// La ventana de vehiculo se indexa por el SEUDONIMO de la placa, no por
	// la placa en claro. Dos razones. Un SCAN sobre Redis mostraria si no el
	// listado de matriculas que han circulado, convirtiendo el estado
	// transitorio de la correlacion en un fichero de vehiculos. Y el
	// seudonimo es el mismo que se archiva en PostgreSQL, de modo que una
	// alerta de persecucion se puede cruzar con el historico del vehiculo sin
	// que la placa en claro salga nunca de la LAN.
	prefijoVehiculo  = "corr:plate:"
	prefijoSupresion = "alert:fired:"
	prefijoActivas   = "alerts:active:"
)

// Store es el acceso a Redis.
type Store struct {
	rdb          *redis.Client
	ttlVisto     time.Duration
	ttlVentana   time.Duration
	ttlSupresion time.Duration
	ttlActivas   time.Duration
	ttlVehiculo  time.Duration
}

// Opciones de construccion del Store.
type Opciones struct {
	Addr         string
	Password     string
	DB           int
	TTLVisto     time.Duration
	TTLVentana   time.Duration
	TTLSupresion time.Duration

	// TTLVehiculo es mayor que TTLVentana a proposito: un vehiculo que huye
	// recorre varias zonas y la cadena de infracciones se extiende mas que
	// cualquier ventana de zona.
	TTLVehiculo time.Duration
}

// New abre la conexion a Redis y verifica que responda.
func New(ctx context.Context, o Opciones) (*Store, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         o.Addr,
		Password:     o.Password,
		DB:           o.DB,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis no responde en %s: %w", o.Addr, err)
	}

	return &Store{
		rdb:          rdb,
		ttlVisto:     o.TTLVisto,
		ttlVentana:   o.TTLVentana,
		ttlSupresion: o.TTLSupresion,
		ttlActivas:   o.TTLVentana,
		ttlVehiculo:  o.TTLVehiculo,
	}, nil
}

// Close cierra la conexion.
func (s *Store) Close() error { return s.rdb.Close() }

// Ping verifica que Redis siga respondiendo. Lo usa el health check.
func (s *Store) Ping(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }

// Resumen es lo que se guarda de cada evento en la ventana.
//
// No se guarda el evento completo: la correlacion solo necesita tipo, momento,
// identificador y payload. Guardar menos reduce la memoria de Redis y el coste
// de deserializar la ventana en cada evento.
type Resumen struct {
	EventID   string         `json:"event_id"`
	EventType string         `json:"event_type"`
	Timestamp string         `json:"timestamp"`
	Zone      string         `json:"zone"`
	Payload   map[string]any `json:"payload,omitempty"`

	// Lat y Lon son punteros porque 0.0 es una coordenada valida: con float64
	// y omitempty, un punto en el ecuador o en Greenwich se perderia al
	// serializar. Un resumen anterior a este campo los trae nulos, y el motor
	// debe tratar eso como "se desconoce", no como "esta en el golfo de
	// Guinea".
	Lat *float64 `json:"lat,omitempty"`
	Lon *float64 `json:"lon,omitempty"`
}

// Momento parsea el timestamp del resumen.
func (r *Resumen) Momento() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, r.Timestamp)
}

// MarcarVisto registra el evento como procesado y devuelve true si es la
// primera vez que se ve.
//
// Usa SET NX EX, que es una sola operacion atomica. La alternativa evidente
// —comprobar si la clave existe y despues escribirla— deja entre ambas
// llamadas un hueco por el que cabe otra instancia procesando el mismo
// evento: la idempotencia se romperia justo cuando mas hace falta, al escalar
// horizontalmente.
func (s *Store) MarcarVisto(ctx context.Context, eventID string) (bool, error) {
	nuevo, err := s.rdb.SetNX(ctx, prefijoVisto+eventID, "1", s.ttlVisto).Result()
	if err != nil {
		return false, fmt.Errorf("no se pudo marcar el evento como visto: %w", err)
	}
	return nuevo, nil
}

// AgregarEvento inserta el resumen en la ventana de su zona y poda lo viejo.
//
// La ventana es un sorted set cuyo score es el instante del evento en
// milisegundos. Eso permite consultar un rango temporal con ZRANGEBYSCORE en
// O(log n) y podar con ZREMRANGEBYSCORE, en lugar de leer y deserializar la
// lista completa en cada evento.
//
// Se usan milisegundos y no nanosegundos porque el score de Redis es un
// float64: los nanosegundos actuales necesitan mas bits de los que caben en
// su mantisa y se perderia precision.
func (s *Store) AgregarEvento(ctx context.Context, r *Resumen, momento time.Time) error {
	crudo, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("no se pudo serializar el resumen: %w", err)
	}

	clave := prefijoZona + r.Zone
	limite := momento.Add(-s.ttlVentana).UnixMilli()

	// Las tres operaciones van en un solo viaje a Redis.
	pipe := s.rdb.TxPipeline()
	pipe.ZAdd(ctx, clave, redis.Z{Score: float64(momento.UnixMilli()), Member: crudo})
	pipe.ZRemRangeByScore(ctx, clave, "-inf", fmt.Sprintf("(%d", limite))
	// El EXPIRE es solo una red de seguridad para zonas que dejan de recibir
	// eventos. La poda por score es lo que acota el tamano de verdad.
	//
	// Confiar el limite al EXPIRE seria un error: al refrescarse en cada
	// insercion, una zona con trafico continuo tendria una ventana que no
	// expira nunca y que crece sin limite.
	pipe.Expire(ctx, clave, s.ttlVentana*2)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("no se pudo agregar el evento a la ventana: %w", err)
	}
	return nil
}

// EventosEnVentana devuelve los eventos de una zona entre dos instantes.
func (s *Store) EventosEnVentana(ctx context.Context, zona string, desde, hasta time.Time) ([]Resumen, error) {
	return s.leerVentana(ctx, prefijoZona+zona, desde, hasta, "de "+zona)
}

// leerVentana lee un rango temporal de un sorted set de resumenes.
//
// La comparten la ventana de zona y la de vehiculo porque son la misma
// estructura con distinta clave: duplicar la lectura habria duplicado tambien
// el criterio de descarte de resumenes corruptos, que es el que decide si un
// dato ilegible detiene la correlacion o solo se omite.
func (s *Store) leerVentana(ctx context.Context, clave string, desde, hasta time.Time,
	queVentana string) ([]Resumen, error) {

	crudos, err := s.rdb.ZRangeByScore(ctx, clave, &redis.ZRangeBy{
		Min: fmt.Sprintf("%d", desde.UnixMilli()),
		Max: fmt.Sprintf("%d", hasta.UnixMilli()),
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la ventana %s: %w", queVentana, err)
	}

	resumenes := make([]Resumen, 0, len(crudos))
	for _, c := range crudos {
		var r Resumen
		if err := json.Unmarshal([]byte(c), &r); err != nil {
			// Un resumen corrupto no debe impedir correlacionar el resto.
			continue
		}
		resumenes = append(resumenes, r)
	}
	return resumenes, nil
}

// AgregarEventoVehiculo inserta el resumen en la ventana de un vehiculo.
//
// Es una segunda ventana, paralela a la de zona y con la misma forma. La de
// zona responde "que ha pasado aqui"; esta responde "por donde ha pasado este
// vehiculo", que es una pregunta que la de zona no puede contestar: un coche
// que huye cambia de zona justamente para dejar de aparecer en ella.
//
// token es el seudonimo HMAC de la placa, nunca la placa en claro.
func (s *Store) AgregarEventoVehiculo(ctx context.Context, token string, r *Resumen, momento time.Time) error {
	if token == "" {
		return nil
	}

	crudo, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("no se pudo serializar el resumen: %w", err)
	}

	clave := prefijoVehiculo + token
	limite := momento.Add(-s.ttlVehiculo).UnixMilli()

	pipe := s.rdb.TxPipeline()
	pipe.ZAdd(ctx, clave, redis.Z{Score: float64(momento.UnixMilli()), Member: crudo})
	pipe.ZRemRangeByScore(ctx, clave, "-inf", fmt.Sprintf("(%d", limite))
	pipe.Expire(ctx, clave, s.ttlVehiculo*2)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("no se pudo agregar el evento a la ventana del vehiculo: %w", err)
	}
	return nil
}

// EventosDeVehiculo devuelve las apariciones de un vehiculo entre dos
// instantes, en cualquier zona.
func (s *Store) EventosDeVehiculo(ctx context.Context, token string, desde, hasta time.Time) ([]Resumen, error) {
	if token == "" {
		return nil, nil
	}
	return s.leerVentana(ctx, prefijoVehiculo+token, desde, hasta, "del vehiculo")
}

// TamanoVentana devuelve cuantos eventos hay vigentes en una zona.
func (s *Store) TamanoVentana(ctx context.Context, zona string) (int64, error) {
	return s.rdb.ZCard(ctx, prefijoZona+zona).Result()
}

// ReservarAlerta devuelve true si esta alerta puede emitirse, y false si ya
// se emitio una equivalente que sigue vigente.
//
// Sin esto, cada evento nuevo que cae en la ventana vuelve a disparar la misma
// regla sobre la misma evidencia: cinco lecturas LPR seguidas en una zona
// generarian varias alertas de exceso de velocidad casi identicas, inflando la
// tabla y los tableros hasta que el operador dejara de mirarlos.
func (s *Store) ReservarAlerta(ctx context.Context, tipo, zona string) (bool, error) {
	clave := fmt.Sprintf("%s%s:%s", prefijoSupresion, tipo, zona)
	reservada, err := s.rdb.SetNX(ctx, clave, "1", s.ttlSupresion).Result()
	if err != nil {
		return false, fmt.Errorf("no se pudo reservar la alerta: %w", err)
	}
	return reservada, nil
}

// GuardarAlertaActiva publica la alerta para el endpoint /alerts/active.
func (s *Store) GuardarAlertaActiva(ctx context.Context, zona string, alerta []byte, momento time.Time) error {
	clave := prefijoActivas + zona

	pipe := s.rdb.TxPipeline()
	pipe.ZAdd(ctx, clave, redis.Z{Score: float64(momento.UnixMilli()), Member: alerta})
	pipe.ZRemRangeByScore(ctx, clave, "-inf",
		fmt.Sprintf("(%d", momento.Add(-s.ttlActivas).UnixMilli()))
	pipe.Expire(ctx, clave, s.ttlActivas*2)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("no se pudo guardar la alerta activa: %w", err)
	}
	return nil
}

// AlertasActivas devuelve las alertas vigentes de una zona, de la mas reciente
// a la mas antigua.
func (s *Store) AlertasActivas(ctx context.Context, zona string) ([]json.RawMessage, error) {
	crudas, err := s.rdb.ZRevRangeByScore(ctx, prefijoActivas+zona, &redis.ZRangeBy{
		Min: "-inf",
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("no se pudieron leer las alertas activas de %s: %w", zona, err)
	}

	alertas := make([]json.RawMessage, 0, len(crudas))
	for _, c := range crudas {
		alertas = append(alertas, json.RawMessage(c))
	}
	return alertas, nil
}

// ZonasActivas lista las zonas con ventana vigente. Lo usa la metrica de
// tamano de ventana.
//
// Usa SCAN y no KEYS: KEYS bloquea el servidor entero mientras recorre el
// espacio de claves, lo que en produccion basta para tumbar la latencia de
// todos los demas clientes.
func (s *Store) ZonasActivas(ctx context.Context) ([]string, error) {
	var zonas []string
	var cursor uint64

	for {
		claves, siguiente, err := s.rdb.Scan(ctx, cursor, prefijoZona+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("no se pudieron listar las zonas: %w", err)
		}
		for _, c := range claves {
			zonas = append(zonas, c[len(prefijoZona):])
		}
		if siguiente == 0 {
			break
		}
		cursor = siguiente
	}
	return zonas, nil
}
