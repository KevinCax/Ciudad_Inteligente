# Makefile raiz. Orquesta los dos microservicios sin acoplarlos: cada uno se
# compila y despliega por separado, esto solo evita repetir comandos.

SERVICIOS := ingestor persister indexer correlator jobs notifier
GO        ?= go

.PHONY: help
help:
	@echo "Targets disponibles:"
	@echo "  make sync-contracts  Copia los contratos a cada servicio"
	@echo "  make check-contracts Falla si alguna copia quedo desincronizada"
	@echo "  make tidy            go mod tidy en ambos servicios"
	@echo "  make build           Compila ambos binarios en bin/"
	@echo "  make test            Ejecuta los tests de ambos servicios"
	@echo "  make vet             go vet en ambos servicios"
	@echo "  make fmt             Formatea el codigo de ambos servicios"
	@echo "  make check           fmt + vet + test + check-contracts"
	@echo "  make docker          Construye las imagenes de ambos servicios"
	@echo "  make adr             Consolida los ADR en docs/ADR-consolidado.md"
	@echo "  make rutas-preparar  Descarga el mapa y prepara los indices de OSRM"

# --- Contratos -------------------------------------------------------------
# El schema vive una sola vez en contracts/ y se copia a cada servicio, que lo
# embebe con go:embed. Ver contracts/README.md para el porque de este diseno.

# Las listas son la fuente de verdad de quien recibe cada contrato. Antes
# estaban escritas a mano copia por copia, y check-contracts solo verificaba 3
# de las 7: las de persister e indexer podian quedar desincronizadas sin que
# nada lo detectara. Ahora anadir un servicio es anadir una palabra, y la
# comprobacion cubre por construccion todo lo que la sincronizacion escribe.
ESQUEMA_EVENTO := contracts/canonical-event/v1.0/schema.json
ESQUEMA_ALERTA := contracts/correlated-alert/v1.0/schema.json

COPIAS_EVENTO := ingestor correlator persister indexer
COPIAS_ALERTA := correlator persister indexer notifier

.PHONY: sync-contracts
sync-contracts:
	@for s in $(COPIAS_EVENTO); do 	  cp $(ESQUEMA_EVENTO) services/$$s/internal/canonical/schema.json; 	done
	@for s in $(COPIAS_ALERTA); do 	  cp $(ESQUEMA_ALERTA) services/$$s/internal/canonical/alert-schema.json; 	done
	@echo "contratos sincronizados"

# check-contracts debe correr en CI: detecta que alguien edito la copia de un
# servicio en lugar de la fuente de verdad.
.PHONY: check-contracts
check-contracts:
	@fallo=0; 	for s in $(COPIAS_EVENTO); do 	  diff -q $(ESQUEMA_EVENTO) services/$$s/internal/canonical/schema.json >/dev/null 	    || { echo "ERROR: la copia del contrato de evento en $$s difiere"; fallo=1; }; 	done; 	for s in $(COPIAS_ALERTA); do 	  diff -q $(ESQUEMA_ALERTA) services/$$s/internal/canonical/alert-schema.json >/dev/null 	    || { echo "ERROR: la copia del contrato de alerta en $$s difiere"; fallo=1; }; 	done; 	[ $$fallo -eq 0 ] || exit 1; 	echo "contratos sincronizados correctamente (todas las copias verificadas)"

# --- Ciclo de desarrollo ---------------------------------------------------

.PHONY: tidy
tidy:
	@for s in $(SERVICIOS); do \
	  echo "==> tidy $$s"; (cd services/$$s && $(GO) mod tidy); \
	done

.PHONY: build
build:
	@mkdir -p bin
	@for s in $(SERVICIOS); do \
	  echo "==> build $$s"; (cd services/$$s && $(GO) build -o ../../bin/$$s ./cmd/$$s); \
	done

.PHONY: test
test:
	@for s in $(SERVICIOS); do \
	  echo "==> test $$s"; (cd services/$$s && $(GO) test ./...) || exit 1; \
	done

.PHONY: vet
vet:
	@for s in $(SERVICIOS); do \
	  echo "==> vet $$s"; (cd services/$$s && $(GO) vet ./...) || exit 1; \
	done

.PHONY: fmt
fmt:
	@for s in $(SERVICIOS); do \
	  echo "==> fmt $$s"; (cd services/$$s && $(GO) fmt ./...); \
	done

.PHONY: check
check: check-contracts fmt vet test

# --- Contenedores ----------------------------------------------------------

.PHONY: docker
docker:
	@for s in $(SERVICIOS); do \
	  echo "==> docker build $$s"; docker build -t ciudad-inteligente/$$s:latest services/$$s; \
	done

# --- Despliegue -----------------------------------------------------------

COMPOSE_EDGE  := -f deploy/docker-compose.edge.yml -f deploy/docker-compose.dev.yml
COMPOSE_CLOUD := -f deploy/docker-compose.cloud.yml
ENVFILE       := --env-file deploy/.env

.PHONY: up
up:
	docker compose $(COMPOSE_EDGE) $(ENVFILE) up -d --build

.PHONY: up-cloud
up-cloud:
	docker compose $(COMPOSE_CLOUD) $(ENVFILE) up -d

.PHONY: down
down:
	-docker compose $(COMPOSE_CLOUD) down
	docker compose $(COMPOSE_EDGE) down

.PHONY: logs
logs:
	docker compose $(COMPOSE_EDGE) logs -f ingestor correlator

.PHONY: config
config:
	docker compose $(COMPOSE_EDGE) $(ENVFILE) config --quiet && echo "edge + dev: sintaxis correcta"
	docker compose $(COMPOSE_CLOUD) $(ENVFILE) config --quiet && echo "cloud: sintaxis correcta"

.PHONY: adr
adr:
	@python docs/consolidar-adr.py

.PHONY: load
load:
	cd test/load && $(GO) run . -escenario $(or $(ESCENARIO),sostenido)

# --- Rutas por calle -------------------------------------------------------
# Una sola vez: descarga el mapa de Guatemala y construye los indices que usa
# el motor de rutas. Fuera de "up" porque son 131 MB y varios minutos.

.PHONY: rutas-preparar
rutas-preparar:
	@bash deploy/preparar-rutas.sh
