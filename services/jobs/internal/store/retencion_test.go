package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// nuevoAlmacenDePrueba abre un Store sobre un esquema temporal de la base que
// indique DATABASE_URL, con las tablas reales de deploy/sql.
//
// Un esquema propio por prueba hace que nunca se toquen los datos de quien tenga
// la base levantada: se crea al empezar y se elimina al terminar. Sin base
// disponible la prueba se omite en lugar de fallar, igual que las del
// planificador sin Redis: el SQL de la purga solo se puede comprobar contra el
// motor real, no contra un doble.
func nuevoAlmacenDePrueba(t *testing.T) *Store {
	t.Helper()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL no esta definida: se omite la prueba contra PostgreSQL")
	}

	ctx := context.Background()
	esquema := fmt.Sprintf("test_retencion_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no se pudo abrir PostgreSQL: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("PostgreSQL no responde: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+esquema); err != nil {
		admin.Close()
		t.Fatalf("no se pudo crear el esquema temporal: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		admin.Close()
		t.Fatalf("DATABASE_URL invalida: %v", err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = esquema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatalf("no se pudo abrir el pool de prueba: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+esquema+" CASCADE")
		admin.Close()
	})

	// Las mismas migraciones que aplica postgres-migrate, en orden.
	archivos, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "deploy", "sql", "*.sql"))
	if err != nil || len(archivos) == 0 {
		t.Fatalf("no se encontraron las migraciones de deploy/sql: %v", err)
	}
	sort.Strings(archivos)
	for _, a := range archivos {
		sql, err := os.ReadFile(a)
		if err != nil {
			t.Fatalf("no se pudo leer %s: %v", a, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("no se pudo aplicar %s: %v", filepath.Base(a), err)
		}
	}

	return &Store{pool: pool}
}

func contar(t *testing.T, s *Store, tabla string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+tabla).Scan(&n); err != nil {
		t.Fatalf("no se pudo contar %s: %v", tabla, err)
	}
	return n
}

func existe(t *testing.T, s *Store, tabla, columna, id string) bool {
	t.Helper()
	var n int
	sql := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1::uuid", tabla, columna)
	if err := s.pool.QueryRow(context.Background(), sql, id).Scan(&n); err != nil {
		t.Fatalf("no se pudo consultar %s: %v", tabla, err)
	}
	return n > 0
}

// Identificadores fijos: facilitan leer un fallo.
const (
	alertaReciente = "00000000-0000-0000-0000-0000000000a1"
	alertaDe400    = "00000000-0000-0000-0000-0000000000a2"
	alertaDe800    = "00000000-0000-0000-0000-0000000000a3"
	alertaHuerfana = "00000000-0000-0000-0000-0000000000a4"
)

