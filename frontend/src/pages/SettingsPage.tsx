import { useState } from "react";
import type { CSSProperties } from "react";
import { postWebhookTest } from "../api/client";
import { ErrorView, Loading } from "../components/StateViews";

// Settings — operator configuration for CI integration and the control
// plane's GitHub integration (webhook secret + GitHub App credentials).
//
// Honest-state note: this screen persists operator values locally
// (localStorage, same shape as the other config screens) and documents
// how to wire them into the server environment — the control plane's
// GitHub features read DTM_GITHUB_WEBHOOK_SECRET and DTM_GITHUB_APP_* at
// boot, and an unconfigured integration is disabled (closed default),
// not faked.

interface WebhookSettings {
  endpoint: string;
  events: string;
  secretConfigured: boolean;
  secret: string; // never displayed back; presence flag only
}

interface GitHubSettings {
  appId: string;
  keyFile: string;
}

const WKEY = "dtm.settings.webhook";
const GKEY = "dtm.settings.github";

const CI_SNIPPET = `# 1) Verify the control plane's GitHub webhook intake works end to end:
#    generate a throwaway HMAC with the same secret the server uses.
BODY='{"action":"opened","number":1}'
SIG=\`printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$DTM_GITHUB_WEBHOOK_SECRET" | awk '{print "sha256="$2}'\`
curl -fsS -X POST "$DTM_API_BASE/v1/webhooks/github" \\
  -H "Content-Type: application/json" \\
  -H "X-Hub-Signature-256: $SIG" \\
  -d "$BODY"

# 2) Ingest a completed CLI/CI run and post the check result (pass the
#    run's machine JSON through; omit check_run to let the server apply
#    the project policy itself).
curl -fsS -X POST "$DTM_API_BASE/v1/cli-runs" \\
  -H "Content-Type: application/json" \\
  -d '{
    "project_id": "github/acme/checkout-service",
    "owner": "acme", "repo": "checkout-service", "commit_sha": "9a2e1c…",
    "runner_mode": "container",
    "executed": true,
    "result_class": "strong_warning",
    "check_run": null
  }'

# 3) Fetch the machine report and the PR-facing summary for a run:
curl -fsS "$DTM_API_BASE/v1/upgrade-runs/run-<id>/report"
curl -fsS "$DTM_API_BASE/v1/upgrade-runs/run-<id>/graph-diff"`;

function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? { ...fallback, ...(JSON.parse(raw) as Partial<T>) } : fallback;
  } catch {
    return fallback;
  }
}

