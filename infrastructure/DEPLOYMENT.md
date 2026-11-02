# Deployment notes

Running the control plane in production-ish conditions.

## 1. Build

```sh
go build -o dtm-server ./cmd/dtm-server
```

## 2. Database

SQLite is the default and needs nothing. For Postgres:

```sh
psql "$DTM_DATABASE_URL" -f infrastructure/migrations/0001_init.sql
```

(`store.Open` also self-migrates on open, so the migration file and the
embedded schema are interchangeable — running both is a no-op.)

Set `DTM_DATA_DRIVER=postgres` and point `DTM_DATABASE_URL` at the
database. For local development the SQLite default is fine: the file
lands at `DTM_SQLITE_PATH` (default `./data/dtm.sqlite`) and the store
creates the parent directory.

## 3. Execution isolation

Choose the mode deliberately (`DTM_RUNNER_MODE`):

| Mode | Behaviour |
|---|---|
| `auto` (default) | Probe docker; use the container runner when reachable, otherwise run safe-degraded analysis-only. The choice and reason are logged at boot. |
| `container` | Require docker; boot fails otherwise. Never silently degrades. |
| `degraded` | Explicitly analysis-only. No probe. |

For container mode:

- `docker info` must succeed from the host the control plane runs on.
- `DTM_DOCKER_IMAGE` must be pullable.
- Create the egress-allowlist network first:
  `docker network create dtm-egress`
  and restrict its bridges to the approved registries. Without it the
  containers fall back to docker's default network and the egress
  boundary is not enforced — verify before running untrusted repos.

`DTM_DISABLE_DOCKER_PROBE=1` forces degraded selection even when docker
is present (used by CI to keep the host side-effect free).

The full statement of what each mode does and does not claim is in
`docs/EXECUTION-ISOLATION-LIMITS.md`.

## 4. GitHub integration

All off until explicitly configured:

- `DTM_GITHUB_WEBHOOK_SECRET` — arms HMAC verification of
  `POST /v1/webhooks/github`. Empty = the endpoint rejects every event.
- `DTM_GITHUB_TOKEN` — enables check-run posting. Empty = posting is
  reported as `skipped_no_token`, never as success.
- `DTM_GITHUB_APP_ID` / `DTM_GITHUB_APP_PRIVATE_KEY_FILE` — GitHub App
  credentials. The installation-token exchange is a documented
  limitation of this environment (see `docs/KNOWN-LIMITATIONS.md`).

## 5. Frontend

Build once, then point the control plane at it:

```sh
cd frontend && npm install && npm run build   # produces frontend/dist/
```

Set `DTM_STATIC_DIR=<path to frontend/dist>` and the control plane serves
the workbench same-origin at `/`, so no CORS configuration is needed in
production. For local development, `npm run dev` starts the Vite server
on :5173 and proxies `/v1` to the control plane.

## 6. Post-deploy smoke check

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS -X POST http://127.0.0.1:8080/v1/repositories/connect \
  -H 'Content-Type: application/json' -d '{"owner":"acme","repo":"checkout-service"}'
```

The boot log line `runner_mode=...` is the single source of truth for
which isolation boundary is active; `safe-degraded` means no build/test
execution is available on this host and run reports will say so.
