# DTM deployment — notes and gaps

Findings from packaging work. Each item is specific: what, where, and
what to do. Nothing here is "none" unless it actually is.

## 1. `go.sum` is missing from the checkout

`go.mod` (Go 1.27, module `github.com/ensembyte/dtm`) is present but
`go.sum` is absent, and `main.go`'s comments + README confirm it was
deliberately not generated yet. Consequences:

- **The root `Dockerfile` uses `GOFLAGS=-mod=mod` and runs `go mod
  download` before `go build`.** Without `-mod=mod`, the build fails
  hard on a missing `go.sum` (Go refuses to write it under the default
  `-mod=readonly`). This is a real, verified gotcha: build the image
  with the shipped Dockerfile as-is, and it will work. If you later add
  `go.sum` (via `go mod tidy`), the `-mod=mod` flag becomes a no-op and
  everything still works, so it's safe to leave.
- The `dtm-server` compose build inherits the same behaviour.
- **Action for the docs/CI head:** run `go mod tidy` and commit
  `go.sum`. After that, `GOFLAGS=-mod=mod` can be dropped, but is not
  required to.

## 2. `DTM_GITHUB_TOKEN` is read outside `config.Load`

`cmd/dtm-server/main.go` (`githubTokenOrNil`) reads
`os.Getenv("DTM_GITHUB_TOKEN")` directly; it is **not** in
`internal/config/config.go`'s `Load()`, and **not** in `.env.example`.
This is a genuine env-var mismatch between the documented reference
(`.env.example`) and the code. The variable works (least-privilege
default: unset → check-run posting is `skipped_no_token`), but an
operator following `.env.example` only will not find it.

- No change made to `cmd/` or `.env.example` (owned by the docs/CI
  head). This is the one to flag when they finalize the env reference.

## 3. `DTM_DISABLE_DOCKER_PROBE` is a code env var not in `.env.example`

`internal/runner/runner.go` honors `DTM_DISABLE_DOCKER_PROBE=1` to
force degraded even when docker is present. Not documented in
`.env.example`. Same treatment as item 2.

## 4. Health endpoint: exists, and it is unthrottled

`GET /healthz` is registered in `internal/api/server.go` *outside* the
rate limiter (comment in that file: "LB probing"). So the Dockerfile
`HEALTHCHECK` and the compose `healthcheck` blocks are safe to point at
it. No gap here — worth stating because the task flagged it. The
frontend Dockerfile's healthcheck instead probes its own `:80/` (the
SPA index) so the container's liveness does **not** depend on
`dtm-server` being up.

## 5. Default `DTM_HTTP_ADDR` is loopback-only

Config default is `127.0.0.1:8080`. Correct for bare-metal/dev, wrong
for a container. Both the root `Dockerfile` and `docker-compose.yml`
override it to `0.0.0.0:8080`. If you deploy the binary by hand,
remember to set `DTM_HTTP_ADDR` or the container/host will not be
reachable off-loopback. This is deliberate, not a bug.

## 6. Frontend: complete enough to ship a Dockerfile, with one soft gap

`frontend/package.json` is present and buildable in principle:
Vite 5 + React 18, `build` = `tsc -b && vite build`. All source under
`frontend/src` (pages, components, hooks, api client) is present.
So `frontend/Dockerfile` (pnpm → nginx, `/v1` and `/api` proxied to
`dtm-server:8080`) is justified and written. Soft gaps:

- **No lockfile** (`package-lock.json`/`pnpm-lock.yaml`) is committed,
  and no `packageManager` field in `package.json`. The Dockerfile
  `corepack prepare`s a pinned pnpm (ARG `PNPM_VERSION`, default
  9.15.0, the era-correct major for Vite 5 / React 18) and runs
  `--no-frozen-lockfile`. Once the frontend head commits a lockfile,
  add it to the Dockerfile's copy stage and switch to
  `--frozen-lockfile` for reproducible builds.
- **Not built/run** — the task forbids running docker, so the image
  is untested. `pnpm build` (`tsc -b && vite build`) may surface
  type errors the head is still fixing. The Dockerfile is written to
  the layout as it stands *today*; re-run after their fixes land.
- The nginx config that the frontend image `COPY`s lives at
  `deploy/dtm-frontend-nginx.conf` (product-root build context). It is
  **not** inside `frontend/`, so it doesn't collide with the
  frontend-fixing head's file ownership.

## 7. Runner-pool profile: intentionally not wired to the server

`docker-compose.yml` ships `runner-pool` under its own named profile.
It is a docker-CLI sidecar that mounts `/var/run/docker.sock` only
when that profile is selected. It does **not** share a volume or
env with `dtm-server` in a way that would make the server "use" it —
the container runner is probed by the server itself (`runner.New` in
auto mode, `internal/runner/runner.go`). The sidecar exists for hosts
where the server container cannot see docker directly. Plain
`docker compose up -d` never starts it.

- **Gap:** the actual "runner pool" (NATS dispatch, shared worker
  fleet, signed worker images) is out of scope per
  `docs/EXECUTION-ISOLATION-LIMITS.md` "Out of scope". This profile is
  the single-host approximation, clearly labelled advanced. If the
  spec's real pool ships later, this service is the placeholder to
  replace.
- The safe-degraded path (`docs/EXECUTION-ISOLATION-LIMITS.md`) is
  what runs with no docker at all. `DTM_RUNNER_MODE=degraded` or
  `auto`-with-no-docker both land there.

## 8. Postgres driver requires `DTM_DATABASE_URL`; boot fails fast

`cmd/dtm-server` exits 2 if `DTM_DATA_DRIVER=postgres` and
`DTM_DATABASE_URL` is empty. The compose `postgres` profile sets both;
a managed-Postgres deployment must export the URL (see `deploy/GUIDE.md`
§3). No migration auto-applies from `infrastructure/migrations` — the
store (`internal/store/store.go`) **does** run its own `Migrate()` on
open for both drivers, so a fresh Postgres boot will create the schema
itself. The SQL files in `infrastructure/migrations/` are for
out-of-band / managed providers; they are not executed by the binary.

## 9. `DTM_STATIC_DIR` is a base-URL, not a static-file server

Read the name carefully: `config.go` sets it and `main.go` passes it
as `BaseURL` to the API, which uses it for report links in PR
comments. It does **not** mount a static directory. The frontend is
served by the separate nginx container (`frontend/Dockerfile`), not by
the Go binary. Setting `DTM_STATIC_DIR` points report URLs at your
public base; it does not make the server serve the React build.

## 10. Nothing was modified outside the allowed new-file set

New files only: `Dockerfile`, `.dockerignore` (root),
`frontend/Dockerfile`, `deploy/dtm-frontend-nginx.conf`,
`docker-compose.yml`, `deploy/GUIDE.md`, `deploy/CI-INTEGRATION.md`,
`deploy/NOTES-AND-GAPS.md`. `cmd/`, `internal/`, `docs/`,
`infrastructure/`, `frontend/` (source), and `.env.example` were
**not** touched.
