// Typed client for the DTM control-plane API. Every call returns a
// discriminated result so pages render explicit loading / error / empty
// states rather than silently showing a stale or fabricated value.
//
// Wire-format convention: the control plane emits camelCase JSON for the
// report/diff/run shapes consumed by this client (see the Go JSON tags in
// internal/core/model.go and internal/report/report.go).

export type Severity =
  | "confirmed_failure"
  | "strong_warning"
  | "review_suggested"
  | "no_issue_observed";

export interface DiffEntry {
  identity: { ecosystem: string; name: string; registry?: string };
  baseVersion: string;
  candVersion: string;
  category:
    | "added"
    | "removed"
    | "upgraded"
    | "downgraded"
    | "integrity_changed"
    | "source_changed"
    | "runtime_requirement_changed"
    | "peer_resolution_changed"
    | "unchanged";
  baseDirect: boolean;
  candDirect: boolean;
  baseIntegrity?: string;
  candIntegrity?: string;
  transitiveImpact: number;
  changedDirect: boolean;
}

export interface GraphDiff {
  entries: DiffEntry[];
  reachableChanged: number;
}

export interface Evidence {
  kind: string;
  url?: string;
  label: string;
}

export interface Finding {
  dimension: string;
  severity: Severity;
  package?: string;
  summary: string;
  confidence: number;
  evidence: Evidence[];
}

export interface ReleaseNote {
  identity: { ecosystem: string; name: string };
  version: string;
  sourceUrl: string;
  retrievedAt: string;
  signals: string[];
}

export interface Advisory {
  advisoryId: string;
  package: { ecosystem: string; name: string };
  severity: string;
  links: string[];
  source: string;
}

// Machine report (GET /report, JSON variant).
export interface Report {
  meta: {
    repoUrl: string;
    commitSha: string;
    baseLockSha: string;
    candLockSha: string;
    runnerImage: string;
    runnerMode: string;
    commands: string[];
    sampleData?: boolean;
  };
  diff: GraphDiff;
  findings: Finding[];
  buckets: {
    confirmedFailures: string[];
    strongWarnings: string[];
    reviewSuggested: string[];
    noIssueObserved: boolean;
  };
  highest: Severity;
  missing: string[];
  notes: ReleaseNote[];
  advisories: Advisory[];
}

// Run summary as returned by POST /v1/upgrade-runs.
export interface RunSummary {
  id: string;
  projectId: string;
  repoUrl: string;
  commitSha: string;
  runnerMode: string;
  executed: boolean;
  resultClass: Severity;
  advisoryFeed: string;
  changedCount: number;
  reportUrl: string;
  graphDiffUrl: string;
}

/**
 * `ApiResult` is the envelope every call resolves to. Pages must check
 * `ok` before touching `data` — this is what keeps empty/error states
 * honest instead of accidental.
 */
export type ApiResult<T> =
  | ({ ok: true; data: T } & { status: number })
  | { ok: false; status: number; error: string };

// Same-origin base; Vite's dev proxy forwards /v1 to the Go control plane.
const BASE = "";

async function request<T>(path: string, init?: RequestInit): Promise<ApiResult<T>> {
  try {
    const res = await fetch(BASE + path, {
      ...init,
      headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    });
    const isJson = res.headers.get("content-type")?.includes("application/json");
    let body: unknown = null;
    if (isJson) body = await res.json();
    if (!res.ok) {
      const msg =
        (body && typeof body === "object" && "error" in body
          ? String((body as { error?: string }).error)
          : res.statusText) || "request failed";
      return { ok: false, status: res.status, error: msg };
    }
    return { ok: true, data: body as T, status: res.status };
  } catch (e) {
    return { ok: false, status: 0, error: e instanceof Error ? e.message : String(e) };
  }
}

// ---- typed endpoints -------------------------------------------------------

export function connectRepository(body: {
  owner: string;
  repo: string;
  provider?: string;
  default_branch?: string;
}) {
  return request<{ project_id: string; registered: boolean }>("/v1/repositories/connect", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function startUpgradeRun(body: {
  project_id: string;
  base_sha: string;
  candidate_sha: string;
  [k: string]: unknown;
}) {
  return request<RunSummary>("/v1/upgrade-runs", {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function getRun(id: string) {
  return request<RunSummary>(`/v1/upgrade-runs/${id}`);
}

export function getGraphDiff(id: string) {
  return request<GraphDiff>(`/v1/upgrade-runs/${id}/graph-diff`);
}

export function getReport(id: string) {
  return request<Report>(`/v1/upgrade-runs/${id}/report`);
}

// Webhook-intake test for the Settings screen. Sends a synthetic event to
// POST /v1/webhooks/github. The server verifies its HMAC only when a
// secret is configured, so an honest 401 is the expected result until the
// operator arms DTM_GITHUB_WEBHOOK_SECRET — the UI surfaces that as an
// error state, not a fabricated success.
export function postWebhookTest(payload: { action: string; number: number }) {
  return request<{ event?: unknown; note: string }>("/v1/webhooks/github", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}
