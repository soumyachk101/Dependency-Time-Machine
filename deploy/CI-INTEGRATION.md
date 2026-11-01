# DTM in your own CI

Concrete example of wiring Dependency Time Machine into **your product
repo's** CI: on a PR that bumps dependencies, run an analysis against the
PR's manifest/lockfile, post a risk-report comment + status check, and
**fail the gate on the "high" risk buckets**.

All env var names, endpoints, and risk-bucket names below are copied from
the actual DTM code, not invented:

- Endpoint: `POST /v1/upgrade-runs` (`internal/api/server.go`).
- Report shape: `GET /v1/upgrade-runs/{id}/report` (Accept
  `application/json`) → the machine report JSON. Its top-level keys are
  `meta`, `diff`, `findings`, `buckets`, `highest`, `missing`, `notes`,
  `advisories` (camelCase; `internal/report/report.go` `JSON()`). The
  bucket fields (`core.RiskBuckets`, `internal/core/model.go`) are:
  - `buckets.confirmedFailures: string[]`  → `confirmed_failure`
  - `buckets.strongWarnings: string[]`     → `strong_warning`
  - `buckets.reviewSuggested: string[]`    → `review_suggested`
  - `buckets.noIssueObserved: boolean`     → `no_issue_observed`
  - plus top-level `highest` = the max severity across findings.

**"High" risk = `confirmedFailures` + `strongWarnings` are non-empty**
(== `highest` is `confirmed_failure` or `strong_warning`). This mirrors
DTM's own `ci.DefaultPolicy()` (`block on confirmed_failure, warn on
strong_warning`, `internal/ci/ci.go`) — so failing on those two buckets
is the product-spec gate. `review_suggested` stays a warning, not a
block, unless you tighten it.

## The workflow

Save as `.github/workflows/dtm-upgrade-gate.yml`. This assumes your DTM
control plane is reachable at `DTM_SERVER_URL` (e.g. the compose dev host
or a managed deployment). It talks to the server over HTTP — no DTM CLI
needed — and posts the check + comment with your own `GITHUB_TOKEN`, since
DTM's built-in check-run posting is off by default (no token on the
control plane; see `cmd/dtm-server` `githubTokenOrNil`).

```yaml
name: DTM upgrade gate

on:
  pull_request:
    types: [opened, synchronize, reopened]

# Only run the heavy dependency analysis when the PR actually touches a
# manifest/lockfile, so you don't pay for every unrelated PR.
# Adjust the globs to whatever your project tracks.
#
# NOTE: the `pull_request` event has no `changed_files` property; the
# filter step below computes it locally with `git diff` between the base
# SHA and the PR head (checkout uses fetch-depth: 0, so both sides are
# present).
env:
  DTM_SERVER_URL: ${{ vars.DTM_SERVER_URL }}     # e.g. http://127.0.0.1:8080
  # The DTM project identifier for this repo (owner/repo), DTM convention:
  DTM_PROJECT_ID: ${{ github.repository }}

permissions:
  pull-requests: write   # posting the PR comment
  statuses: write       # posting the commit status check (the gate)
  contents: read         # checkout

