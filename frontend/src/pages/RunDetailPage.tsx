import { useState } from "react";
import { getReport, type Finding, type Report } from "../api/client";
import { useApi } from "../hooks/useApi";
import { Empty, ErrorView, Loading } from "../components/StateViews";
import { SeverityBadge, severityLabel } from "../components/SeverityBadge";
import { SnapshotCarousel } from "../components/SnapshotCarousel";

// RunDetailPage — the focused engineering workbench for one upgrade run:
// header (repo, candidate commit, run state), dependency diff table,
// base/candidate comparison via the snapshot carousel, and the evidence
// inspector for a selected finding. This is the primary view the spec
// calls for: "comparing what changed and what failed", not metric cards.
export function RunDetailPage({ runId }: { runId: string }) {
  const { state, retry } = useApi<Report>(() => getReport(runId), [runId]);
  const [selected, setSelected] = useState<Finding | null>(null);

  if (state.status === "idle" || state.status === "loading") {
    return <Loading label={`Loading run ${runId}…`} />;
  }
  if (state.status === "error") {
    return <ErrorView title="Could not load run" detail={state.error} onRetry={retry} />;
  }

  const rep = state.data;
  return (
    <section aria-labelledby="run-h">
      <h2 id="run-h">Run {runId}</h2>

      {/* Header workbench strip: repo, candidate commit, run state. */}
      <div className="status-strip">
        <div>
          <span className="k">Repository</span>
          <span className="v">{rep.meta.repoUrl || "—"}</span>
        </div>
        <div>
          <span className="k">Candidate</span>
          <span className="v">{rep.meta.commitSha || "—"}</span>
        </div>
        <div>
          <span className="k">Runner</span>
          <span className="v">{rep.meta.runnerMode}</span>
        </div>
        <div>
          <span className="k">Result</span>
          <span className="v">
            <SeverityBadge severity={rep.highest} />
          </span>
        </div>
      </div>

      {rep.meta.sampleData ? (
        <div className="notice" style={{ marginTop: 12, borderColor: "var(--info)" }}>
          Sample data — this run was generated from demo fixtures and does
          not represent a real upgrade.
        </div>
      ) : null}
      {rep.meta.runnerMode === "degraded" ? (
        <div className="notice" style={{ marginTop: 12 }}>
          No isolated execution was performed. Graph, release-note and
          advisory analysis ran only; build and test commands did not.
        </div>
      ) : null}

      {/* Base/candidate comparison: horizontal carousel of version
          snapshots — the one place a carousel genuinely aids comparison.
          Falls back to the side-by-side table when there is nothing to
          carousel over. */}
      <SnapshotSection rep={rep} />

      <DiffTable rep={rep} onSelect={(pkg) => {
        const f = rep.findings.find((x) => x.package?.includes(pkg));
        setSelected(f ?? null);
      }} />

      <h3 style={{ margin: "24px 0 8px", color: "var(--text-secondary)" }}>Findings</h3>
      {rep.findings.length === 0 ? (
        <Empty title="No findings in the analysed dimensions." />
      ) : (
        <table className="dtm-table">
          <thead>
            <tr>
              <th>Dimension</th>
              <th>Severity</th>
              <th>Package</th>
              <th>Confidence</th>
              <th aria-label="Summary" />
            </tr>
          </thead>
          <tbody>
            {rep.findings.map((f, i) => (
              <tr
                key={i}
                style={{ cursor: "pointer" }}
                onClick={() => setSelected(f)}
                aria-selected={selected === f}
              >
                <td>{f.dimension}</td>
                <td><SeverityBadge severity={f.severity} /></td>
                <td className="mono">{f.package || "—"}</td>
                <td className="mono">{f.confidence.toFixed(2)}</td>
                <td style={{ color: "var(--text-secondary)" }}>{f.summary}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* Inspector: selected finding's evidence, linked out. */}
      {selected ? (
        <aside
          aria-label="Finding detail"
          style={{
            marginTop: 16,
            padding: 16,
            background: "var(--surface-1)",
            border: "1px solid var(--border-subtle)",
            borderRadius: "var(--radius-md)",
          }}
        >
          <div style={{ display: "flex", gap: 12, alignItems: "center", marginBottom: 8 }}>
            <SeverityBadge severity={selected.severity} />
            <span style={{ fontWeight: 500 }}>{selected.dimension}</span>
            <span className="mono" style={{ color: "var(--text-muted)" }}>
              {selected.package || "run-level"}
            </span>
          </div>
          <p style={{ color: "var(--text-secondary)" }}>{selected.summary}</p>
          <p className="mono" style={{ color: "var(--text-muted)", fontSize: "0.75rem", marginTop: 4 }}>
            Confidence {selected.confidence.toFixed(2)} · {severityLabel(selected.severity)}
          </p>
          {selected.evidence.length > 0 ? (
            <ul style={{ marginTop: 8, paddingLeft: 18, color: "var(--text-secondary)" }}>
              {selected.evidence.map((ev, i) => (
                <li key={i} className="mono" style={{ fontSize: "0.8125rem" }}>
                  {ev.kind} · {ev.label}
                  {ev.url ? (
                    <>
                      {" "}
                      <a href={ev.url} target="_blank" rel="noreferrer">
                        link
                      </a>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : (
            <p style={{ marginTop: 8, color: "var(--text-muted)" }}>No linked evidence.</p>
          )}
        </aside>
      ) : null}

      {rep.missing.length > 0 ? (
        <p style={{ marginTop: 16, color: "var(--text-muted)", fontSize: "0.8125rem" }}>
          Missing evidence: {rep.missing.join(", ")}
        </p>
      ) : null}
    </section>
  );
}

// SnapshotSection renders the base/candidate snapshot carousel when the
// diff has enough version moves to warrant it; otherwise a compact
// side-by-side.
function SnapshotSection({ rep }: { rep: Report }) {
  const changed = rep.diff.entries.filter((e) => e.category !== "unchanged");
  if (changed.length < 2) {
    return (
      <div style={{ margin: "16px 0" }}>
        <h3 style={{ color: "var(--text-secondary)", marginBottom: 8 }}>Comparison</h3>
        <Empty
          title="Not enough version moves to build a snapshot timeline."
          hint="The dependency diff table below still shows what changed."
        />
      </div>
    );
  }
  const items = changed.map((e, i) => ({
    id: `${e.identity.name}-${i}`,
    label: e.category,
    version: e.candVersion || e.baseVersion || "—",
    content: (
      <span className="mono" style={{ fontSize: "0.75rem", color: "var(--text-secondary)" }}>
        {e.identity.name} · {e.baseVersion || "absent"} → {e.candVersion || "absent"}
      </span>
    ),
  }));
  const [active, setActive] = useState(items[0].id);
  return (
    <div style={{ margin: "16px 0" }}>
      <h3 style={{ color: "var(--text-secondary)", marginBottom: 8 }}>Version snapshots</h3>
      <SnapshotCarousel items={items} activeId={active} onSelect={setActive} itemWidth={260} />
    </div>
  );
}

// DiffTable — the accessible table view of the graph diff (spec: tables as
// the accessible alternative to the graph view).
function DiffTable({ rep, onSelect }: { rep: Report; onSelect: (pkg: string) => void }) {
  const changed = rep.diff.entries.filter((e) => e.category !== "unchanged");
  return (
    <div style={{ margin: "16px 0" }}>
      <h3 style={{ color: "var(--text-secondary)", marginBottom: 8 }}>
        Dependency diff ({changed.length} changed · {rep.diff.reachableChanged} reachable)
      </h3>
      {changed.length === 0 ? (
        <Empty title="No package-level changes between base and candidate." />
      ) : (
        <table className="dtm-table">
          <thead>
            <tr>
              <th>Package</th>
              <th>From</th>
              <th>To</th>
              <th>Category</th>
              <th>Direct</th>
              <th>Reach</th>
            </tr>
          </thead>
          <tbody>
            {changed.map((e, i) => (
              <tr
                key={i}
                style={{ cursor: "pointer" }}
                onClick={() => onSelect(e.identity.name)}
              >
                <td className="mono">{e.identity.name}</td>
                <td className="mono">{e.baseVersion || "—"}</td>
                <td className="mono">{e.candVersion || "—"}</td>
                <td>{e.category.replace(/_/g, " ")}</td>
                <td>{e.candDirect ? "yes" : "no"}</td>
                <td className="mono">{e.transitiveImpact}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
