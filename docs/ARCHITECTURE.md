# Architecture — Dependency Time Machine

This document describes the implemented architecture and maps it to the
blueprint at `03-Dependency-Time-Machine.md` (spec sections cited as §n).

## Component map

```
cmd/dtm-server        control-plane entrypoint (fail-fast config, boot audit log)
internal/config       environment loading + validation (all defaults documented)
internal/logx         structured logging (slog, stable field vocabulary)
internal/parser       manifest/lockfile parsing for node (npm/pnpm), python, rust
internal/graphd       deterministic base/candidate graph diff
internal/core         domain model: identity, graph, diff, findings, evidence
internal/pipeline     orchestration: parse → diff → advisory → release notes → report
internal/risk         component-based risk engine + regression classification buckets
internal/report       evidence bundle: markdown + machine JSON rendering
internal/advisory     OSV feed client + bundled offline dataset; provenance markers
internal/relnotes     release-note fetch with source URL + retrieval time
internal/runner       execution isolation boundary (container vs degraded runner)
internal/ci           policy thresholds, check-state decision, GitHub check-run client
internal/api          HTTP control plane: routes, rate limiter, webhook intake, run store
internal/store        data layer: SQLite / Postgres / in-memory (same SQL)
frontend/             React + Vite workbench (hash-routed, typed API client)
```

## Request flow (analysis path)

```
POST /v1/upgrade-runs
  → internal/api (rate limit, idempotency, decode + strict field check)
  → internal/pipeline.Run
      parser (base + candidate)          → internal/graphd.Diff
      internal/advisory (OSV online or offline dataset; feed status recorded)
      internal/relnotes (only when notes_enabled)
      internal/risk.Evaluate            → findings + buckets
      internal/report (markdown + JSON, provenance: SHAs, lockfile hashes,
      runner mode, retention)
  → StoredRun in api.InMemoryRunStore (replayed idempotency keys return it)
```

Isolated execution is **deliberately not** part of the API pipeline: a
pipeline run is the analysis half. Execution happens in `internal/runner`
and its results are fed back through `internal/risk.ExecResult` by the
orchestrator that hosts both, keeping the degraded/container boundary
explicit and auditable (see `docs/EXECUTION-ISOLATION-LIMITS.md`).

## Execution isolation

`internal/runner` exposes one interface (`Runner`) with two
implementations:

- **ContainerRunner** — the documented real-isolation path. One disposable
  container per side; shared CPU/memory/disk limits (fair-comparison NFR);
  egress restricted to the `dtm-egress` network; no host mounts beyond the
  staged workdir; no docker socket; no privileged mode; image digest
  recorded on every result (reproducibility NFR). Reached only when a
  docker runtime is actually available.
- **DegradedRunner** — safe analysis-only fallback. Performs no execution
  at all; every result carries `Executed: false`, which flows through the
  result normaliser so infrastructure absence is never classified as a
  dependency regression (NFR "Reliability").

`runner.New("auto", ...)` probes for docker and falls back with a
structured log entry, so the boundary is always visible in boot logs and
run reports.

## Data layer

`internal/store` shares one SQL schema across backends:

- **SQLite** (pure-Go `modernc.org/sqlite`) — local dev and CI, zero
  external services; WAL mode enabled.
- **Postgres** — production; DSN via `DTM_DATABASE_URL`. Migration files
  mirror the embedded schema in `infrastructure/migrations/`.
- **In-memory** — tests and ephemeral use.

Tables: `projects`, `snapshots`, `upgrade_runs`, `findings`, `policies`.

The API's run records additionally live in `api.InMemoryRunStore` so the
control plane is fully usable without a database; the persistent store is
opened only when a non-memory driver is configured.

## Security posture (spec §12)

- Webhook intake verifies `X-Hub-Signature-256` (HMAC-SHA256) when a
  secret is configured; **without a secret it rejects** — closed default.
- GitHub check-run posting requires an explicitly configured token;
  unconfigured integrations report `skipped_no_token`, never faked
  success.
- Public routes pass a per-client-IP token bucket; unknown JSON fields
  are rejected on every decode.
- Untrusted strings are only ever emitted as JSON, never spliced into
  log lines.
- Parser input is size-capped (8 MB per file) as an archive-bomb guard.
- Adjudication: failed infrastructure runs are marked with runner-side
  exit code 125, not a package failure.

## Frontend

`frontend/` is a React 18 + Vite 5 app, hash-routed with six primary
sections matching the spec's information architecture (Repositories,
Upgrade runs, Dependency graph, Policies, Environments, Settings). The
typed API client (`src/api/client.ts`) models loading/error/empty
explicitly so no screen can render a fabricated value; dev mode proxies
`/v1` to the control plane, production is served same-origin from
`DTM_STATIC_DIR`. Palette and typography follow the spec's design brief
(Palette F, monochrome; Space Grotesk + Geist Mono with documented
self-hosting path).

## Out of scope for this codebase

The spec's full target architecture (NATS JetStream queue, Rust parser
services, S3 artifact store, Firecracker microVMs) is not implemented in
this repository. The NATS/S3 layers are design-time recommendations; the
MVP runs end-to-end on the Go control plane, the two documented runner
modes, and the SQLite/Postgres metadata layer.
