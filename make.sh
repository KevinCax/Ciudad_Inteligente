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
    ( cd test/load && "$GO" run . -escenario "${2:-sostenido}" )
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
    echo "Despliegue:  up up-cloud down logs config"
    echo "Documentos:  adr"
    echo "Pruebas:     load [sostenido|rafaga|alertas|bulk|enunciado]"
    ;;
esac