jobs:
  dtm-upgrade:
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          # Fetch the PR head and its merge base so we can read both sides.
          fetch-depth: 0
          ref: ${{ github.event.pull_request.head.sha }}
          persist-credentials: false

      - name: Run DTM only if a manifest/lockfile changed
        id: filter
        run: |
          set -euo pipefail
          CHANGED=$(git diff --name-only \
            "${{ github.event.pull_request.base.sha }}...HEAD" 2>/dev/null || \
            git diff --name-only origin/${{ github.event.pull_request.base.ref }}...HEAD)
          echo "$CHANGED"
          if echo "$CHANGED" | grep -qE '^(package\.json|package-lock\.json|pnpm-lock\.yaml|yarn\.lock|requirements(\.txt)?(\.[0-9.]+)?|setup\.py|pyproject\.toml|uv\.lock|Cargo\.toml|Cargo\.lock)$'; then
            echo "manifest_changed=true" >> "$GITHUB_OUTPUT"
          fi

      - name: Load manifest + lockfile at base and candidate
        id: prep
        if: steps.filter.outputs.manifest_changed == 'true'
        run: |
          set -euo pipefail
          BASE=${{ github.event.pull_request.base.sha }}
          CAND=${{ github.event.pull_request.head.sha }}
          # Candidate (HEAD) files come from the workdir.
          cp package.json manifest.candidate
          [ -f package-lock.json ] && cp package-lock.json lockfile.candidate || :
          # Base side straight from git.
          git show "$BASE:package.json" > manifest.base
          git show "$BASE:package-lock.json" > lockfile.base 2>/dev/null || :

      - name: Register repository with DTM (idempotent)
        if: steps.filter.outputs.manifest_changed == 'true'
        run: |
          set -euo pipefail
          OWNER=${GITHUB_REPOSITORY%%/*}; REPO=${GITHUB_REPOSITORY##*/}
          curl -fsS -X POST "$DTM_SERVER_URL/v1/repositories/connect" \
            -H 'Content-Type: application/json' \
            -d "{\"owner\":\"$OWNER\",\"repo\":\"$REPO\",\"provider\":\"github\"}"

      - name: Run the DTM upgrade analysis
        if: steps.filter.outputs.manifest_changed == 'true'
        id: run
        run: |
          set -euo pipefail
          # `parser.Files` is map[string][]byte — the JSON wire format is
          # a map from filename to base64. `base_lockfile` /
          # `candidate_lockfile` are raw bytes, also base64 in JSON.
          BASE_MANIFEST_B64=$(base64 < manifest.base)
          CAND_MANIFEST_B64=$(base64 < manifest.candidate)
          BASE_LOCK_B64=$( [ -s lockfile.base ] && base64 < lockfile.base || true )
          CAND_LOCK_B64=$( [ -s lockfile.candidate ] && base64 < lockfile.candidate || true )
          RESP=$(curl -fsS -X POST "$DTM_SERVER_URL/v1/upgrade-runs" \
            -H 'Content-Type: application/json' \
            -d @- <<EOF
          {
            "project_id": "${DTM_PROJECT_ID}",
            "repo_url": "${{ github.event.pull_request.head.repo.html_url }}",
            "base_sha":   "${{ github.event.pull_request.base.sha }}",
            "candidate_sha": "${{ github.event.pull_request.head.sha }}",
            "base_files":    { "package.json": "${BASE_MANIFEST_B64}" },
            "candidate_files": { "package.json": "${CAND_MANIFEST_B64}" },
            "base_lockfile":     "${BASE_LOCK_B64}",
            "candidate_lockfile": "${CAND_LOCK_B64}",
            "notes_enabled": true,
            "advisory_online": true,
            "idempotency_key": "pr-${{ github.event.pull_request.number }}-${{ github.event.pull_request.head.sha }}"
          }
          EOF
          )
          echo "$RESP"
          RUN_ID=$(echo "$RESP" | jq -r '.id')
          echo "run_id=$RUN_ID" >> "$GITHUB_OUTPUT"

      - name: Fetch the risk report
        if: steps.filter.outputs.manifest_changed == 'true' && steps.run.outputs.run_id != ''
        id: report
        run: |
          set -euo pipefail
          curl -fsS "$DTM_SERVER_URL/v1/upgrade-runs/${{ steps.run.outputs.run_id }}/report" \
            -H 'Accept: application/json' -o report.json
          cat report.json

      - name: Evaluate the gate (fail on high-risk buckets)
        if: steps.filter.outputs.manifest_changed == 'true' && steps.run.outputs.run_id != ''
        id: gate
        run: |
          set -euo pipefail
          # Machine report JSON: top-level `highest` + `buckets` (camelCase
          # fields; see internal/report + internal/core model.go).
          HIGH=$(jq -r '.buckets.confirmedFailures | length' report.json)
          WARN=$(jq -r '.buckets.strongWarnings | length' report.json)
          HIGHEST=$(jq -r '.highest' report.json)
          echo "high buckets: confirmed_failures=$HIGH strong_warnings=$WARN (highest=$HIGHEST)"
          # "High" = confirmed_failures + strong_warnings non-empty
          # (== the default policy's block+warn tiers in internal/ci).
          if [ "$((HIGH + WARN))" -gt 0 ]; then
            echo "blocking=true" >> "$GITHUB_OUTPUT"
            echo "verdict=block" >> "$GITHUB_OUTPUT"
          else
            echo "blocking=false" >> "$GITHUB_OUTPUT"
            echo "verdict=pass" >> "$GITHUB_OUTPUT"
          fi

      - name: Post the risk-report comment + status check
        if: steps.filter.outputs.manifest_changed == 'true' && steps.run.outputs.run_id != ''
        run: |
          set -euo pipefail
          VERDICT=${{ steps.gate.outputs.verdict }}
          STATE=success
          if [ "$VERDICT" = "block" ]; then STATE=failure; fi
          REPORT_URL="$DTM_SERVER_URL/v1/upgrade-runs/${{ steps.run.outputs.run_id }}/report"

          # 1) PR status check (the gate).
          curl -fsS -X POST \
            "https://api.github.com/repos/${GITHUB_REPOSITORY}/statuses/${{ github.event.pull_request.head.sha }}" \
            -H "Authorization: Bearer $GITHUB_TOKEN" -H 'Content-Type: application/json' \
            -d "{
              \"state\": \"${STATE}\",
              \"target_url\": \"${REPORT_URL}\",
              \"description\": \"DTM ${VERDICT}\",
              \"context\": \"dtm/upgrade-gate\"
            }" || true

          # 2) Concise comment with the top findings + a link to the full report.
          # NOTE: the machine report has no `reportUrl` field (that key is on
          # the run summary, not the report body) — use the URL we built above.
          {
            echo "## Dependency Time Machine — upgrade risk"
            echo
            jq -r '
              "- highest severity: \(.highest)"
              + "- confirmed failures: \(.buckets.confirmedFailures | length)"
              + "- strong warnings: \(.buckets.strongWarnings | length)"
              + "- review suggested: \(.buckets.reviewSuggested | length)"
            ' report.json
            echo
            echo "[Full report and evidence → ${REPORT_URL}]"
          } > dtm-comment.md
          gh api repos/$GITHUB_REPOSITORY/issues/${{ github.event.pull_request.number }}/comments \
            -f body="$(cat dtm-comment.md)" --jq .html_url || true

      - name: Fail the gate on high-risk buckets
        if: steps.filter.outputs.manifest_changed == 'true' && steps.run.outputs.run_id != '' && steps.gate.outputs.blocking == 'true'
        run: exit 1
