# Dependency Time Machine — Scaling & Operations

## Statelessness and horizontal scale

The control-plane HTTP handler is stateless: request parsing, policy
evaluation, graph diff and risk classification are pure functions of the
request plus store state. Running N replicas behind a load balancer scales
throughput linearly for the read and analysis paths.

Two things are **not** automatically safe to scale out, and must be
declared to an operator:

1. **Per-replica rate-limit counters.** The token-bucket limiters are
   in-process (`internal/api` + `internal/ratelimit`). A pod restart or a
   new replica resets its counters to full, so the effective global budget
   is `N_replicas × per-class budget`. The mitigation is to size
   `DTM_RATE_LIMIT_<CLASS>_RPS/_BURST` for the *sum* of replicas you intend
   to run (e.g. 3 replicas × 40 burst = 120 in flight), or move to a
   shared limiter (Redis) in a later phase. This is a documented
   limitation, not a silent bug.

2. **Webhook replay nonces.** The `X-Hub-Delivery` nonce window is held in
   memory per replica. A delivery replayed to a *different* replica is not
   caught, because that replica has not seen the nonce yet. The replay
   guard is therefore a *per-process best-effort* protection: it stops
   redelivery to the same instance and is the honest mechanism available
   without an external store. A production deployment wanting cross-replica
   replay safety must store nonces in Postgres/Redis. Both are listed here
   so an operator is not surprised.

## Data store

`DTM_DATA_DRIVER` selects the store (`sqlite` default, `postgres` production,
`memory` for tests). The SQLite single-file store is not a multi-writer
store: for more than one replica or any durability guarantee you must run
`DTM_DATA_DRIVER=postgres` with a shared Postgres (see
`infrastructure/migrations/`). The schema is applied at boot by
`internal/store.Migrate` (idempotent); `infrastructure/migrations/*.sql` is
the canonical DDL for a managed Postgres.

## Run persistence (verified on this build)

Runs recorded via `POST /v1/cli-runs` (and results written by the pipeline)
now **survive a restart** when `DTM_DATA_DRIVER=sqlite` or `postgres`:
`cmd/dtm-server` wires the opened store into the API layer via
`store.NewPersistentRunStore`, which upserts the run row and rewrites its
findings into the shared `findings` table on every save. The in-memory
twin is used only when `DTM_DATA_DRIVER=memory` (tests / ephemeral runs).
After a restart the report is rehydrated from the durable row via
`report.FromJSON` (the public camelCase shape); the JSON report endpoint
works from the raw payload, the markdown endpoint requires a successful
rehydrate. This was a documented gap in earlier builds — it is now closed
for the durable store paths.

## Execution isolation (do not restate)

The base/candidate build-and-test boundary is enforced by
`internal/runner`, which is either the docker-container path (real
isolation) or the *safe degraded* path (manifest/graph level only, no
repository code executed on the host). The runner mode is selected by
`DTM_RUNNER_MODE` and the probes behind it; the exact guarantees are in
[EXECUTION-ISOLATION-LIMITS.md](./EXECUTION-ISOLATION-LIMITS.md).

## Observability

- Structured JSON logs (`logx`, `DTM_LOG_LEVEL`); no credential or secret
  values are written to logs.
- `GET /healthz` (liveness, unthrottled) and `GET /readyz` (503 until the
  store is reachable, unthrottled) — wire these into your load balancer
  and orchestrator probes.
- OpenTelemetry export is not wired in this reference build; the log/JSON
  surface is the integration point for a metrics pipeline.

## Load-balancer / timeout knobs

The `http.Server` in `cmd/dtm-server` sets `ReadHeaderTimeout`,
`IdleTimeout` and `ReadTimeout`/`WriteTimeout` (see main.go). For a TLS-terminating
LB, mirror those values on the LB's backend timeout so the LB does not cut
long-running analysis requests mid-stream.
