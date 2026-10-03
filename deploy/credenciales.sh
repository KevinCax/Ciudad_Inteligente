#!/usr/bin/env bash
# Carga las credenciales de deploy/.env en la sesion actual de la terminal.
#
#   source deploy/credenciales.sh
#
# Existe para que la demostracion no dependa de copiar a mano dos ordenes con
# $( ) y tuberias dentro: es el paso mas facil de romper al pegarlo, y falla
# con un error de sintaxis que no dice nada util.
#
# OJO: hay que invocarlo con "source" (o con un punto). Ejecutarlo como
# ./deploy/credenciales.sh no sirve: las variables se perderian al terminar.

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  echo "Hay que cargarlo en la sesion, no ejecutarlo:" >&2
  echo "    source deploy/credenciales.sh" >&2
  exit 1
fi

_env_ruta="deploy/.env"
if [ ! -f "$_env_ruta" ]; then
  # Por si lo cargan estando dentro de deploy/
  if [ -f ".env" ]; then
    _env_ruta=".env"
  else
    echo "no encuentro deploy/.env" >&2
    echo "  ejecuta esto desde la raiz del proyecto, o copia deploy/.env.example" >&2
    return 1
  fi
fi

_leer() {
  grep -E "^$1=" "$_env_ruta" | head -1 | cut -d= -f2-
}

export KEY="$(_leer INGESTOR_API_KEY)"
export TOKEN="$(_leer CORRELATOR_API_TOKEN)"
export TOKEN_JOBS="$(_leer JOBS_API_TOKEN)"
export CORRELATOR="http://localhost:$(_leer CORRELATOR_HOST_PORT)"
[ "$CORRELATOR" = "http://localhost:" ] && export CORRELATOR="http://localhost:8001"

unset -f _leer
unset _env_ruta

_mostrar() {
  if [ -z "$2" ]; then
    printf "  %-12s SIN VALOR en deploy/.env\n" "$1"
  else
    printf "  %-12s %s...\n" "$1" "${2:0:8}"
  fi
}

echo "credenciales cargadas:"
_mostrar "KEY"    "$KEY"
_mostrar "TOKEN"  "$TOKEN"
printf "  %-12s %s\n" "CORRELATOR" "$CORRELATOR"
unset -f _mostrar

if [ -z "$KEY" ] || [ -z "$TOKEN" ]; then
  echo ""
  echo "Alguna credencial esta vacia. Generarlas con:"
  echo "    python -c \"import secrets;print(secrets.token_hex(24))\""
  echo "y ponerlas en deploy/.env, despues:  ./make.sh up"
fi
