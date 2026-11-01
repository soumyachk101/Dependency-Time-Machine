// SeverityBadge — a compact, semantic-coloured chip for a risk severity.
// Monochrome base; colour is reserved for the state itself.
import type { Severity } from "../api/client";

const LABELS: Record<Severity, string> = {
  confirmed_failure: "Block",
  strong_warning: "Warn",
  review_suggested: "Review",
  no_issue_observed: "No issue observed",
};

export function SeverityBadge({ severity }: { severity: Severity }) {
  return (
    <span
      className={`sev-${severity}`}
      style={{
        fontFamily: "var(--font-mono)",
        fontSize: "0.75rem",
        border: "1px solid var(--border-subtle)",
        borderRadius: "var(--radius-sm)",
        padding: "1px 8px",
      }}
    >
      {LABELS[severity] ?? severity}
    </span>
  );
}

export function severityLabel(s: string): string {
  return (LABELS as Record<string, string>)[s] ?? s;
}
