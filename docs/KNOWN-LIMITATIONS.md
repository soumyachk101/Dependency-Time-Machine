# Known limitations — Dependency Time Machine

These limitations are real and intentional. Each is surfaced by the
system itself (status markers, log lines, UI notices) rather than hidden
behind a plausible-looking result.

## 1. Fonts are not vendored as binary files

The product's typography (Space Grotesk for display/UI, Geist Mono for
identifiers, versions, timings and counts) is **not** vendored in this
repository as binary `.woff2` files. The self-hosting mechanism is the
**`@fontsource` package set with system fallbacks**:

- `frontend/src/styles/fonts/README.md` documents the exact licensed
  files to drop in place and the `@font-face` registration to add in
  `tokens.css` once the licensed files are present.
- Until the files (or the corresponding `@fontsource` packages) are
  added to `package.json` and installed, the CSS fallback stacks in
  `tokens.css` apply — Helvetica Neue / system sans for display and
  body, SFMono / `ui-monospace` for code.
- Layout is sized in `rem`, so nothing breaks when the custom faces are
  added later; no layout change is required.

This is a licensing decision, not a bug: binary font files are only
shipped when their licences permit it.

## 2. Advisory and release-note fetch is network-dependent

Security-advisory evidence (`internal/advisory`) and release-note
retrieval (`internal/relnotes`) depend on outbound network access to the
configured endpoints (OSV by default, `DTM_OSV_ENDPOINT`):

- **Offline fallback:** a bundled offline advisory dataset
  (`advisory.offlineAdvisories`) is embedded so a run works with no
  network at all. Its entries carry an `offline:<id>` source marker so
  reports always show which source was actually used.
- **Honest status, never fabrication:** when the live feed is
  unreachable, or credentials are absent, the affected finding is
  labelled with an explicit status — `feed_unreachable`,
  `offline_dataset_only`, or `awaiting_credentials`. Advisories are
  **never** invented to fill the gap: only entries with a concrete
  source (`osv:<id>` or `offline:<id>`) count as evidence.
- Release notes carry `RetrievedAt` (an actual timestamp) or
  `unretrieved`, plus a `retrieval_unavailable` signal when the fetch
  did not happen. The source URL is still recorded because it is
  independently verifiable by a human.
- `DTM_ENABLE_ONLINE_ADVISORIES=false` forces the offline-only path; the
  run's advisory feed status reflects that on every report.

## 3. Execution isolation is conditional on a container runtime

See `docs/EXECUTION-ISOLATION-LIMITS.md` for the full statement. In
short: the container-backed runner is the only real-isolation path, and
without a docker runtime the system runs manifest-level and graph-level
analysis only, labelled `safe-degraded` — it never claims isolated
build/test execution happened.

## 4. GitHub integration is token-gated and partially out of scope

- Webhook intake (`POST /v1/webhooks/github`) rejects every event
  unless `DTM_GITHUB_WEBHOOK_SECRET` is configured (closed default).
- Check-run posting requires `DTM_GITHUB_TOKEN`. The GitHub App
  installation-token exchange (App ID + private key file → installation
  token) is **not** wired in this environment; until it is, posting
  reports `skipped_no_token` rather than faking success.
- Consuming a Renovate/Dependabot PR branch directly (fetching the
  branch's files) is not implemented; callers supply the base and
  candidate manifest/lockfile contents to `POST /v1/upgrade-runs`.

## 5. Persistence is in-process by default

The API's run records live in `api.InMemoryRunStore` (mutex-guarded
map). The persistent `store` layer (SQLite/Postgres) is opened only when
`DTM_DATA_DRIVER` is non-memory, and in the MVP the in-memory store is
the one the handlers read/write — so run records do not survive a
restart. This is a documented gap, not silent data loss: the report
JSON and markdown are the canonical artifacts.

## 6. No flaky-test orchestration

Flaky outcome detection by controlled repeat runs (spec FR-05) is
design-only in this codebase: `risk.ExecResult` models per-side results,
but repeat-run orchestration is not implemented.

## 7. The offline advisory dataset is a placeholder shape

`advisory.offlineAdvisories` is an intentionally small, documented
sample set. The shape (identity-keyed, version-range-checked) is what
the refresh pipeline exercises; concrete production entries are
shipped by a separate refresh process. An empty match means "no
advisories in the shipped dataset", which the report states as such —
it is never presented as "package is safe".
