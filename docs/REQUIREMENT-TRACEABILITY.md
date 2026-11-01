# Requirement traceability — Dependency Time Machine

Maps every functional requirement and non-functional requirement in the
blueprint (`03-Dependency-Time-Machine.md`, §4) to where it is
implemented in this repository. Status:

- **done** — implemented and exercisable in this codebase.
- **partial** — implemented with a documented limitation (see
  `docs/KNOWN-LIMITATIONS.md`).
- **design-only** — represented in the data model / docs but not
  executed by this codebase.

## Functional requirements

### FR-01 — Repository ingestion

| Requirement | Where | Status |
|---|---|---|
| CLI support | `cmd/dtm-server` + `POST /v1/cli-runs` (`internal/api/server.go: handleCLIRun`) | done |
| Read manifest/lockfile at a specific commit SHA | `POST /v1/snapshots` (`handleSnapshot`), `pipeline.Revision.CommitSHA` | done |
| Detect package manager and runtime version | `internal/parser` (`Detect` in `parser/files.go`; parsers for npm/pnpm/python/rust) | done |
| Preserve exact source revision and lockfile hash | `report.RunMeta` (BaseLockSHA/CandLockSHA via `pipeline/helpers.go: lockHash`), `store.snapshots.lockfile_sha256` | done |
| GitHub App | GitHub App credential config exists (`DTM_GITHUB_APP_ID`, `DTM_GITHUB_APP_PRIVATE_KEY_FILE`); installation-token exchange is not wired in this environment | partial |

### FR-02 — Dependency graph

| Requirement | Where | Status |
|---|---|---|
| Direct and transitive dependencies | `internal/parser` → `core.Graph` (Packages + Edges) | done |
| Version constraints, resolved version, registry, integrity | `core.PackageVersion` (Integrity, Source, Direct, RuntimeReq, License) | done |
| Added/removed/upgraded/downgraded/replaced classification | `internal/graphd/diff.go` → `core.DiffCategory` constants | done |
| Reverse dependency impact and transitive reach | `core.DiffEntry.TransitiveImpact`, `core.GraphDiff.ReachableChanged` | done |
| Source/integrity/runtime/peer-change categories | `graphd.Diff` categories `source_changed`, `integrity_changed`, `runtime_requirement_changed`, `peer_resolution_changed` | done |

### FR-03 — Upgrade candidate

| Requirement | Where | Status |
|---|---|---|
| Accept target manifest/lockfile or PR branch | `POST /v1/upgrade-runs` with `base_files`/`candidate_files` + lockfile bytes; PR consumption is via the CLI-run intake path | partial (branch fetch is not implemented; files are supplied by the caller) |
| Compare baseline and candidate graph | `graphd.Diff` (single call site in `pipeline/helpers.go: graphDiff`) | done |
| Release notes with source URL and retrieval time | `internal/relnotes` (`core.ReleaseNote.SourceURL`, `RetrievedAt`; signals labelled "signals, not proof") | done |
| Known breaking-change signals labelled as signals | `internal/relnotes: detectBreakingSignals` + `internal/risk` finding text | done |

### FR-04 — Isolated execution

| Requirement | Where | Status |
|---|---|---|
| Disposable runners | `internal/runner: ContainerRunner` (one container per side, `--rm`, no host mounts beyond workdir, no docker socket, no privileged mode) | done (container mode) |
| No production credentials | Runner env carries only the workdir + anonymous cache volume; check-run posting token stays on the control plane | done |
| Network restricted to approved registries | `--network dtm-egress` (allowlist network) in `ContainerRunner.Run` | done |
| CPU/memory/disk/wall-clock limits | `runner.Limits` shared by both sides; wall-clock enforced via context deadline; timeout exit recorded as 125 (runner-side failure, not regression) | done |
| Runner image digest, runtime version, command list | `runner.Result.ImageDigest`; report meta carries `RunnerImage`, `Commands` | partial (runtime version is carried per-image, not re-verified at run time) |
| Real execution in this environment | Requires a docker runtime; without one the system is degraded-mode only (see `docs/EXECUTION-ISOLATION-LIMITS.md`) | partial |

### FR-05 — Baseline comparison

| Requirement | Where | Status |
|---|---|---|
| Identical commands on base and candidate | `runner.RunSpec` per side with shared `Limits` and shared image pull | done (container mode) |
| Pre-existing vs candidate-only failure | `internal/risk` buckets `ConfirmedFailures` (candidate-only) vs review buckets; `ExecResult.Executed` guard | done |
| Compare test/build results, logs, duration | `risk.ExecResult` (Failures, DurationMS, per-side BuildOK/TestOK) | done |
| Flaky outcome by controlled repeats | Repeats are not orchestrated in this codebase | design-only |

