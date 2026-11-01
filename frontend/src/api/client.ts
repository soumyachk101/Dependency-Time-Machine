// Typed client for the DTM control-plane API. Every call returns a
// discriminated result so pages render explicit loading / error / empty
// states rather than silently showing a stale or fabricated value.

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
  transitiveImpact: number;
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
  package: string;
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
    runnerMode: "container" | "degraded" | string;
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
  project_id: string;
  repo_url: string;
  commit_sha: string;
  runner_mode: string;
  executed: boolean;
  result_class: Severity;
  advisory_feed: string;
  changed_count: number;
  report_url: string;
  graph_diff_url: string;
}

export interface ListableRun {
  id: string;
  repo_url: string;
  commit_sha: string;
  result_class: string;
}

/**
 * `ApiResult` is the envelope every call resolves to. Pages must check
 * `ok` before touching `data` — this is what keeps empty/error states
 * honest instead of accidental.
 */
export type ApiResult<T> =
  | ({ ok: true; data: T } & { status: number })
  | { ok: false; status: number; error: string };

const BASE = ""; // same-origin; Vite dev proxy routes /v1 to the Go server

async function request<T>(path: string, init?: RequestInit): Promise<ApiResult<T>> {
  try {
    const res = await fetch(path, {
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

// A minimal runs listing. The API's POST /v1/upgrade-runs returns the full
// summary; listing all runs for the timeline-of-versions view is derived
// client-side from a session's initiated runs until a GET /v1/upgrade-runs
// collection endpoint ships (documented limitation, see docs/known-limitations).
export const _BASE = BASE;
