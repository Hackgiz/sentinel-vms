import { useState } from "react";
import { useLoad } from "../shared/hooks";
import { fmtDateTime, v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";
import "./timeProfiles.css";

export type Webhook = {
  id: string;
  name: string;
  url: string;
  events: string[];
  enabled: boolean;
  hasSecret: boolean;
  lastStatus: string;
  lastFiredAt: number;
};

/** Event-kind subscriptions (empty = the webhook only fires from rule actions). */
const eventChoices: { id: string; label: string }[] = [
  { id: "alarm.new", label: "New alarm" },
  { id: "alarm.state", label: "Alarm state change" },
  { id: "event.new", label: "Detection event" },
  { id: "recorder.online", label: "Recording server online/offline" },
];

export function StatusDot({ status }: { status: string }) {
  const cls = status === "" ? "" : status.startsWith("ok") ? "ok" : "bad";
  return <span className={`status-dot ${cls}`} aria-hidden="true" />;
}

export default function WebhooksSection() {
  const hooks = useLoad(async () => (await v2.call("get", "/api/v2/webhooks")).items as Webhook[], []);
  const [editing, setEditing] = useState<Webhook | "new" | null>(null);
  const [deleting, setDeleting] = useState<Webhook | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, string>>({});
  const [error, setError] = useState<unknown>(null);
  const list = hooks.data ?? [];

  async function test(h: Webhook) {
    setTesting(h.id);
    setError(null);
    try {
      const r = await v2.call("post", "/api/v2/webhooks/{webhookId}/test", { path: { webhookId: h.id } });
      setTestResult((t) => ({ ...t, [h.id]: r.status }));
      void hooks.reload();
    } catch (e) {
      setError(e);
    } finally {
      setTesting(null);
    }
  }

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Webhooks</h1>
          <div className="sub">
            Sentinel calls these addresses with a JSON POST when a rule fires or an event you subscribe to happens. If you set a secret, each call carries an{" "}
            <code>X-Sentinel-Signature: sha256=…</code> HMAC of the body.
          </div>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + New webhook
        </button>
      </div>
      <ErrorBanner error={hooks.error || error} onRetry={hooks.reload} />
      <div className="card table-wrap">
        {hooks.data === null ? (
          hooks.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="↗" title="No webhooks yet">
            Add one, then give a rule a webhook action, or subscribe it to events here.
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Webhook</th>
                <th>Fires on</th>
                <th>Last delivery</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((h) => (
                <tr key={h.id}>
                  <td>
                    <strong>{h.name}</strong> {!h.enabled && <span className="badge amber">Off</span>}
                    <div className="mono hook-url">{h.url}</div>
                    <div className="muted small">{h.hasSecret ? "Signed" : "Not signed"}</div>
                  </td>
                  <td className="small">{h.events.length ? h.events.join(", ") : "Rule actions only"}</td>
                  <td className="small">
                    <StatusDot status={h.lastStatus} />
                    {h.lastStatus ? h.lastStatus : "Never"}
                    {h.lastFiredAt > 0 && <div className="muted">{fmtDateTime(h.lastFiredAt)}</div>}
                    {testResult[h.id] && <div className="muted">Test: {testResult[h.id]}</div>}
                  </td>
                  <td className="row-actions">
                    <button className="btn small" disabled={testing === h.id} onClick={() => test(h)}>
                      {testing === h.id ? "Sending…" : "Test"}
                    </button>
                    <button className="btn small" onClick={() => setEditing(h)}>
                      Edit
                    </button>
                    <button className="btn small danger" onClick={() => setDeleting(h)}>
                      Delete…
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {editing && (
        <WebhookEditor
          hook={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void hooks.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete webhook"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.call("delete", "/api/v2/webhooks/{webhookId}", { path: { webhookId: deleting.id } });
            setDeleting(null);
            void hooks.reload();
          }}
        >
          <p>A webhook that rules still use can't be deleted; the error lists them.</p>
        </Confirm>
      )}
    </>
  );
}

function WebhookEditor({ hook, onClose, onSaved }: { hook: Webhook | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(hook?.name ?? "");
  const [url, setUrl] = useState(hook?.url ?? "");
  const [secret, setSecret] = useState("");
  const [clearSecret, setClearSecret] = useState(false);
  const [events, setEvents] = useState<string[]>(hook?.events ?? []);
  const [enabled, setEnabled] = useState(hook?.enabled ?? true);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toggle = (id: string) => setEvents((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]));

  return (
    <Modal
      title={hook ? `Edit ${hook.name}` : "New webhook"}
      wide
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          // The secret is write-only: absent keeps the stored one, "" clears it.
          const body: { name: string; url: string; events: string[]; enabled: boolean; secret?: string } = { name: name.trim(), url: url.trim(), events, enabled };
          if (secret) body.secret = secret;
          else if (clearSecret) body.secret = "";
          if (hook) await v2.call("put", "/api/v2/webhooks/{webhookId}", { path: { webhookId: hook.id }, body: body as never });
          else await v2.call("post", "/api/v2/webhooks", { body: body as never });
          onSaved();
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !name.trim() || !url.trim()}>
            {hook ? "Save" : "Create webhook"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} maxLength={100} required placeholder="e.g. Slack relay" onChange={(e) => setName(e.target.value)} autoFocus />
      </label>
      <label className="field">
        <span>URL</span>
        <input type="url" value={url} required placeholder="https://example.com/hooks/sentinel" onChange={(e) => setUrl(e.target.value)} />
        <small>https:// is verified against trusted certificates; self-signed endpoints are not accepted.</small>
      </label>
      <label className="field">
        <span>Signing secret</span>
        <input
          type="password"
          value={secret}
          autoComplete="new-password"
          placeholder={hook?.hasSecret && !clearSecret ? "A secret is set — leave blank to keep it" : "Optional"}
          onChange={(e) => setSecret(e.target.value)}
        />
        <small>Write-only: it is never shown again.</small>
      </label>
      {hook?.hasSecret && (
        <label className="check">
          <input type="checkbox" checked={clearSecret} disabled={!!secret} onChange={() => setClearSecret(!clearSecret)} />
          Remove the stored secret
        </label>
      )}
      <div className="field">
        <span className="field-label">Also fire on (rule actions always fire)</span>
        <div className="chip-grid">
          {eventChoices.map((c) => (
            <label key={c.id} className={`chip ${events.includes(c.id) ? "on" : ""}`}>
              <input type="checkbox" checked={events.includes(c.id)} onChange={() => toggle(c.id)} />
              {c.label}
            </label>
          ))}
        </div>
      </div>
      <label className="check">
        <input type="checkbox" checked={enabled} onChange={() => setEnabled(!enabled)} />
        Webhook is on
      </label>
    </Modal>
  );
}