### FR-06 — Risk report

| Requirement | Where | Status |
|---|---|---|
| Dimensions: graph reach, API usage, release-note signals, runtime compatibility, build/test outcome, security advisory | `internal/risk` finding dimensions (graph_reach, release_signal, security, runtime, execution, …) | done |
| Explain each score component | Every `core.Finding` carries dimension, severity, confidence, evidence list | done |
| No opaque single score | `internal/risk` design rule documented at package top; buckets + findings, no aggregate number | done |
| Confidence and missing-evidence warnings | `Finding.Confidence` (0..1), `report.Report.Missing[]` (e.g. feed status markers) | done |

### FR-07 — PR integration

| Requirement | Where | Status |
|---|---|---|
| Post concise report and CI status | `internal/ci: BuildComment`, `GitHub.PostCheckRun` (check-run payload with link to full report) | done (token-gated) |
| Link to full run details | `ci.Comment.ReportURL`; run summary's `reportUrl`/`graphDiffUrl` | done |
| Policy thresholds: block / warn | `ci.Policy` (block_on / warn_on), `POST /v1/policies` (`handlePolicy`), `ci.Decide` | done |
| No auto-merge by default | `ci.Decide` only maps to check states success/warning/failure; no merge call exists anywhere | done |

## API (§10)

| Endpoint | Where | Status |
|---|---|---|
| `POST /v1/repositories/connect` | `api/server.go: handleConnect` | done |
| `POST /v1/snapshots` | `api/server.go: handleSnapshot` | done |
| `POST /v1/upgrade-runs` | `api/server.go: handleUpgradeRun` (idempotency key support) | done |
| `GET /v1/upgrade-runs/{id}` | `api/server.go: handleGetRun` | done |
| `GET /v1/upgrade-runs/{id}/graph-diff` | `api/server.go: handleGraphDiff` | done |
| `GET /v1/upgrade-runs/{id}/report` | `api/server.go: handleReport` (JSON + markdown via Accept) | done |
| `POST /v1/policies` | `api/server.go: handlePolicy` | done |
| `POST /v1/webhooks/github` | `api/server.go: handleWebhook` (HMAC, closed default) | done |
| `POST /v1/cli-runs` | `api/server.go: handleCLIRun` | done |
| Idempotency keys | `upgradeRunRequest.IdempotencyKey` + `Runs.Get("idem-" + key)` | done |
| Project-configured execution command templates | `runner.CommandTemplate` | done |

## Non-functional requirements

| NFR | Where | Status |
|---|---|---|
| Reproducibility (source SHA, lockfile hash, runner image digest recorded) | `report.RunMeta`, `store.upgrade_runs` columns, `runner.Result.ImageDigest` | done |
| Isolation (untrusted lifecycle scripts only in disposable sandbox) | `internal/runner` boundary + boot-time mode logging in `cmd/dtm-server` | done |
| Fair comparison (equivalent base/candidate resources) | shared `runner.Limits`; single image pull for both sides | done |
| Performance visibility (queue + duration) | run summary fields, `risk.ExecResult.DurationMS`; no durable queue in this codebase (in-process execution) | partial |
| Scalability (workers scale independently) | design-only in this codebase | design-only |
| Security (no prod secrets; egress allowlist) | token-gated GitHub posting, webhook closed default, `dtm-egress` network, rate limiter | done |
| Explainability (each finding links to package/log/source/note) | `core.Evidence` on every finding | done |
| Reliability (failed infra ≠ regression) | runner exit code 125 convention, `Executed=false` flow, `ci.Decide` floors degraded runs to warning | done |

## Data entities (§7)

All 18 spec entities map to this codebase's model: `Repository`/`Manifest`/`LockfileSnapshot` → `store.projects`/`store.snapshots`; `Package`/`PackageVersion`/`DependencyEdge` → `core.PackageVersion`/`core.Edge`; `UpgradeCandidate`/`SandboxRun`/`CommandRun`/`TestCaseResult` → `pipeline.Revision`, `runner.RunSpec`/`Result`, `risk.ExecResult`; `Finding`/`RiskComponent` → `core.Finding`, `internal/risk`; `ReleaseNote`/`Advisory` → `core.ReleaseNote`/`core.Advisory`; `Policy` → `store.policies`/`ci.Policy`; `ExecutionEnvironment` → `runner.Limits` + `store.upgrade_runs.runner_mode`/`runner_image_digest`; `Artifact` → `report.RunMeta.ArtifactURL`/`RetentionDays` (retention is configured, not yet enforced by a janitor).
