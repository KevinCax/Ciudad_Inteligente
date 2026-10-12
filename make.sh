#!/usr/bin/env bash
# Equivalente al Makefile para entornos sin make (Windows + Git Bash).
# Uso: ./make.sh <target>    Ej: ./make.sh check
#
# Los targets son los mismos que los del Makefile; si modificas uno, actualiza
# el otro. En Linux/macOS usa make directamente.

set -euo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$RAIZ"

SERVICIOS=(ingestor persister indexer correlator jobs notifier)

# Localiza el binario de Go aunque no este en el PATH de la terminal actual
# (caso tipico en Windows tras instalar Go sin reiniciar la sesion).
if command -v go >/dev/null 2>&1; then
  GO="go"
elif [ -x "/c/Program Files/Go/bin/go.exe" ]; then
  GO="/c/Program Files/Go/bin/go.exe"
else
  echo "ERROR: no se encontro el binario de Go" >&2
  exit 1
fi

# Las listas son la fuente de verdad de quien recibe cada contrato. Antes
# estaban escritas copia por copia y check_contracts solo verificaba 3 de las
# 7: las de persister e indexer podian desincronizarse sin que nada lo
# detectara. Mantener estas listas iguales a las del Makefile.
ESQUEMA_EVENTO="contracts/canonical-event/v1.0/schema.json"
ESQUEMA_ALERTA="contracts/correlated-alert/v1.0/schema.json"

COPIAS_EVENTO=(ingestor correlator persister indexer)
COPIAS_ALERTA=(correlator persister indexer notifier)

sync_contracts() {
  local s
  for s in "${COPIAS_EVENTO[@]}"; do
    cp "$ESQUEMA_EVENTO" "services/$s/internal/canonical/schema.json"
  done
  for s in "${COPIAS_ALERTA[@]}"; do
    cp "$ESQUEMA_ALERTA" "services/$s/internal/canonical/alert-schema.json"
  done
  echo "contratos sincronizados"
}

check_contracts() {
  local fallo=0 s
  for s in "${COPIAS_EVENTO[@]}"; do
    diff -q "$ESQUEMA_EVENTO" "services/$s/internal/canonical/schema.json" >/dev/null       || { echo "ERROR: la copia del contrato de evento en $s difiere"; fallo=1; }
  done
  for s in "${COPIAS_ALERTA[@]}"; do
    diff -q "$ESQUEMA_ALERTA" "services/$s/internal/canonical/alert-schema.json" >/dev/null       || { echo "ERROR: la copia del contrato de alerta en $s difiere"; fallo=1; }
  done
  [ "$fallo" -eq 0 ] || exit 1
  echo "contratos sincronizados correctamente (todas las copias verificadas)"
}

por_servicio() {
  local accion="$1"; shift
  for s in "${SERVICIOS[@]}"; do
    echo "==> $accion $s"
    ( cd "services/$s" && "$GO" "$@" )
  done
}

