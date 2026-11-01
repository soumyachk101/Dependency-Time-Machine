# Dependency Time Machine — control plane (cmd/dtm-server)
#
# Build context: product root (go.mod lives here).
#   docker build -t dtm-server .
#
# Notes:
# - go.mod declares go 1.27, so the builder uses golang:1.27-alpine.
# - SQLite backend is github.com/modernc.org/sqlite: a PURE-GO driver.
#   CGO_ENABLED=0 gives a fully static binary; no system sqlite/libc needed.
# - Postgres backend uses github.com/lib/pq (also pure Go). Same image
#   serves both; the mode is chosen at runtime by env, not build arg:
#
#     SQLite (dev default) profile:
#       DTM_DATA_DRIVER=sqlite        (default when unset)
#       DTM_SQLITE_PATH=/data/dtm.sqlite
#
#     Postgres (production) profile:
#       DTM_DATA_DRIVER=postgres
#       DTM_DATABASE_URL=postgres://dtm:secret@<host>:5432/dtm?sslmode=disable
#
#   Both profiles are wired in docker-compose.yml; env names verified
#   against internal/config/config.go.

# ---- builder ---------------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Module cache + build cache layers.
COPY go.mod ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

# Version marker: dtm-server --version reports $DTM_VERSION at RUNTIME
# (main.versionString reads the env, defaulting to "dev"). Set it per-deploy,
# not at build time. There is no ldflags -X symbol to set here.
RUN CGO_ENABLED=0 GOFLAGS=-mod=mod go build \
    -ldflags="-s -w" \
    -o /out/dtm-server ./cmd/dtm-server

# ---- runtime ---------------------------------------------------------------
FROM alpine:3.20

# Non-root user; owns the data dir.
RUN addgroup -S dtm && adduser -S dtm -G dtm \
    && mkdir -p /data && chown -R dtm:dtm /data
USER dtm

COPY --from=build /out/dtm-server /usr/local/bin/dtm-server

WORKDIR /data

# Verified port: internal/config defaults DTM_HTTP_ADDR to 127.0.0.1:8080;
# in a container it must bind 0.0.0.0:8080 (compose sets the env).
EXPOSE 8080

ENV DTM_HTTP_ADDR=0.0.0.0:8080 \
    DTM_SQLITE_PATH=/data/dtm.sqlite \
    DTM_DATA_DRIVER=sqlite \
    DTM_LOG_LEVEL=INFO

# Health endpoint is GET /healthz (internal/api/server.go, not rate-limited).
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

VOLUME ["/data"]

CMD ["dtm-server"]
