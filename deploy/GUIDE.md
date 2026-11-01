# DTM deployment guide

Three shapes, matching the env names the server actually reads
(`internal/config/config.go`):

1. local dev via SQLite (default, zero external deps)
2. single-host compose (SQLite or Postgres)
3. managed-Postgres production shape

## 1. Local dev via SQLite

No container needed:

```sh
go build -o dtm-server ./cmd/dtm-server
cp .env.example .env        # optional; sane defaults apply without it
./dtm-server                # listens on 127.0.0.1:8080, data in ./data/dtm.sqlite
curl -s localhost:8080/healthz
```

Env that matter (all have defaults; set only what you change):

| var | default | notes |
| --- | --- | --- |
| `DTM_DATA_DRIVER` | `sqlite` | `sqlite` \| `postgres` \| `memory` |
| `DTM_SQLITE_PATH` | `./data/dtm.sqlite` | driver `sqlite` only |
| `DTM_DATABASE_URL` | *(empty)* | required when driver=`postgres`; boot exits 2 if missing |
| `DTM_HTTP_ADDR` | `127.0.0.1:8080` | in a container set `0.0.0.0:8080` |
| `DTM_LOG_LEVEL` | `INFO` | |
| `DTM_STATIC_DIR` | *(empty)* | if set, the server treats it as the public base URL for report links |
| `DTM_RATE_LIMIT_RPS` / `DTM_RATE_LIMIT_BURST` | `10` / `20` | per-client-IP token bucket; `/healthz` is exempt |
| `DTM_RUNNER_MODE` | `auto` | `auto` probes docker; `container` requires it; `degraded` is analysis-only |
| `DTM_DOCKER_IMAGE` | `ghcr.io/ensembyte/dtm-runner:1.0.0` | runner image, recorded per result |
| `DTM_RUNNER_CPU_LIMIT` / `DTM_RUNNER_MEMORY_LIMIT` / `DTM_RUNNER_DISK_LIMIT` / `DTM_RUNNER_WALL_CLOCK_LIMIT` | `2` / `4g` / `10g` / `1800s` | shared base+candidate quotas |
| `DTM_OSV_ENDPOINT` | *(empty)* | e.g. `https://api.osv.dev/v1` |
| `DTM_ENABLE_ONLINE_ADVISORIES` | `true` | when `false`, findings are labelled offline-only, never fabricated |
| `DTM_ARTIFACT_RETENTION_DAYS` | `14` | |
| `DTM_GITHUB_WEBHOOK_SECRET` | *(empty)* | empty → webhook intake rejects with 401 (closed default) |
| `DTM_GITHUB_APP_ID` / `DTM_GITHUB_APP_PRIVATE_KEY_FILE` | *(empty)* | GitHub App integration; see KNOWN-LIMITATIONS |
| `DTM_GITHUB_TOKEN` | *(empty)* | read directly by `cmd/dtm-server` (not via `config.Load`); without it check-run posting is `skipped_no_token` |
| `DTM_VERSION` | *(empty → `dev`)* | `dtm-server --version` |
| `DTM_DISABLE_DOCKER_PROBE` | *(unset)* | `=1` forces degraded even when docker is present |

## 2. Single-host compose

At the product root, `docker-compose.yml`:

```sh
# dev: server + SQLite (data in ./data)
docker compose up -d

# production shape: Postgres (db service + server pointed at it via env)
DTM_DATA_DRIVER=postgres \
DTM_DATABASE_URL='postgres://dtm:change-me@postgres:5432/dtm?sslmode=disable' \
DTM_PG_PASSWORD=change-me \
docker compose --profile postgres up -d

# add the operator workbench (nginx on :8081, proxies /v1 and /api → dtm-server)
docker compose --profile frontend up -d
```

Notes:
- The server image is non-root; `/data` is its working dir and a volume
  (SQLite) or unused (Postgres). Health: `GET /healthz`.
- Postgres is **not** published on the host port; it's reachable on the
  compose network as `postgres:5432`. That is the host part of
  `DTM_DATABASE_URL` above.
- **runner-pool profile (advanced)**: `docker compose --profile
  runner-pool up -d` mounts the host docker socket into a sidecar so the
  container runner can drive it. It is NOT part of `docker compose up -d`,
  NOT a managed pool, and NOT required on hosts where the server already
  sees docker directly. Without it (or docker at all), the safe-degraded
  mode in `docs/EXECUTION-ISOLATION-LIMITS.md` is what runs.

## 3. Managed-Postgres production shape

Same server image; the only deltas:

- `DTM_DATA_DRIVER=postgres`
- `DTM_DATABASE_URL=postgres://<user>:<secret>@<managed-host>:5432/dtm?sslmode=require`
  (adjust `sslmode` to your provider; the migrations that create the schema
  live in `infrastructure/migrations/` and the store also self-migrates
  on boot, so applying them out of band is optional)
- `DTM_HTTP_ADDR` behind your LB; TLS termination in front, not in the
  server (it speaks plain HTTP).
- Keep `DTM_RUNNER_MODE` explicit (`container` on a host with docker,
  `degraded` on a host that must not execute untrusted code — e.g. a
  shared platform). Don't ship `auto` to a shared platform and discover
  the probe behaviour in prod.
- Secrets (`DTM_GITHUB_WEBHOOK_SECRET`, `DTM_GITHUB_APP_*`,
  `DTM_GITHUB_TOKEN`) come from your secret manager; empty means the
  feature is off, which is the safe default.
- The Go binary is pure-CGO-off (modernc sqlite + lib/pq are both pure
  Go), so the image is the same in every shape; only env changes.

## Frontend

`frontend/Dockerfile` (multi-stage pnpm → nginx) is ready in this
checkout, but see `deploy/NOTES-AND-GAPS.md`: no lockfile yet and the
build is untested (docker is not run here). The nginx config in
`deploy/dtm-frontend-nginx.conf` proxies `/v1/` and `/api/` to
`http://dtm-server:8080`, matching the same-origin fetch convention in
`frontend/src/api/client.ts`.
