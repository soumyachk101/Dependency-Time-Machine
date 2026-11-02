import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import {
  getRun,
  startUpgradeRun,
  type RunSummary,
} from "../api/client";
import { Empty, ErrorView } from "../components/StateViews";
import { SeverityBadge } from "../components/SeverityBadge";
import { loadSessionRuns, saveSessionRuns } from "./runsSession";

export function RunsPage() {
  const [runs, setRuns] = useState<RunSummary[]>(loadSessionRuns);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [projectId, setProjectId] = useState("");
  const [baseSha, setBaseSha] = useState("");
  const [candSha, setCandSha] = useState("");

  useEffect(() => {
    saveSessionRuns(runs);
  }, [runs]);

  async function start() {
    setBusy(true);
    setErr(null);
    const res = await startUpgradeRun({
      project_id: projectId,
      base_sha: baseSha,
      candidate_sha: candSha,
    });
    setBusy(false);
    if (res.ok) {
      setRuns((prev) => [res.data, ...prev]);
      // Jump straight to the run's workbench.
      window.location.hash = `/runs/${res.data.id}`;
    } else {
      setErr(res.error);
    }
  }

  async function refresh(id: string) {
    const res = await getRun(id);
    if (res.ok) setRuns((prev) => prev.map((r) => (r.id === id ? res.data : r)));
  }

  return (
    <section aria-labelledby="runs-h">
      <h2 id="runs-h">Upgrade runs</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 60 }}>
        Start a base-vs-candidate evaluation, then open it as a workbench.
        Runs listed here are the ones started in this session.
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          void start();
        }}
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
          gap: 12,
          padding: 16,
          background: "var(--surface-1)",
          border: "1px solid var(--border-subtle)",
          borderRadius: "var(--radius-md)",
          marginBottom: 20,
        }}
      >
        <Field label="Project">
          <input value={projectId} onChange={(e) => setProjectId(e.target.value)} placeholder="github/owner/repo" />
        </Field>
        <Field label="Base SHA">
          <input value={baseSha} onChange={(e) => setBaseSha(e.target.value)} placeholder="main @ 4c9f…" className="mono" />
        </Field>
        <Field label="Candidate SHA">
          <input value={candSha} onChange={(e) => setCandSha(e.target.value)} placeholder="renovate/pin 2.x @ 9a2e…" className="mono" />
        </Field>
        <div style={{ alignSelf: "end" }}>
          <button type="submit" disabled={busy || !projectId || !baseSha || !candSha}>
            {busy ? "Starting…" : "Start run"}
          </button>
        </div>
      </form>

      {err ? <ErrorView title="Run failed to start" detail={err} onRetry={() => void start()} /> : null}

      {runs.length === 0 ? (
        <Empty
          title="No runs started in this session yet"
          hint="Use the form above to start one; it will appear here and in its own workbench."
        />
      ) : (
        <table className="dtm-table">
          <thead>
            <tr>
              <th>Run</th>
              <th>Repository</th>
              <th>Candidate commit</th>
              <th>Runner</th>
              <th>Result</th>
              <th>Changed</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id}>
                <td className="mono">{r.id}</td>
                <td>{r.repoUrl || r.projectId}</td>
                <td className="mono">{r.commitSha || "—"}</td>
                <td className="mono">{r.runnerMode}</td>
                <td>
                  <SeverityBadge severity={r.resultClass} />
                </td>
                <td className="mono">{r.changedCount}</td>
                <td style={{ whiteSpace: "nowrap" }}>
                  <a href={`#/runs/${r.id}`}>Open</a>
                  <button onClick={() => void refresh(r.id)} style={{ marginLeft: 8, fontSize: "0.75rem" }}>
                    Refresh
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
      <span style={{ color: "var(--text-muted)" }}>{label}</span>
      {children}
    </label>
  );
}
