// Session-scoped run records, shared by the Runs list and the
// Dependency graph screen so they agree on what "a run" means. Scoped to
// the browser session on purpose — a refresh clears it, which is the
// honest representation of "runs started this session" rather than
// fabricated history.
import type { RunSummary } from "../api/client";

const KEY = "dtm.session.runs";

export function loadSessionRuns(): RunSummary[] {
  try {
    const raw = sessionStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as RunSummary[]) : [];
  } catch {
    return [];
  }
}

export function saveSessionRuns(runs: RunSummary[]) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(runs));
  } catch {
    // Quota / private-mode failures are non-fatal: the in-memory list
    // still works for the lifetime of this page.
  }
}
