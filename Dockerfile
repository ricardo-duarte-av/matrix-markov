# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
# cgo is needed for SQLite (mattn/go-sqlite3).
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -tags goolm -trimpath -ldflags="-s -w" -o /out/markov-bot .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 1000 markov \
 && mkdir -p /config /data && chown markov:markov /data
COPY --from=build /out/markov-bot /usr/local/bin/markov-bot
USER markov
VOLUME ["/data"]
ENTRYPOINT ["markov-bot"]
CMD ["-config", "/config/config.yaml", "-data-dir", "/data"]
