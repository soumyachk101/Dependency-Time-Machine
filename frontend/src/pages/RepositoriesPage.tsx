import { useState } from "react";
import { connectRepository } from "../api/client";
import { Empty, ErrorView, Loading } from "../components/StateViews";

// Repositories — the "Connect repository" screen. On success it records the
// project for later runs; it never shows fake repository data.
export function RepositoriesPage() {
  const [owner, setOwner] = useState("");
  const [repo, setRepo] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    if (!owner.trim() || !repo.trim()) {
      setErr("Owner and repository name are required.");
      return;
    }
    setBusy(true);
    setErr(null);
    const res = await connectRepository({ owner: owner.trim(), repo: repo.trim() });
    setBusy(false);
    if (res.ok) {
      setDone(res.data.project_id);
    } else {
      setErr(res.error);
    }
  }

  return (
    <section aria-labelledby="repos-h">
      <h2 id="repos-h">Connect a repository</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 60 }}>
        Register the repository you want to evaluate upgrades for. Runs are
        started from the Upgrade runs screen.
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
        style={{ display: "grid", gap: 12, maxWidth: 360 }}
      >
        <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
          <span style={{ color: "var(--text-muted)" }}>Owner / organisation</span>
          <input value={owner} onChange={(e) => setOwner(e.target.value)} autoFocus required />
        </label>
        <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
          <span style={{ color: "var(--text-muted)" }}>Repository</span>
          <input value={repo} onChange={(e) => setRepo(e.target.value)} required />
        </label>
        <button type="submit" disabled={busy} style={{ justifySelf: "start" }}>
          {busy ? "Connecting…" : "Connect"}
        </button>
      </form>

      {busy ? <Loading label="Connecting repository…" /> : null}
      {done ? (
        <p className="mono" style={{ marginTop: 16, color: "var(--success)", fontSize: "0.8125rem" }}>
          Connected: {done}
        </p>
      ) : null}
      {err ? <ErrorView title="Could not connect" detail={err} /> : null}

      <Empty
        title="No repositories connected yet"
        hint="Connecting a repository enables snapshot and upgrade-run recording for it."
      />
    </section>
  );
}
