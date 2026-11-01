# Execution isolation limits

This file states precisely what the execution-isolation layer does and
does not do, so no report, CI check or UI surface can ever claim
isolated builds/tests ran when they did not.

## The two modes

`internal/runner` implements one interface with two concrete runners:

### Container mode — the documented real-isolation path

The container-backed runner is the only path in this system that
executes install/build/test commands. It satisfies the spec's worker
isolation requirements (§6): one disposable container per side, no
production credentials, no host home directory or docker socket mounts,
no privileged mode, egress restricted to the allowlist network
(`dtm-egress`), shared resource quotas for base and candidate
(`DTM_RUNNER_CPU_LIMIT`, `DTM_RUNNER_MEMORY_LIMIT`,
`DTM_RUNNER_DISK_LIMIT`, `DTM_RUNNER_WALL_CLOCK_LIMIT`), runner image
digest recorded on every result, and automatic cleanup (`--rm`).

This path is only ever reached when a docker runtime is actually
available on the host (probed by `runner.New` in `auto` mode, required in
`container` mode).

### Degraded mode — safe, analysis-only

When no container runtime is available, the system selects the
**DegradedRunner**. In this mode:

- The system performs **only manifest-level and graph-level analysis**:
  parsing, deterministic diffing, advisory matching (against whichever
  feed is reachable), release-note retrieval, and report rendering.
- **No repository code and no package lifecycle script is executed on
  the control-plane host.** No process is ever spawned that could run
  untrusted code.
- Every result carries `Executed: false`. This flag flows through the
  result normaliser and the CI decision, so a degraded run:
  - is labelled **`safe-degraded`** in run metadata (`runnerMode`),
  - can never produce a CI "success" that implies execution happened —
    `ci.Decide` floors any finding to at least a warning,
  - never reports infrastructure absence as a dependency regression
    (spec NFR "Reliability").

## What degraded mode never claims

A `safe-degraded` run's report, CI check and machine JSON state exactly
that **isolated build/test execution did not happen**. Build outcome,
test outcome and resource-usage dimensions are absent from findings, and
the missing-evidence list names them. No execution dimension may be
presented as passing or failing in this mode.

## Mode selection and auditability

- `DTM_RUNNER_MODE=auto` (default): probe docker; use the container
  runner when present, otherwise fall back to degraded. The selection —
  and the reason — is logged at boot (`cmd/dtm-server`), so the audit
  trail always shows which boundary was active.
- `DTM_RUNNER_MODE=container`: require docker; boot fails otherwise. It
  never silently degrades.
- `DTM_RUNNER_MODE=degraded`: explicitly analysis-only; no probe is run.

`DTM_DISABLE_DOCKER_PROBE=1` forces degraded selection even when docker
is present (used by tests and CI where the host must stay side-effect
free).

## Out of scope

Firecracker microVMs (spec §5's stronger-isolation option), signed
worker images, NATS-backed worker dispatch, and S3 artifact retention
janitors are design-time recommendations, not implemented in this
codebase — see `docs/KNOWN-LIMITATIONS.md`.
