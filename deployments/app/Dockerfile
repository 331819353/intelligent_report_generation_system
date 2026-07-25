FROM golang:1.26.5-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
  && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker \
  && CGO_ENABLED=0 go build -trimpath -o /out/connection-test-worker ./cmd/connection-test-worker

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/worker /app/worker
COPY --from=builder /out/connection-test-worker /app/connection-test-worker
USER 65532:65532