case "${1:-help}" in
  sync-contracts)  sync_contracts ;;
  check-contracts) check_contracts ;;
  tidy)            por_servicio tidy mod tidy ;;
  vet)             por_servicio vet vet ./... ;;
  fmt)             por_servicio fmt fmt ./... ;;
  test)            por_servicio test test ./... ;;
  build)
    mkdir -p bin
    for s in "${SERVICIOS[@]}"; do
      echo "==> build $s"
      ( cd "services/$s" && "$GO" build -o "../../bin/$s" "./cmd/$s" )
    done
    ;;
  docker)
    for s in "${SERVICIOS[@]}"; do
      echo "==> docker build $s"
      docker build -t "ciudad-inteligente/$s:latest" "services/$s"
    done
    ;;

  up)
    # Capa edge con la base local de desarrollo.
    docker compose -f deploy/docker-compose.edge.yml                    -f deploy/docker-compose.dev.yml                    --env-file deploy/.env up -d --build
    ;;

  up-cloud)
    # La red ciudad_lan la crea el compose de edge, asi que edge va primero.
    docker compose -f deploy/docker-compose.cloud.yml                    --env-file deploy/.env up -d
    ;;

  verificar)
    # Comprueba que lo correlacionado sea coherente y que el tablero lo
    # muestre. No genera carga: se lanza DESPUES de un escenario.
    #
    # Las consultas van por la API de Grafana contra el mismo origen de datos
    # que usan los paneles, para que "pasa la verificacion" y "el tablero
    # ensena algo" no puedan separarse.
    # OJO con el nombre: PWD es una variable especial del shell y el "cd" del
    # subshell de abajo la reescribe con la ruta. Costo un 401 descubrirlo.
    USR=$(grep -E '^GRAFANA_USER=' deploy/.env 2>/dev/null | cut -d= -f2-)
    CLAVE=$(grep -E '^GRAFANA_PASSWORD=' deploy/.env 2>/dev/null | cut -d= -f2-)
    VENT=$(grep -E '^RULE_VENTANA_PERSECUCION=' deploy/.env 2>/dev/null | cut -d= -f2-)
    # El tope del sistema se pasa para CONTRASTARLO, no para usarlo como
    # expectativa: la verificacion lleva su propia referencia medida.
    TOPE=$(grep -E '^RULE_VELOCIDAD_MAXIMA_PLAUSIBLE=' deploy/.env 2>/dev/null | cut -d= -f2-)
    ( cd test/load && "$GO" run . -escenario verificar         -grafana-usuario "${USR:-admin}" -grafana-clave "$CLAVE"         -tableros ../../deploy/grafana/provisioning/dashboards         -ventana "${VENT:-15m}" -umbral-sistema "${TOPE:-0}" )
    ;;

  rutas-preparar)
    # Descarga el mapa de Guatemala y construye los indices de OSRM. Una vez.
    # No esta en "up" a proposito: son 131 MB y varios minutos, y meterlo en el
    # arranque haria que levantar el sistema dependiera de la red.
    bash deploy/preparar-rutas.sh
    ;;

  estado)
    # Responde de un vistazo "¿esta todo listo?".
    #
    # Existe porque el indexer tarda en arrancar: depende de Elasticsearch, que
    # necesita cerca de minuto y medio, y hasta entonces se cae y Docker lo
    # reintenta. Visto en un tablero parece averiado cuando solo esta
    # esperando, y sin algo asi no hay forma rapida de distinguirlo.
    echo "Servicios"
    # El "|| true" de cada sustitucion no es decorativo: el script corre con
    # set -e, y un servicio caido hace que curl salga con 7. Sin eso, la
    # comprobacion se mata justo en el primer servicio con problemas, que es
    # exactamente cuando hace falta ver el resto.
    salud () {
      printf "  %-12s " "$1"
      codigo=$(curl -s -o /dev/null -m 3 -w "%{http_code}" "$2" 2>/dev/null || true)
      if [ "$codigo" = "200" ]; then
        echo "listo"
      else
        reinicios=$(docker inspect "ciudad-$1" --format '{{.RestartCount}}' 2>/dev/null || true)
        estado=$(docker inspect "ciudad-$1" --format '{{.State.Status}}' 2>/dev/null || true)
        if [ -z "$estado" ]; then
          echo "NO EXISTE el contenedor      -> ./make.sh up"
        elif [ "$estado" != "running" ]; then
          echo "contenedor $estado"
        else
          echo "arrancando (HTTP ${codigo:-sin respuesta}, ${reinicios:-0} reintentos)"
        fi
      fi
    }
    salud ingestor   http://localhost:8000/events/health
    puerto_corr=$(grep -E '^CORRELATOR_HOST_PORT=' deploy/.env 2>/dev/null | cut -d= -f2- || true)
    salud correlator "http://localhost:${puerto_corr:-8001}/health"
    salud persister  http://localhost:8002/health
    salud indexer    http://localhost:8003/health
    salud jobs       http://localhost:8004/health
    salud notifier   http://localhost:8005/health

    echo ""
    echo "Infraestructura"
    infra () {
      printf "  %-14s " "$1"
      if curl -s -o /dev/null -m 3 "$2" 2>/dev/null; then echo "listo"; else echo "no responde"; fi
    }
    puerto_osrm=$(grep -E '^OSRM_HOST_PORT=' deploy/.env 2>/dev/null | cut -d= -f2- || true)
    infra osrm          "http://localhost:${puerto_osrm:-5001}/route/v1/driving/-90.5133,14.6407;-90.5220,14.6280"
    infra elasticsearch http://localhost:9200/_cluster/health
    infra prometheus    http://localhost:9090/-/ready
    infra grafana       http://localhost:3000/api/health
    infra kibana        http://localhost:5601/api/status

    echo ""
    echo "Lo que ve Prometheus"
    curl -s -m 3 http://localhost:9090/api/v1/targets 2>/dev/null | python -c "
