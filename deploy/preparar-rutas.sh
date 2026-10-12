#!/usr/bin/env bash
# Descarga el mapa de Guatemala y lo preprocesa para OSRM.
#
# Hay que ejecutarlo UNA vez antes de levantar el servicio de rutas. No forma
# parte de "up" a proposito: son 131 MB de descarga y varios minutos de
# calculo, y repetirlo en cada arranque haria que levantar el sistema para una
# demostracion dependiera de la red.
#
# Es idempotente: si los indices ya existen, no hace nada. Para rehacerlos,
# borre deploy/osrm-data.
#
# Los datos viven en deploy/osrm-data, que esta en .gitignore: un mapa de
# OpenStreetMap no se versiona, se descarga.
set -euo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATOS="$RAIZ/deploy/osrm-data"
PBF="guatemala-latest.osm.pbf"
BASE="guatemala-latest"
URL="https://download.geofabrik.de/central-america/$PBF"
IMAGEN="osrm/osrm-backend:latest"

mkdir -p "$DATOS"

# Rutas y Git Bash en Windows: hay dos conversiones en juego y conviene no
# mezclarlas.
#
# Docker necesita que /data NO se convierta, o Git Bash lo reescribe como una
# ruta de Windows antes de pasarlo al contenedor y el montaje falla. Para eso
# esta MSYS_NO_PATHCONV, pero aplicado SOLO a docker: si se exporta global,
# curl —que es un binario de Windows— recibe /c/Users/... sin traducir y no
# encuentra el directorio. Costo un intento descubrirlo.
#
# El lado del host del montaje si debe ir en formato Windows, de ahi cygpath.
DATOS_HOST="$(cygpath -w "$DATOS" 2>/dev/null || echo "$DATOS")"

osrm () {
  MSYS_NO_PATHCONV=1 docker run --rm -v "$DATOS_HOST:/data" "$IMAGEN" "$@"
}

if [ -f "$DATOS/$BASE.osrm.mldgr" ]; then
  echo "  los indices ya estan preparados: nada que hacer"
  echo "  (para rehacerlos: rm -rf deploy/osrm-data)"
  exit 0
fi

# Se comprueba el tamano y no solo la existencia: una descarga interrumpida
# deja un archivo a medias que osrm-extract acepta y luego falla con un error
# de formato que no menciona la descarga.
if [ ! -s "$DATOS/$PBF" ] || [ "$(stat -c%s "$DATOS/$PBF" 2>/dev/null || echo 0)" -lt 100000000 ]; then
  echo "  descargando el mapa de Guatemala (unos 131 MB)..."
  rm -f "$DATOS/$PBF"
  curl -fL --progress-bar -o "$DATOS/$PBF" "$URL"

  if [ ! -s "$DATOS/$PBF" ]; then
    echo "ERROR: la descarga no dejo ningun archivo en $DATOS/$PBF" >&2
    exit 1
  fi
  echo "  descargado: $(du -h "$DATOS/$PBF" | cut -f1)"
else
  echo "  el mapa ya estaba descargado"
fi

# Las tres fases del algoritmo MLD (multi-level Dijkstra). Se usa MLD y no CH
# porque permite recalcular los pesos sin rehacer todo el indice, que es lo que
# haria falta el dia que se quisieran incorporar cortes de calle o trafico.
echo "  1/3 extrayendo el grafo de calles (perfil de automovil)..."
osrm osrm-extract -p /opt/car.lua "/data/$PBF"

echo "  2/3 particionando..."
osrm osrm-partition "/data/$BASE.osrm"

echo "  3/3 calculando pesos..."
osrm osrm-customize "/data/$BASE.osrm"

echo ""
echo "  listo. Levante el servicio con:"
echo "    docker compose -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml --env-file deploy/.env up -d osrm"
