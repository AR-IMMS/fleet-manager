# syntax=docker/dockerfile:1.7

FROM golang:1.25.5-alpine3.22 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/fleet-manager ./cmd/fleet-manager

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 app \
    && adduser -S -D -H -u 65532 -G app app

COPY --from=build --chown=65532:65532 /out/fleet-manager /usr/local/bin/fleet-manager

USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/fleet-manager"]
