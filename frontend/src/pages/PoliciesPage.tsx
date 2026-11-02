import { useState } from "react";

// Policies — block/warn thresholds per project. The UI is a form that
// writes to a localStorage-backed policy (server-side persistence of
// per-project policies is a documented limitation in this environment;
// the shape mirrors POST /v1/policies so the wiring is one-line when
// that endpoint's storage lands).

interface Policy {
  blockOn: string;
  warnOn: string;
}

const SEVERITIES = [
  "confirmed_failure",
  "strong_warning",
  "review_suggested",
  "no_issue_observed",
] as const;

const KEY = "dtm.policy";

export function PoliciesPage() {
  const [policy, setPolicy] = useState<Policy>(() => {
    try {
      const raw = localStorage.getItem(KEY);
      return raw ? (JSON.parse(raw) as Policy) : { blockOn: "confirmed_failure", warnOn: "strong_warning" };
    } catch {
      return { blockOn: "confirmed_failure", warnOn: "strong_warning" };
    }
  });

  function save() {
    localStorage.setItem(KEY, JSON.stringify(policy));
  }

  return (
    <section aria-labelledby="pol-h">
      <h2 id="pol-h">Policies</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 60 }}>
        How a run's highest severity maps to a CI check outcome. The
        default blocks on a confirmed candidate-only failure and warns on
        a strong warning. Upgrades are never merged automatically.
      </p>

      <div style={{ display: "grid", gap: 12, maxWidth: 380 }}>
        <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
          <span style={{ color: "var(--text-muted)" }}>Block on</span>
          <select
            value={policy.blockOn}
            onChange={(e) => setPolicy((p) => ({ ...p, blockOn: e.target.value }))}
          >
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {s.replace(/_/g, " ")}
              </option>
            ))}
          </select>
        </label>
        <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
          <span style={{ color: "var(--text-muted)" }}>Warn on</span>
          <select
            value={policy.warnOn}
            onChange={(e) => setPolicy((p) => ({ ...p, warnOn: e.target.value }))}
          >
            {SEVERITIES.map((s) => (
              <option key={s} value={s}>
                {s.replace(/_/g, " ")}
              </option>
            ))}
          </select>
        </label>
        <button onClick={save} style={{ justifySelf: "start" }}>
          Save policy
        </button>
      </div>

      <div className="notice" style={{ marginTop: 20, borderColor: "var(--info)" }}>
        Policy decisions run server-side (Go ci package); this screen
        configures the thresholds. CI check mapping is: block →
        check-run failure, warn → check-run warning, otherwise success.
        A degraded (analysis-only) run floors to warning whenever it has
        any finding, so a no-execution result never reads as "verified".
      </div>
    </section>
  );
}
