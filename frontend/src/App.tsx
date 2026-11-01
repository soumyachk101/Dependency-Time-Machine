import { useEffect, useState } from "react";
import { RepositoriesPage } from "./pages/RepositoriesPage";
import { RunsPage } from "./pages/RunsPage";
import { RunDetailPage } from "./pages/RunDetailPage";
import { DependencyGraphPage } from "./pages/DependencyGraphPage";
import { PoliciesPage } from "./pages/PoliciesPage";
import { EnvironmentsPage } from "./pages/EnvironmentsPage";
import { SettingsPage } from "./pages/SettingsPage";

// Minimal hash-based router: keeps the app dependency-free while giving
// the spec's six primary navigation sections real, independent URLs.
function useHashRoute(): string {
  const [route, setRoute] = useState(() => window.location.hash.replace(/^#/, "") || "/");
  useEffect(() => {
    const onHash = () => setRoute(window.location.hash.replace(/^#/, "") || "/");
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return route;
}

function navigate(to: string) {
  window.location.hash = to;
}

export function App() {
  const route = useHashRoute();
  const isRunDetail = /^\/runs\/.+/.test(route);

  return (
    <div className="shell">
      <header style={{ display: "flex", alignItems: "baseline", gap: 12, padding: "16px 0" }}>
        <h1>Dependency Time Machine</h1>
        <span className="mono" style={{ color: "var(--text-muted)", fontSize: "0.75rem" }}>
          reproducible pre-merge upgrade evaluation
        </span>
      </header>

      <div className="app-grid">
        <nav className="app-rail" aria-label="Primary">
          <RailItem label="Repositories" to="/" active={route === "/"} />
          <RailItem label="Upgrade runs" to="/runs" active={route === "/runs" || isRunDetail} />
          <RailItem label="Dependency graph" to="/graph" active={route === "/graph"} />
          <RailItem label="Policies" to="/policies" active={route === "/policies"} />
          <RailItem label="Environments" to="/environments" active={route === "/environments"} />
          <RailItem label="Settings" to="/settings" active={route === "/settings"} />
        </nav>

        <main className="app-main">
          {route === "/" && <RepositoriesPage />}
          {route === "/runs" && <RunsPage />}
          {isRunDetail && <RunDetailPage runId={route.split("/")[2] ?? ""} />}
          {route === "/graph" && <DependencyGraphPage />}
          {route === "/policies" && <PoliciesPage />}
          {route === "/environments" && <EnvironmentsPage />}
          {route === "/settings" && <SettingsPage />}
        </main>
      </div>
    </div>
  );
}

function RailItem({ label, to, active }: { label: string; to: string; active: boolean }) {
  return (
    <button
      onClick={() => navigate(to)}
      aria-current={active ? "page" : undefined}
      style={{
        textAlign: "left",
        border: "none",
        background: active ? "var(--surface-2)" : "transparent",
        color: active ? "var(--text-primary)" : "var(--text-secondary)",
        borderRadius: "var(--radius-sm)",
        fontWeight: active ? 500 : 400,
      }}
    >
      {label}
    </button>
  );
}

// navigate is module-scoped and used by RailItem; no extra export needed.