import json,sys
try: d=json.load(sys.stdin)['data']['activeTargets']
except Exception: print('  Prometheus no responde todavia'); raise SystemExit
caidos=[t['labels'].get('job') for t in d if t['health']!='up']
print('  %d de %d objetivos activos%s' % (len(d)-len(caidos), len(d),
      '' if not caidos else '   caidos: '+', '.join(caidos)))
" 2>/dev/null || echo "  Prometheus no responde todavia"
    echo ""
    echo "Si algo sale 'arrancando', esperar 30 s y repetir. Elasticsearch"
    echo "tarda cerca de minuto y medio, y el indexer no arranca sin el."
    ;;

  down)
    docker compose -f deploy/docker-compose.cloud.yml down 2>/dev/null || true
    docker compose -f deploy/docker-compose.edge.yml                    -f deploy/docker-compose.dev.yml down
    ;;

  logs)
    docker compose -f deploy/docker-compose.edge.yml                    -f deploy/docker-compose.dev.yml logs -f ingestor correlator
    ;;

  load)
    # Prueba de carga. Pasa el escenario como segundo argumento:
    #   ./make.sh load rafaga
    #   ./make.sh load alertas
    #   ./make.sh load bulk
    #   ./make.sh load enunciado
    #   ./make.sh load tormenta        (100 alertas a la vez)
    #   ./make.sh load persecucion     (cadena de un mismo vehiculo)
    #   ./make.sh load tormenta 250    (el tercer argumento cambia la cantidad)
    # La clave del ingestor se lee de deploy/.env para que los escenarios
    # sigan funcionando con la autenticacion activada. Sin esto, activar la
    # clave rompe todas las pruebas de carga y no es evidente por que.
    CLAVE=$(grep -E '^INGESTOR_API_KEY=' deploy/.env 2>/dev/null | cut -d= -f2-)
    ( cd test/load && "$GO" run . -escenario "${2:-sostenido}" -api-key "$CLAVE" ${3:+-alertas "$3"} )
    ;;

  config)
    # Valida la sintaxis de los compose sin necesidad del demonio de Docker.
    docker compose -f deploy/docker-compose.edge.yml                    -f deploy/docker-compose.dev.yml --env-file deploy/.env config --quiet       && echo "edge + dev: sintaxis correcta"
    docker compose -f deploy/docker-compose.cloud.yml                    --env-file deploy/.env config --quiet       && echo "cloud: sintaxis correcta"
    ;;
  adr)
    # Consolida los ADR en el documento anexo que pide el enunciado (4.2).
    python docs/consolidar-adr.py
    ;;
  check)
    check_contracts
    por_servicio fmt fmt ./...
    por_servicio vet vet ./...
    por_servicio test test ./...
    echo ""
    echo "check completo: contratos, formato, vet y tests en verde"
    ;;
  help|*)
    sed -n '4,6p' "$0"
    echo ""
    echo "Codigo:      sync-contracts check-contracts tidy fmt vet test build docker check"
    echo "Despliegue:  up up-cloud estado down logs config rutas-preparar"
    echo "Documentos:  adr"
    echo "Pruebas:     load [sostenido|rafaga|alertas|bulk|enunciado|tormenta [N]|persecucion]
             verificar   (coherencia de extremo a extremo + lo que vera el tablero)"
    ;;
esac
