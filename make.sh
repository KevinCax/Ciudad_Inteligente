#!/usr/bin/env bash
# Equivalente al Makefile para entornos sin make (Windows + Git Bash).
# Uso: ./make.sh <target>    Ej: ./make.sh check
#
# Los targets son los mismos que los del Makefile; si modificas uno, actualiza
# el otro. En Linux/macOS usa make directamente.

set -euo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$RAIZ"

SERVICIOS=(ingestor persister indexer correlator jobs)

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

sync_contracts() {
  cp contracts/canonical-event/v1.0/schema.json services/ingestor/internal/canonical/schema.json
  cp contracts/canonical-event/v1.0/schema.json services/correlator/internal/canonical/schema.json
  cp contracts/canonical-event/v1.0/schema.json services/persister/internal/canonical/schema.json
  cp contracts/correlated-alert/v1.0/schema.json services/correlator/internal/canonical/alert-schema.json
  cp contracts/canonical-event/v1.0/schema.json services/indexer/internal/canonical/schema.json
  cp contracts/correlated-alert/v1.0/schema.json services/persister/internal/canonical/alert-schema.json
  cp contracts/correlated-alert/v1.0/schema.json services/indexer/internal/canonical/alert-schema.json
  echo "contratos sincronizados"
}

check_contracts() {
  local fallo=0
  diff -q contracts/canonical-event/v1.0/schema.json \
          services/ingestor/internal/canonical/schema.json >/dev/null \
    || { echo "ERROR: la copia del ingestor difiere del contrato"; fallo=1; }
  diff -q contracts/canonical-event/v1.0/schema.json \
          services/correlator/internal/canonical/schema.json >/dev/null \
    || { echo "ERROR: la copia del correlator difiere del contrato"; fallo=1; }
  diff -q contracts/correlated-alert/v1.0/schema.json \
          services/correlator/internal/canonical/alert-schema.json >/dev/null \
    || { echo "ERROR: la copia del contrato de alerta difiere"; fallo=1; }
  [ "$fallo" -eq 0 ] || exit 1
  echo "contratos sincronizados correctamente"
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

    echo ""
    echo "Infraestructura"
    infra () {
      printf "  %-14s " "$1"
      if curl -s -o /dev/null -m 3 "$2" 2>/dev/null; then echo "listo"; else echo "no responde"; fi
    }
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
    echo "Despliegue:  up up-cloud estado down logs config"
    echo "Documentos:  adr"
    echo "Pruebas:     load [sostenido|rafaga|alertas|bulk|enunciado|tormenta [N]]"
    ;;
esac
