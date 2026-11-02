# Dependency Time Machine — API Reference

Base URL in local dev is `http://127.0.0.1:8080` (env `DTM_HTTP_ADDR`).
All responses are JSON. Every non-2xx returns the shared error envelope:

```json
{ "error": { "code": "err_not_found", "message": "route not found: GET /x" } }
```

## Authentication

- **Operator auth** — when `DTM_API_TOKEN` is set (>=16 chars), every `/v1/*`
  route requires a matching `Authorization: Bearer <token>` header, compared in
  constant time. A missing or wrong token returns `401 err_unauthorized`.
  When the token is unset the API runs in **dev-open mode** and logs a WARN per
  unauthenticated request (see [SCALING.md](./SCALING.md)).
- **Webhooks** — `POST /v1/webhooks/github` is verified with a
  per-recipient `X-Hub-Signature-256` HMAC-SHA256 (key = `DTM_GITHUB_WEBHOOK_SECRET`)
  using a constant-time compare, plus a `X-Hub-Delivery` nonce replay guard
  (15-minute in-process window; a replay returns `403 err_forbidden`).
  A webhook is REJECTED with `401` when no secret is configured.
- **Liveness/readiness** — `GET /healthz` and `GET /readyz` are unauthenticated
  and unthrottled so a load balancer's probe never 429s or 401s.

## Rate limiting (per client IP, per route class)

| Class   | Env knobs                                   | Default RPS / burst |
|---------|---------------------------------------------|---------------------|
| analysis| `DTM_RATE_LIMIT_ANALYSIS_RPS/_BURST`         | 20 / 40             |
| webhooks| `DTM_RATE_LIMIT_WEBHOOKS_RPS/_BURST`         | 10 / 20             |
| reads   | `DTM_RATE_LIMIT_READS_RPS/_BURST`            | 50 / 100            |
| repos   | `DTM_RATE_LIMIT_REPOS_RPS/_BURST`            | 5 / 10              |

Over-limit responses are `429 err_rate_limited` with a `Retry-After: 1` header
and the error envelope. The legacy global knobs `DTM_RATE_LIMIT_RPS/_BURST`
(10/20) are a fallback when a class is explicitly unset.

## Endpoints

### `POST /v1/upgrade-runs` — class: analysis
Run a dependency upgrade evaluation. Body: `upgradeRunRequest`.
Returns `200` with the run (report + graph diff + findings) on success,
`413 err_payload_too_large` over 2 MiB, `400 err_invalid_request` on
validation failure.

### `POST /v1/repositories/connect` — class: repos
Register a repository. Body: `connectRequest` (owner, repo). `200` on success.

### `POST /v1/snapshots` — class: repos
Record a base/candidate lockfile snapshot. Body: `snapshotRequest`.

### `POST /v1/policies` — class: repos
Define the upgrade policy (allowed version classes, risk thresholds).
Body: `policyRequest`.

### `POST /v1/webhooks/github` — class: webhooks
GitHub event intake. Verified (HMAC + replay guard) before processing.
`202` when accepted (replays are idempotent no-ops), `401`/`403` on auth
failure, `413` over the 2 MiB cap.

### `POST /v1/cli-runs` — class: repos
Intake a run produced by the `dtm` CLI. Body: `cliRunRequest`. The finished
run is **persisted** to the configured store (survives a restart when
`DTM_DATA_DRIVER=sqlite` or `postgres`; see [SCALING.md](./SCALING.md)).

### `GET /v1/upgrade-runs/{id}` — class: reads
Return a stored run (report + findings + artifact ref). `404 err_not_found`
when absent. The path parameter is validated against a safe charset
(`400` otherwise).

### `GET /v1/upgrade-runs/{id}/graph-diff` — class: reads
Return the run's dependency-graph diff. `404` when the run or diff is absent.

### `GET /v1/upgrade-runs/{id}/report` — class: reads
Return the run's report. Accepts `?format=json` (default) or `?format=markdown`.
After a restart the report is rehydrated from the durable store; the markdown
path works only when rehydration succeeds (otherwise the JSON payload is the
authoritative artifact).

### `GET /healthz` — unthrottled
`200 {"status":"ok"}` once the process is up (liveness).

### `GET /readyz` — unthrottled
`200 {"status":"ready"}` once the data store is reachable, else
`503 {"status":"not_ready"}`.

## Error codes

| code                   | HTTP | meaning                                   |
|------------------------|------|-------------------------------------------|
| `err_unauthorized`     | 401  | missing/invalid operator token or webhook HMAC |
| `err_forbidden`        | 403  | webhook nonce replay within the window    |
| `err_rate_limited`     | 429  | over the route-class budget (see `Retry-After`) |
| `err_invalid_request`  | 400  | bad path/query param or JSON body         |
| `err_payload_too_large`| 413  | body exceeds the 2 MiB cap                |
| `err_not_found`        | 404  | unknown route or absent run               |
| `err_conflict`         | 409  | idempotency key reused with different content |
| `err_internal`         | 500  | unexpected server error                   |