```

### Notes for adapting it

- **Risk-bucket names are `confirmed_failures`, `strong_warnings`,
  `review_suggested`, `no_issue_observed`** on the wire (camelCase),
  which are the `confirmed_failure` / `strong_warning` / `review_suggested`
  / `no_issue_observed` severities from `internal/risk`. Don't invent
  "high"/"medium" buckets — DTM doesn't have them.
- **Gate policy**: failing on `confirmed_failures + strong_warnings`
  matches DTM's default policy exactly. To also block on review-level
  findings, add `reviewSuggested` to the `HIGH` count — but understand
  that's stricter than DTM's own default.
- **Execution caveat**: `POST /v1/upgrade-runs` runs the *analysis*
  pipeline (manifest/graph diff, release notes, advisories) in
  `degraded`/analysis-only mode on the control plane — it does **not**
  run your build/tests. If you need DTM to also execute the candidate's
  build/test in an isolated container, that's the `runner` layer
  (`DTM_RUNNER_MODE=container`), a separate concern from this gate.
- **`base_files`/`candidate_files`** are keyed by manifest path as the
  parser sees it (`internal/parser`). The example assumes a single
  `package.json`; extend the object for monorepos. `base_lockfile` /
  `candidate_lockfile` are raw byte arrays (base64 in the JSON body).
- **`gh` CLI** is preinstalled on `ubuntu-latest`; the status check uses
  raw `curl` with `GITHUB_TOKEN` (default repo token) so it works without
  the CLI.
- If your team prefers the DTM **server** to post the check itself, that
  path needs a `DTM_GITHUB_TOKEN`/App configured on the control plane —
  but in this checkout that is off by default and returns
  `skipped_no_token`, so client-side posting (as above) is the reliable
  one.
