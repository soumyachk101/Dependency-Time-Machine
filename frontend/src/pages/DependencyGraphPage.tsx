import { useState } from "react";

// Dependency graph — the progressively-disclosed tree view of the changed
// packages from a run's graph diff, with the table as the accessible
// alternative. This screen is a *graph view of the diff*, driven by real
// run data (fetched here for the most recent session run); when no run
// exists it says so rather than inventing a tree.
//
// Full node-adjacency graph rendering is a documented limitation in the
// MVP (see docs/known-limitations.md): the tree here is built from diff
// entries + transitive reach, which is the comparison-relevant subset.
import { getReport } from "../api/client";
import { useApi } from "../hooks/useApi";
import { Empty, ErrorView, Loading } from "../components/StateViews";
import { loadSessionRuns } from "./runsSession";

export function DependencyGraphPage() {
  const runs = loadSessionRuns();
  const [runId, setRunId] = useState(runs[0]?.id ?? "");
  const { state, retry } = useApi(() => getReport(runId), [runId]);

  return (
    <section aria-labelledby="graph-h">
      <h2 id="graph-h">Dependency graph</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 60 }}>
        Changed packages and their transitive reach, from a run's graph
        diff. Progressive disclosure: overview first, expand a package to
        its neighbourhood.
      </p>

      {runs.length > 0 ? (
        <label style={{ display: "inline-flex", gap: 8, fontSize: "0.8125rem", marginBottom: 12 }}>
          <span style={{ color: "var(--text-muted)" }}>Run</span>
          <select value={runId} onChange={(e) => setRunId(e.target.value)}>
            {runs.map((r) => (
              <option key={r.id} value={r.id}>
                {r.id}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <p className="mono" style={{ color: "var(--text-muted)", marginBottom: 12, fontSize: "0.8125rem" }}>
          No runs in this session to graph yet.
        </p>
      )}

      {state.status === "idle" || state.status === "loading" ? (
        <Loading label="Loading graph…" />
      ) : state.status === "error" ? (
        <ErrorView title="Could not load graph" detail={state.error} onRetry={retry} />
      ) : (
        <GraphView entries={state.data.diff.entries} />
      )}
    </section>
  );
}

// GraphView renders the changed-node tree as an indented, expandable list
// (the "overview first, expand a neighbourhood" disclosure) with a
// table toggle for the accessible alternative.
function GraphView({
  entries,
}: {
  entries: {
    identity: { name: string };
    category: string;
    transitiveImpact: number;
    baseVersion: string;
    candVersion: string;
  }[];
}) {
  const [showTable, setShowTable] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const changed = entries.filter((e) => e.category !== "unchanged");

  if (changed.length === 0) {
    return <Empty title="No changed packages in this run." />;
  }

  const toggle = (name: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  return (
    <div>
      <div style={{ display: "flex", gap: 8, marginBottom: 12 }}>
        <button onClick={() => setShowTable(false)} style={{ opacity: showTable ? 0.6 : 1 }}>
          Tree
        </button>
        <button onClick={() => setShowTable(true)} style={{ opacity: showTable ? 1 : 0.6 }}>
          Table
        </button>
      </div>

      {showTable ? (
        <table className="dtm-table">
          <thead>
            <tr>
              <th>Package</th>
              <th>From</th>
              <th>To</th>
              <th>Category</th>
              <th>Reach</th>
            </tr>
          </thead>
          <tbody>
            {changed.map((e, i) => (
              <tr key={i}>
                <td className="mono">{e.identity.name}</td>
                <td className="mono">{e.baseVersion || "—"}</td>
                <td className="mono">{e.candVersion || "—"}</td>
                <td>{e.category.replace(/_/g, " ")}</td>
                <td className="mono">{e.transitiveImpact}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <ul
          role="tree"
          aria-label="Changed packages"
          style={{ listStyle: "none", padding: 0, margin: 0 }}
        >
          {changed.map((e, i) => (
            <li key={i}>
              <button
                onClick={() => toggle(e.identity.name)}
                aria-expanded={expanded.has(e.identity.name)}
                style={{
                  width: "100%",
                  textAlign: "left",
                  border: "none",
                  background: "transparent",
                  color: "inherit",
                  padding: "4px 8px",
                  display: "flex",
                  gap: 8,
                }}
              >
                <span className="mono" style={{ color: "var(--text-muted)" }}>
                  {expanded.has(e.identity.name) ? "▾" : "▸"}
                </span>
                <span className="mono">{e.identity.name}</span>
                <span className="mono" style={{ color: "var(--text-muted)" }}>
                  {e.category.replace(/_/g, " ")}
                </span>
              </button>
              {expanded.has(e.identity.name) ? (
                <div
                  style={{
                    marginLeft: 20,
                    padding: "6px 0",
                    borderLeft: "1px solid var(--border-subtle)",
                    paddingLeft: 12,
                  }}
                >
                  <div className="mono" style={{ fontSize: "0.8125rem" }}>
                    {e.baseVersion || "absent"} → {e.candVersion || "absent"}
                  </div>
                  <div style={{ color: "var(--text-muted)", fontSize: "0.75rem", marginTop: 2 }}>
                    {e.transitiveImpact} transitive dependants
                  </div>
                </div>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
