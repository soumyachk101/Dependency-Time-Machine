// Reusable state surfaces: every screen in the app renders one of these
// instead of a fake result. They exist so "loading", "empty", "error"
// are designed states, not accidents.

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div
      role="status"
      aria-live="polite"
      style={{ padding: "var(--space-6)", color: "var(--text-muted)" }}
    >
      <span aria-hidden="true" className="mono">
        …
      </span>{" "}
      {label}
    </div>
  );
}

export function Empty({
  title,
  hint,
}: {
  title: string;
  hint?: string;
}) {
  return (
    <div style={{ padding: "var(--space-6)", color: "var(--text-muted)" }}>
      <p style={{ color: "var(--text-secondary)", fontWeight: 500 }}>{title}</p>
      {hint ? <p style={{ marginTop: 4, fontSize: "0.8125rem" }}>{hint}</p> : null}
    </div>
  );
}

export function ErrorView({
  title,
  detail,
  onRetry,
}: {
  title: string;
  detail?: string;
  onRetry?: () => void;
}) {
  return (
    <div role="alert" className="notice" style={{ borderColor: "var(--danger)" }}>
      <p style={{ fontWeight: 500, color: "var(--danger)" }}>{title}</p>
      {detail ? <p style={{ marginTop: 4 }}>{detail}</p> : null}
      {onRetry ? (
        <button onClick={onRetry} style={{ marginTop: 8 }}>
          Retry
        </button>
      ) : null}
    </div>
  );
}
