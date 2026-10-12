import { useCallback, useEffect, useRef, useState } from "react";
import { type EnrollmentToken, type RecordingServer } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { fmtAgo } from "../shared/time";
import { formatBytes, v2 } from "./client";
import { Confirm, CopyButton, Empty, ErrorBanner, Loading, Modal, OnlineBadge, useLiveReload } from "./ui";

export default function ServersSection() {
  const servers = useLoad(() => v2.servers.list(), []);
  const [editing, setEditing] = useState<RecordingServer | null>(null);
  const [revoking, setRevoking] = useState<RecordingServer | null>(null);
  const [viewingLog, setViewingLog] = useState<RecordingServer | null>(null);
  const [enrolling, setEnrolling] = useState(false);
  const [now, setNow] = useState(Date.now());
  useLiveReload(servers.reload, ["recorder.status"]);
  useEffect(() => {
    const t = setInterval(() => {
      setNow(Date.now());
      void servers.reload();
    }, 30_000);
    return () => clearInterval(t);
  }, [servers.reload]);

  const list = servers.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Recording servers</h1>
          <div className="sub">Machines that connect to cameras, record and detect. Video goes from them straight to viewers.</div>
        </div>
        <button className="btn primary" onClick={() => setEnrolling(true)}>
          + Enroll server
        </button>
      </div>
      <ErrorBanner error={servers.error} onRetry={servers.reload} />
      <div className="card table-wrap">
        {servers.data === null ? (
          servers.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="▤" title="No recording servers">
            Use <strong>Enroll server</strong> to add the first one.
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Version</th>
                <th>Cameras</th>
                <th>Storage</th>
                <th>Last seen</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((s) => (
                <tr key={s.id} className={s.revoked ? "row-muted" : ""}>
                  <td>
                    <strong>{s.name}</strong> {s.local && <span className="badge blue">This machine</span>}
                    <div className="mono">{s.publicUrl || "Video through the management server"}</div>
                  </td>
                  <td>{s.revoked ? <span className="badge red">Revoked</span> : <OnlineBadge online={s.online} />}</td>
                  <td className="mono">{s.version || "—"}</td>
                  <td>{s.cameras}</td>
                  <td className="mono">
                    {s.storage ? formatBytes(s.storage.recordingsBytes) : "—"}
                    {s.storage?.diskFreeBytes !== undefined && <div>{formatBytes(s.storage.diskFreeBytes)} free</div>}
                  </td>
                  <td className="mono">{s.online ? "Now" : fmtAgo(s.lastSeenAt, now)}</td>
                  <td className="row-actions">
                    {!s.revoked && (
                      <>
                        <button className="btn small" onClick={() => setEditing(s)}>
                          Edit
                        </button>
                        <button
                          className="btn small"
                          disabled={!s.online}
                          title={s.online ? "Recent system log" : "Offline: its log can't be fetched"}
                          onClick={() => setViewingLog(s)}
                        >
                          Log
                        </button>
                        {!s.local && (
                          <button className="btn small danger" onClick={() => setRevoking(s)}>
                            Revoke…
                          </button>
                        )}
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {enrolling && (
        <EnrollDialog
          onClose={() => {
            setEnrolling(false);
            void servers.reload();
          }}
        />
      )}
      {editing && (
        <EditServer
          server={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void servers.reload();
          }}
        />
      )}
      {viewingLog && <LogDialog server={viewingLog} onClose={() => setViewingLog(null)} />}
      {revoking && (
        <Confirm
          title={`Revoke ${revoking.name}?`}
          confirmLabel="Revoke server"
          onCancel={() => setRevoking(null)}
          run={async () => {
            await v2.servers.revoke(revoking.id);
            setRevoking(null);
            void servers.reload();
          }}
        >
          <p>
            Its certificate is refused from now on and it is disconnected. Its {revoking.cameras} camera{revoking.cameras === 1 ? "" : "s"} stay assigned to it,
            and stop recording, until you move them to another server on the Cameras page.
          </p>
          <p className="muted small">To use this machine again, enroll it with a new token.</p>
        </Confirm>
      )}
    </>
  );
}

function EnrollDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("");
  const [token, setToken] = useState<EnrollmentToken | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!token) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [token]);

  const left = token ? Math.max(0, token.expiresAt - now) : 0;
  const mmss = `${Math.floor(left / 60000)}:${String(Math.floor((left % 60000) / 1000)).padStart(2, "0")}`;

  if (token) {
    return (
      <Modal
        title="Enroll a recording server"
        wide
        onClose={onClose}
        actions={
          <button type="button" className="btn primary" onClick={onClose}>
            Done
          </button>
        }
      >
        <div className="notice">
          This token is shown <strong>only now</strong> and works once.{" "}
          {left > 0 ? (
            <>
              It expires in <strong className="mono" aria-live="off">{mmss}</strong>.
            </>
          ) : (
            <strong>It has expired. Close this and create a new one.</strong>
          )}
        </div>
        <p className="muted small">On the new machine, with Sentinel Server installed, run:</p>
        <div className="code-row">
          <div className="code-block grow">{token.command}</div>
          <CopyButton text={token.command} label="Copy command" />
        </div>
        <label className="field">
          <span>Token only (for the installer)</span>
          <div className="code-row">
            <input type="text" readOnly value={token.token} className="mono" onFocus={(e) => e.currentTarget.select()} />
            <CopyButton text={token.token} />
          </div>
        </label>
        <p className="muted small">The server appears in the list once it connects. Then set where viewers reach it and assign cameras.</p>
      </Modal>
    );
  }

  return (
    <Modal
      title="Enroll a recording server"
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          setToken(await v2.servers.enroll(name.trim() || undefined));
          setNow(Date.now());
        } catch (e) {
          setError(e);
        } finally {
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "Creating…" : "Create token"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <p className="muted">Creates a one-time enrollment token, valid for 1 hour.</p>
      <label className="field">
        <span>Name (optional)</span>
        <input type="text" value={name} maxLength={100} onChange={(e) => setName(e.target.value)} placeholder="Warehouse NVR" autoFocus />
        <small>What the new server is called here. You can rename it later.</small>
      </label>
    </Modal>
  );
}

function EditServer({ server, onClose, onSaved }: { server: RecordingServer; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(server.name);
  const [publicUrl, setPublicUrl] = useState(server.publicUrl);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const urlProblem = publicUrl.trim() && !/^https?:\/\/[^\s/]+/.test(publicUrl.trim()) ? "Start with http:// or https://" : "";
  return (
    <Modal
      title={`Edit ${server.name}`}
      onClose={onClose}
      onSubmit={async () => {
        if (urlProblem) return;
        setBusy(true);
        setError(null);
        try {
          await v2.servers.update(server.id, { name: name.trim(), publicUrl: publicUrl.trim().replace(/\/+$/, "") });
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
          <button className="btn primary" disabled={busy || !name.trim() || !!urlProblem}>
            Save
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} required maxLength={100} onChange={(e) => setName(e.target.value)} autoFocus />
      </label>
      <label className="field">
        <span>Public URL</span>
        <input
          type="text"
          value={publicUrl}
          onChange={(e) => setPublicUrl(e.target.value)}
          placeholder="https://rs2.lan:8090"
          aria-invalid={!!urlProblem}
          aria-describedby="public-url-help"
        />
        <small id="public-url-help">
          {urlProblem ||
            "Where viewers on your network reach this server's video. Leave empty to send video through the management server (slower, but works from anywhere)."}
        </small>
      </label>
    </Modal>
  );
}

const LOG_LINES = [100, 300, 1000, 2000];

/** A recording server's recent system log (secrets stripped), newest at the bottom. */
function LogDialog({ server, onClose }: { server: RecordingServer; onClose: () => void }) {
  const [lines, setLines] = useState(300);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [fetchedAt, setFetchedAt] = useState(0);
  const view = useRef<HTMLPreElement>(null);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      setBusy(true);
      setError(null);
      try {
        const t = await v2.servers.log(server.id, lines, signal);
        setText(t ?? "");
        setFetchedAt(Date.now());
      } catch (e) {
        if ((e as Error)?.name !== "AbortError") setError(e);
      } finally {
        if (!signal?.aborted) setBusy(false);
      }
    },
    [server.id, lines],
  );
  useEffect(() => {
    const ac = new AbortController();
    void load(ac.signal);
    return () => ac.abort();
  }, [load]);
  useEffect(() => {
    if (view.current) view.current.scrollTop = view.current.scrollHeight; // newest lines are last
  }, [text]);

  const body = (text ?? "").replace(/\n$/, "");
  const count = body ? body.split("\n").length : 0;
  return (
    <Modal
      title={`Log: ${server.name}`}
      wide
      onClose={onClose}
      onSubmit={() => void load()}
      actions={
        <>
          <CopyButton text={body} label="Copy log" />
          <button className="btn" disabled={busy}>
            {busy ? "Loading…" : "Refresh"}
          </button>
          <button type="button" className="btn primary" onClick={onClose}>
            Close
          </button>
        </>
      }
    >
      <div className="log-toolbar">
        <label className="log-lines">
          <span>Last</span>
          <select value={lines} onChange={(e) => setLines(Number(e.target.value))}>
            {LOG_LINES.map((n) => (
              <option key={n} value={n}>
                {n} lines
              </option>
            ))}
          </select>
        </label>
        <span className="muted small grow" aria-live="polite">
          {text !== null && !busy && `${count} line${count === 1 ? "" : "s"}, fetched ${new Date(fetchedAt).toLocaleTimeString()}`}
        </span>
      </div>
      <ErrorBanner error={error} onRetry={() => void load()} />
      {text === null ? (
        error ? null : <Loading label="Fetching the log…" />
      ) : body ? (
        <pre ref={view} className="code-block log-view" tabIndex={0} aria-label={`Log of ${server.name}`}>
          {body}
        </pre>
      ) : (
        <p className="muted">Nothing logged yet.</p>
      )}
      <p className="muted small">Passwords, tokens and keys are removed before the log leaves the server. Each fetch is recorded in the audit log.</p>
    </Modal>
  );
}
