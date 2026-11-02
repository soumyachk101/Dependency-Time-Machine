import { useState } from "react";

// Environments — surfaces the execution-isolation boundary for the
// operator: which runner mode is active (container vs degraded), the
// shared resource limits that keep base and candidate fair, and a plain
// statement of what degraded mode does and does not do. This is the UI's
// home for the spec's hard security constraint, kept visible rather than
// buried in docs.

interface EnvState {
  mode: "container" | "degraded";
  image: string;
  cpu: string;
  memory: string;
  disk: string;
  wall: string;
  egress: string;
}

const DEFAULTS: EnvState = {
  mode: "degraded",
  image: "ghcr.io/ensembyte/dtm-runner:1.0.0",
  cpu: "2",
  memory: "4g",
  disk: "10g",
  wall: "30m",
  egress: "registry proxy allowlist (npmjs, pypi, crates.io)",
};

const KEY = "dtm.env";

export function EnvironmentsPage() {
  const [env, setEnv] = useState<EnvState>(() => {
    try {
      const raw = localStorage.getItem(KEY);
      return raw ? { ...DEFAULTS, ...(JSON.parse(raw) as Partial<EnvState>) } : DEFAULTS;
    } catch {
      return DEFAULTS;
    }
  });

  function save() {
    localStorage.setItem(KEY, JSON.stringify(env));
  }

  return (
    <section aria-labelledby="env-h">
      <h2 id="env-h">Environments</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 60 }}>
        Runner configuration for isolated execution. Base and candidate
        always share the limits below — that parity is what makes the
        comparison fair.
      </p>

      <div className="notice" style={{ marginBottom: 16 }}>
        {env.mode === "degraded" ? (
          <>
            <strong>Safe degraded mode is active.</strong> Manifest- and
            graph-level analysis only: parsing, diffing, advisory
            matching and report rendering. No repository or package code
            is executed on the control-plane host. Build/test outcomes
            are not available in this mode and are labelled as such on
            every report.
          </>
        ) : (
          <>
            <strong>Container mode.</strong> Install/build/test commands
            run in a disposable, resource-capped, egress-restricted
            container per side. Untrusted lifecycle scripts never touch
            the control-plane host.
          </>
        )}
      </div>

      <div style={{ display: "grid", gap: 12, maxWidth: 420 }}>
        <Labeled
          label="Runner mode"
          value={
            <select
              value={env.mode}
              onChange={(e) => setEnv({ ...env, mode: e.target.value as EnvState["mode"] })}
            >
              <option value="degraded">degraded (analysis-only)</option>
              <option value="container">container</option>
            </select>
          }
        />
        <Labeled
          label="Runner image"
          value={
            <input className="mono" value={env.image} onChange={(e) => setEnv({ ...env, image: e.target.value })} />
          }
        />
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
          <Labeled label="CPU" value={<input className="mono" value={env.cpu} onChange={(e) => setEnv({ ...env, cpu: e.target.value })} />} />
          <Labeled label="Memory" value={<input className="mono" value={env.memory} onChange={(e) => setEnv({ ...env, memory: e.target.value })} />} />
          <Labeled label="Disk" value={<input className="mono" value={env.disk} onChange={(e) => setEnv({ ...env, disk: e.target.value })} />} />
          <Labeled label="Wall clock" value={<input className="mono" value={env.wall} onChange={(e) => setEnv({ ...env, wall: e.target.value })} />} />
        </div>
        <Labeled
          label="Egress"
          value={<input value={env.egress} onChange={(e) => setEnv({ ...env, egress: e.target.value })} />}
        />
        <button onClick={save} style={{ justifySelf: "start" }}>
          Save environment
        </button>
      </div>
    </section>
  );
}

function Labeled({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
      <span style={{ color: "var(--text-muted)" }}>{label}</span>
      {value}
    </label>
  );
}