export function SettingsPage() {
  const [webhook, setWebhook] = useState<WebhookSettings>(() =>
    load(WKEY, { endpoint: "/v1/webhooks/github", events: "pull_request", secretConfigured: false, secret: "" }),
  );
  const [github, setGithub] = useState<GitHubSettings>(() => load(GKEY, { appId: "", keyFile: "" }));
  const [err, setErr] = useState<string | null>(null);
  const [testBusy, setTestBusy] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null);

  // Sends one synthetic event to the real webhook-intake endpoint. Until
  // the server has a signing secret this returns 401 — an honest "intake
  // disabled" state, not a fabricated "verified" one.
  async function testWebhook() {
    setTestBusy(true);
    setTestResult(null);
    const res = await postWebhookTest({ action: "opened", number: 1 });
    setTestBusy(false);
    if (res.ok) {
      setTestResult({ ok: true, text: "Intake accepted the event: " + res.data.note });
    } else {
      setTestResult({
        ok: false,
        text:
          res.status === 401
            ? "Intake disabled or signature rejected (" + res.status + "): set DTM_GITHUB_WEBHOOK_SECRET on the server and sign events with it."
            : "Request failed (" + res.status + "): " + res.error,
      });
    }
  }

  function save() {
    localStorage.setItem(WKEY, JSON.stringify(webhook));
    localStorage.setItem(GKEY, JSON.stringify(github));
  }

  function copySnippet() {
    if (!navigator.clipboard) {
      setErr("Clipboard is unavailable in this context; select the snippet manually.");
      return;
    }
    navigator.clipboard
      .writeText(CI_SNIPPET)
      .then(() => setErr(null))
      .catch(() => setErr("Could not copy to the clipboard."));
  }

  const inputStyle: CSSProperties = { width: "100%" };

  return (
    <section aria-labelledby="settings-h">
      <h2 id="settings-h">Settings</h2>
      <p style={{ color: "var(--text-muted)", margin: "4px 0 16px", maxWidth: 420 }}>
        CI integration and control-plane GitHub configuration. Unconfigured
        secrets mean the integration is disabled — the server rejects
        unauthenticated webhooks and reports check-run posting as
        skipped, never as success.
      </p>

      <div style={{ display: "grid", gap: 24, maxWidth: 480 }}>
        {/* ---- CI integration ------------------------------------------------ */}
        <div>
          <h3 style={{ marginBottom: 4 }}>CI integration</h3>
          <p style={{ color: "var(--text-muted)", fontSize: "0.8125rem", marginBottom: 12 }}>
            A CI job ingests completed runs through <code className="mono">POST /v1/cli-runs</code> and
            can read any run's report through the machine API. Replayed
            requests are no-ops; execution outcomes carry an explicit
            "executed" flag so a no-execution result can never read as
            verified.
          </p>
          <pre className="mono" style={{
            background: "var(--surface-1)",
            border: "1px solid var(--border-subtle)",
            borderRadius: "var(--radius-md)",
            padding: 12,
            overflowX: "auto",
            fontSize: "0.75rem",
            lineHeight: 1.5,
          }}>{CI_SNIPPET}</pre>
          <button onClick={copySnippet} style={{ marginTop: 8, fontSize: "0.8125rem" }}>
            Copy snippet
          </button>
          {err ? <ErrorView title="Copy failed" detail={err} /> : null}
          <div className="notice" style={{ marginTop: 12 }}>
            Policy decision: block → check failure, warn → check warning,
            otherwise success. A degraded (analysis-only) run with any
            finding floors to warning, so a no-execution result is never
            presented as "verified". Upgrades are never merged
            automatically.
          </div>
        </div>

        {/* ---- Webhook -------------------------------------------------------- */}
        <div>
          <h3 style={{ marginBottom: 4 }}>GitHub webhook</h3>
          <p style={{ color: "var(--text-muted)", fontSize: "0.8125rem", marginBottom: 12 }}>
            Point your repository's webhook at the control plane's
            <code className="mono"> {webhook.endpoint}</code> path with the event types
            selected below. The secret must match
            <code className="mono"> DTM_GITHUB_WEBHOOK_SECRET</code> on the server —
            without a configured secret the intake endpoint rejects every
            event (closed default).
          </p>
          <div style={{ display: "grid", gap: 12 }}>
            <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
              <span style={{ color: "var(--text-muted)" }}>Webhook path</span>
              <input className="mono" value={webhook.endpoint} style={inputStyle}
                onChange={(e) => setWebhook({ ...webhook, endpoint: e.target.value })} />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
              <span style={{ color: "var(--text-muted)" }}>Event types</span>
              <input value={webhook.events} style={inputStyle}
                onChange={(e) => setWebhook({ ...webhook, events: e.target.value })} />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
              <span style={{ color: "var(--text-muted)" }}>
                {webhook.secretConfigured ? "Signing secret (stored locally)" : "Signing secret"}
              </span>
              <input type="password" value={webhook.secret} style={inputStyle}
                placeholder={webhook.secretConfigured ? "configured" : "e.g. 32+ random bytes"}
                onChange={(e) => setWebhook({ ...webhook, secret: e.target.value, secretConfigured: e.target.value !== "" })} />
            </label>
          </div>
          <div style={{ marginTop: 12, fontSize: "0.8125rem", color: "var(--text-secondary)" }}>
            {webhook.secretConfigured ? (
              <span style={{ color: "var(--success)" }}>Secret present locally — export it as <code className="mono">DTM_GITHUB_WEBHOOK_SECRET</code> on the server to arm HMAC verification.</span>
            ) : (
              <span>Secret not configured: the server will reject webhook events until <code className="mono">DTM_GITHUB_WEBHOOK_SECRET</code> is set.</span>
            )}
          </div>

          <div style={{ marginTop: 16, display: "grid", gap: 8 }}>
            <button
              onClick={() => void testWebhook()}
              disabled={testBusy}
              style={{ justifySelf: "start", fontSize: "0.8125rem" }}
            >
              {testBusy ? "Testing intake…" : "Test webhook intake"}
            </button>
            {testBusy ? <Loading label="Sending a synthetic event to " + webhook.endpoint + "…" /> : null}
            {testResult ? (
              testResult.ok ? (
                <p className="mono" style={{ fontSize: "0.8125rem", color: "var(--success)" }}>{testResult.text}</p>
              ) : (
                <ErrorView title="Intake not verified" detail={testResult.text} onRetry={() => void testWebhook()} />
              )
            ) : null}
          </div>
        </div>

        {/* ---- GitHub App ----------------------------------------------------- */}
        <div>
          <h3 style={{ marginBottom: 4 }}>GitHub App</h3>
          <p style={{ color: "var(--text-muted)", fontSize: "0.8125rem", marginBottom: 12 }}>
            Check-run posting is enabled only when the operator supplies
            App credentials. In this environment the installation-token
            exchange is a documented limitation, so a direct
            <code className="mono"> DTM_GITHUB_TOKEN</code> is the supported path;
            unconfigured, the server reports posting as
            <code className="mono"> skipped_no_token</code>.
          </p>
          <div style={{ display: "grid", gap: 12 }}>
            <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
              <span style={{ color: "var(--text-muted)" }}>App ID</span>
              <input className="mono" value={github.appId} style={inputStyle}
                onChange={(e) => setGithub({ ...github, appId: e.target.value })} />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: "0.8125rem" }}>
              <span style={{ color: "var(--text-muted)" }>Private key file</span>
              <input className="mono" value={github.keyFile} style={inputStyle}
                onChange={(e) => setGithub({ ...github, keyFile: e.target.value })} />
            </label>
          </div>
        </div>

        <button onClick={save} style={{ justifySelf: "start" }}>Save settings</button>
      </div>

      <div className="notice" style={{ marginTop: 20, borderColor: "var(--info)" }}>
        Rate limiting: public API routes share a per-client-IP token
        bucket (<code className="mono">DTM_RATE_LIMIT_RPS</code> / <code className="mono">DTM_RATE_LIMIT_BURST</code>,
        defaults 10 rps / burst 20). Runs accept an{" "}
        <code className="mono">idempotency_key</code> so replayed webhooks and
        retried jobs are no-ops, not duplicate work.
      </div>
    </section>
  );
}
