# Dependency Time Machine

Reproducible pre-merge dependency-upgrade evaluation. Compare a base and a
candidate dependency graph, inspect release notes and security advisories,
run builds/tests in isolated environments, and produce an evidence-linked
risk report plus a CI check — instead of "upgrade and hope".

This repository is the MVP: a Go control plane, a React/Vite operator
workbench, SQLite/Postgres metadata, and a container- or degraded-mode
execution boundary. The full blueprint lives at
`03-Dependency-Time-Machine.md` at the workspace root.

## Layout

```
cmd/dtm-server/         the control plane binary
internal/               core, parser, graphd, pipeline, risk, report,
                        advisory, relnotes, runner, ci, api, store, config, logx
frontend/               React + Vite workbench (hash-routed, six sections)
docs/                   ARCHITECTURE, REQUIREMENT-TRACEABILITY,
                        EXECUTION-ISOLATION-LIMITS, KNOWN-LIMITATIONS
infrastructure/         Postgres migration + deployment notes
.github/workflows/ci    Go + frontend CI
```

## Install

Requires Go 1.27+ (see `go.mod`) and Node 20+ for the frontend.

```sh
go version      # need 1.27+
node --version  # need 20+
```

There is no install step for the Go side — build it in place (below).

## Run

### 1. Control plane

```sh
cd Dependency-Time-Machine
cp .env.example .env     # optional; sane defaults apply without it

# Go has no go.sum yet in this checkout; fetch module dependencies once:
go mod download          # (or `go mod tidy` to also write go.sum)

# Build:
go build -o dtm-server ./cmd/dtm-server

# Run (defaults: sqlite store at ./data/dtm.sqlite, http on 127.0.0.1:8080,
# runner mode auto → container if docker is reachable, else safe-degraded):
./dtm-server             # add -h for flags, -version for the build marker
```

Verify:

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS -X POST http://127.0.0.1:8080/v1/repositories/connect \
  -H 'Content-Type: application/json' \
  -d '{"owner":"acme","repo":"checkout-service"}'
```

The boot log line `runner_mode=...` tells you which isolation boundary is
active. `auto` logs which mode it selected (container or degraded) and why.

### 2. Frontend

```sh
cd frontend
npm install
npm run dev              # dev server on :5173; proxies /v1 to :8080
```

Open http://127.0.0.1:5173 — the dev server proxies API calls to the
control plane, so no CORS configuration is needed. Production: build
`frontend/dist/` and point `DTM_STATIC_DIR` at it; it is then served
same-origin by the control plane.

## Environment variables

See `.env.example` for the full, commented reference. Key ones:

| Variable | Default | Meaning |
|---|---|---|
| `DTM_DATA_DRIVER` | `sqlite` | `sqlite` \| `postgres` \| `memory` |
| `DTM_SQLITE_PATH` | `./data/dtm.sqlite` | SQLite file path |
| `DTM_DATABASE_URL` | — | Postgres DSN (required when driver is `postgres`) |
| `DTM_HTTP_ADDR` | `127.0.0.1:8080` | listen address |
| `DTM_LOG_LEVEL` | `INFO` | `DEBUG`/`INFO`/`WARN`/`ERROR` |
| `DTM_STATIC_DIR` | — | serve a prebuilt `frontend/dist` at `/` |
| `DTM_RATE_LIMIT_RPS` / `_BURST` | `10` / `20` | per-client-IP token bucket |
| `DTM_RUNNER_MODE` | `auto` | `auto`/`container`/`degraded` |
| `DTM_DOCKER_IMAGE` | `ghcr.io/ensembyte/dtm-runner:1.0.0` | runner image |
| `DTM_RUNNER_CPU_LIMIT` | `2` | shared across base + candidate (fairness) |
| `DTM_RUNNER_MEMORY_LIMIT` | `4g` | ditto |
| `DTM_RUNNER_DISK_LIMIT` | `10g` | ditto |
| `DTM_RUNNER_WALL_CLOCK_LIMIT` | `1800s` | ditto |
| `DTM_OSV_ENDPOINT` | `https://api.osv.dev/v1` | advisory feed |
| `DTM_ENABLE_ONLINE_ADVISORIES` | `true` | `false` → offline dataset only |
| `DTM_ARTIFACT_RETENTION_DAYS` | `14` | raw-log retention |
| `DTM_GITHUB_WEBHOOK_SECRET` | — | empty = webhook intake rejects everything |
| `DTM_GITHUB_APP_ID` | — | GitHub App ID (see limitations) |
| `DTM_GITHUB_APP_PRIVATE_KEY_FILE` | — | App private key path |
| `DTM_GITHUB_TOKEN` | — | enables check-run posting (empty = skip, reported honestly) |
| `DTM_DISABLE_DOCKER_PROBE` | — | `1` forces degraded selection even with docker present |
| `DTM_VERSION` | `dev` | version string reported by `-version` |