// TestRetencionDeRecorridosYAvisos comprueba que los recorridos guardados siguen
// el plazo de las coordenadas y los avisos el del borrado.
//
// El caso que motiva la prueba es una trayectoria que sobrevivia indefinidamente
// al evento del que salia: la retencion solo actuaba sobre events.
func TestRetencionDeRecorridosYAvisos(t *testing.T) {
	s := nuevoAlmacenDePrueba(t)
	ctx := context.Background()

	// Tres alertas de edades distintas. La cuarta no existe en alerts: su ruta es
	// una fila huerfana que solo se puede fechar por su calculo.
	for id, dias := range map[string]int{alertaReciente: 10, alertaDe400: 400, alertaDe800: 800} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO alerts (alert_id, type, zone, created_at)
			 VALUES ($1::uuid, 'possible_robbery', 'zona_1', now() - make_interval(days => $2))`,
			id, dias); err != nil {
			t.Fatalf("no se pudo insertar la alerta %s: %v", id, err)
		}
	}

	// Rutas. La de la alerta de 400 dias se recalculo ayer: su fecha de calculo es
	// reciente, y aun asi debe borrarse porque el incidente es antiguo.
	rutas := []struct {
		alerta  string
		calculo string
	}{
		{alertaReciente, "now()"},
		{alertaDe400, "now() - interval '1 day'"},
		{alertaDe800, "now() - interval '800 days'"},
		{alertaHuerfana, "now() - interval '500 days'"},
	}
	for _, r := range rutas {
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(
			`INSERT INTO alert_routes (alert_id, estado, geometria, calculado_en)
			 VALUES ($1::uuid, 'calculada', '[[-90.5, 14.6], [-90.51, 14.61]]'::jsonb, %s)`, r.calculo),
			r.alerta); err != nil {
			t.Fatalf("no se pudo insertar la ruta de %s: %v", r.alerta, err)
		}
	}

	// Avisos: uno reciente, uno de 400 dias (dentro del plazo de borrado) y otro de
	// 800 (fuera).
	avisos := []struct {
		alerta string
		dias   int
	}{{alertaReciente, 10}, {alertaDe400, 400}, {alertaDe800, 800}}
	for i, a := range avisos {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO notifications (notification_id, alert_id, alert_type, zone, organismo, canal, estado, creado_en)
			 VALUES (gen_random_uuid(), $1::uuid, 'possible_robbery', 'zona_1', 'policia', 'simulado', 'entregado',
			         now() - make_interval(days => $2))`,
			a.alerta, a.dias); err != nil {
			t.Fatalf("no se pudo insertar el aviso %d: %v", i, err)
		}
	}

	politica := PoliticaRetencion{DiasPlaca: 90, DiasCoordenadas: 365, DiasBorrado: 730}

	res, err := s.AplicarRetencion(ctx, politica)
	if err != nil {
		t.Fatalf("AplicarRetencion fallo: %v", err)
	}

	// Recorridos: se borran los de incidentes de mas de 365 dias (400 y 800) y la
	// huerfana de 500; queda solo el reciente.
	if res.RecorridosBorrados != 3 {
		t.Errorf("recorridos borrados: se esperaban 3 y fueron %d", res.RecorridosBorrados)
	}
	if !existe(t, s, "alert_routes", "alert_id", alertaReciente) {
		t.Error("el recorrido reciente no debia borrarse")
	}
	for _, id := range []string{alertaDe400, alertaDe800, alertaHuerfana} {
		if existe(t, s, "alert_routes", "alert_id", id) {
			t.Errorf("el recorrido %s debia borrarse", id)
		}
	}

	// Avisos: solo el de 800 dias supera el plazo de borrado.
	if res.AvisosBorrados != 1 {
		t.Errorf("avisos borrados: se esperaba 1 y fue %d", res.AvisosBorrados)
	}
	if n := contar(t, s, "notifications"); n != 2 {
		t.Errorf("deben quedar 2 avisos y quedan %d", n)
	}

	// Las alertas no se tocan: no llevan coordenadas ni placa.
	if n := contar(t, s, "alerts"); n != 3 {
		t.Errorf("las alertas no deben purgarse: quedan %d de 3", n)
	}

	// Idempotencia: repetir la purga no cambia nada.
	again, err := s.AplicarRetencion(ctx, politica)
	if err != nil {
		t.Fatalf("la segunda ejecucion fallo: %v", err)
	}
	if again.RecorridosBorrados != 0 || again.AvisosBorrados != 0 {
		t.Errorf("repetir la purga no debe borrar mas: %+v", again)
	}
}

// TestRetencionNoBorraNadaDentroDePlazo es la prueba negativa: si todo es
// reciente, la purga no debe tocar nada. Una purga que borra de mas es peor que
// una que borra de menos, porque el borrado es irreversible.
func TestRetencionNoBorraNadaDentroDePlazo(t *testing.T) {
	s := nuevoAlmacenDePrueba(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO alerts (alert_id, type, zone, created_at) VALUES ($1::uuid, 'fire', 'zona_3', now())`,
		alertaReciente); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO alert_routes (alert_id, estado, geometria) VALUES ($1::uuid, 'calculada', '[[-90.5, 14.6]]'::jsonb)`,
		alertaReciente); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO notifications (notification_id, alert_id, alert_type, zone, organismo, canal, estado)
		 VALUES (gen_random_uuid(), $1::uuid, 'fire', 'zona_3', 'bomberos', 'simulado', 'entregado')`,
		alertaReciente); err != nil {
		t.Fatal(err)
	}

	res, err := s.AplicarRetencion(ctx, PoliticaRetencion{DiasPlaca: 90, DiasCoordenadas: 365, DiasBorrado: 730})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total() != 0 {
		t.Errorf("no debia tocarse nada y se afectaron %d filas: %+v", res.Total(), res)
	}
	for _, tabla := range []string{"alerts", "alert_routes", "notifications"} {
		if n := contar(t, s, tabla); n != 1 {
			t.Errorf("%s: se esperaba 1 fila y hay %d", tabla, n)
		}
	}
}

func TestEsquemaDePruebaEsIndependiente(t *testing.T) {
	s := nuevoAlmacenDePrueba(t)
	var esquema string
	if err := s.pool.QueryRow(context.Background(), "SELECT current_schema()").Scan(&esquema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(esquema, "test_retencion_") {
		t.Fatalf("la prueba debe correr en un esquema propio y corre en %q", esquema)
	}
}
