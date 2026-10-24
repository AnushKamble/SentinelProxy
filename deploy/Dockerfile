# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build statically linked binaries
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/sentinelproxy ./cmd/sentinelproxy
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/mock-backend ./cmd/mock-backend
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/loadgen ./cmd/loadgen

# Runtime stage
FROM alpine:3.20

RUN apk --no-cache add ca-certificates curl tzdata && \
    addgroup -S sentinel && adduser -S sentinel -G sentinel

WORKDIR /app

COPY --from=builder /bin/sentinelproxy /app/sentinelproxy
COPY --from=builder /bin/mock-backend /app/mock-backend
COPY --from=builder /bin/loadgen /app/loadgen
COPY configs/ /app/configs/

RUN chown -R sentinel:sentinel /app

USER sentinel

EXPOSE 8080 8081

ENTRYPOINT ["/app/sentinelproxy"]
CMD ["-config", "/app/configs/sentinelproxy.docker.yaml"]