## CI integration

The control plane ingests completed runs and posts check-runs. Two ways to
wire it into your pipeline:

```sh
# 1. GitHub App webhook (verify your control plane's intake):
BODY='{"action":"opened"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 \
      -hmac "$DTM_GITHUB_WEBHOOK_SECRET" | awk '{print "sha256="$2}')
curl -fsS -X POST "$DTM_API_BASE/v1/webhooks/github" \
  -H "X-Hub-Signature-256: $SIG" -d "$BODY"

# 2. Ingest a finished CLI/CI run (the "block on confirmed build failure"
#    policy decision is applied server-side when check_run is null):
curl -fsS -X POST "$DTM_API_BASE/v1/cli-runs" \
  -H "Content-Type: application/json" \
  -d '{"project_id":"github/acme/checkout-service","owner":"acme",
       "repo":"checkout-service","commit_sha":"9a2e…",
       "runner_mode":"container","executed":true,
       "result_class":"confirmed_failure","check_run":null}'
```

Policy mapping (see `internal/ci`): block → check failure, warn → warning,
otherwise success. Upgrades are never merged automatically. A degraded
(analysis-only) run with any finding floors to **warning**, so a
no-execution result can never read as "verified".

Full endpoint list: `POST /v1/repositories/connect`, `POST /v1/snapshots`,
`POST /v1/upgrade-runs`, `GET /v1/upgrade-runs/{id}`,
`GET /v1/upgrade-runs/{id}/graph-diff`, `GET /v1/upgrade-runs/{id}/report`,
`POST /v1/policies`, `POST /v1/webhooks/github`, `POST /v1/cli-runs`.

## Commands per component

| Component | Build | Run / test |
|---|---|---|
| Go control plane | `go build -o dtm-server ./cmd/dtm-server` | `./dtm-server` |
| Go unit tests | — | `go test ./...` |
| Go vet | — | `go vet ./...` |
| Frontend | `cd frontend && npm install && npm run build` | `npm run dev` (dev), `npm run preview` (preview build) |
| Frontend lint | — | `cd frontend && npm run lint` |

## Execution isolation — read this

The container-backed runner is the **only** path that executes builds or
tests. When no docker runtime is available, the system falls back to
**safe-degraded** mode: manifest-level and graph-level analysis only,
labelled as such on every report, CI check and UI surface. It **never**
claims isolated build/test execution happened. Full statement:
`docs/EXECUTION-ISOLATION-LIMITS.md`.

## Known limitations

Fonts are not vendored as binary files; the `@fontsource` package set with
system fallbacks is the self-hosting mechanism (see
`frontend/src/styles/fonts/README.md`). Advisory/release-note fetch is
network-dependent, with an offline fallback dataset and an explicit
`awaiting network/credentials` status — never fabricated. Full list:
`docs/KNOWN-LIMITATIONS.md`.
