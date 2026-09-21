# Build en dos etapas: el binario se compila con el toolchain completo y se
# ejecuta sobre una imagen sin shell ni gestor de paquetes.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Las dependencias se resuelven en una capa aparte para que un cambio de
# codigo no invalide la cache de modulos.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# CGO deshabilitado -> binario estatico, ejecutable sobre distroless/static.
# Es la razon de elegir segmentio/kafka-go en lugar de confluent-kafka-go,
# que arrastra librdkafka via cgo.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/ingestor ./cmd/ingestor

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/ingestor /ingestor

EXPOSE 8000
USER nonroot:nonroot

ENTRYPOINT ["/ingestor"]
