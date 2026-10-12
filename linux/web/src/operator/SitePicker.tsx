// The first screen: saved sites, and adding or editing one.
import { useState, type FormEvent } from "react";
import { normalizeAddress, sortSites, type Site } from "./sites";

export default function SitePicker({
  sites,
  onConnect,
  onSave,
  onRemove,
}: {
  sites: Site[];
  onConnect: (s: Site) => void;
  onSave: (input: { id?: string; name: string; address: string }) => Promise<void>;
  onRemove: (s: Site) => void;
}) {
  const [editing, setEditing] = useState<{ id?: string; name: string; address: string } | null>(sites.length ? null : { name: "", address: "" });
  const [error, setError] = useState("");
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!editing) return;
    const norm = normalizeAddress(editing.address);
    if ("error" in norm) {
      setError(norm.error);
      return;
    }
    try {
      await onSave(editing);
      setEditing(null);
      setError("");
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <div className="auth op-sites">
      <div className="card">
        <div className="logo">
          <img src="/favicon.svg" alt="" />
        </div>
        <h1>Sentinel Operator</h1>
        <p className="lead">{editing ? (editing.id ? "Edit site" : "Add a site: the management server's address") : "Choose a site"}</p>
        {editing ? (
          <form onSubmit={submit}>
            <label className="field">
              <span>Address</span>
              <input
                type="text"
                autoFocus
                value={editing.address}
                placeholder="ms.lan, 192.168.1.20:8090 or https://vms.example.com"
                onChange={(e) => setEditing({ ...editing, address: e.target.value })}
                spellCheck={false}
                autoCapitalize="off"
              />
              <small>Without http:// or https://, Sentinel uses http on port 8090.</small>
            </label>
            <label className="field">
              <span>Name</span>
              <input type="text" value={editing.name} placeholder="Warehouse" onChange={(e) => setEditing({ ...editing, name: e.target.value })} />
            </label>
            {error && <div className="error">{error}</div>}
            <button className="btn primary" type="submit">
              {editing.id ? "Save" : "Add site"}
            </button>
            {sites.length > 0 && (
              <button
                className="btn op-mt"
                type="button"
                onClick={() => {
                  setEditing(null);
                  setError("");
                }}
              >
                Cancel
              </button>
            )}
          </form>
        ) : (
          <>
            <div className="op-site-list" role="list">
              {sortSites(sites).map((s) => (
                <div key={s.id} className="op-site" role="listitem">
                  <button className="op-site-main" onClick={() => onConnect(s)} title={`Connect to ${s.address}`}>
                    <strong>{s.name}</strong>
                    <span className="mono">{s.address}</span>
                    {s.lastUser && <span className="muted small">Last signed in as {s.lastUser}</span>}
                  </button>
                  <div className="op-site-actions">
                    {confirmRemove === s.id ? (
                      <>
                        <button className="btn small danger" onClick={() => onRemove(s)}>
                          Remove
                        </button>
                        <button className="btn small" onClick={() => setConfirmRemove(null)}>
                          Keep
                        </button>
                      </>
                    ) : (
                      <>
                        <button className="btn small" onClick={() => setEditing({ id: s.id, name: s.name, address: s.address })} aria-label={`Edit ${s.name}`}>
                          Edit
                        </button>
                        <button className="btn small" onClick={() => setConfirmRemove(s.id)} aria-label={`Remove ${s.name}`}>
                          ✕
                        </button>
                      </>
                    )}
                  </div>
                </div>
              ))}
            </div>
            <button className="btn op-mt" onClick={() => setEditing({ name: "", address: "" })}>
              + Add site
            </button>
          </>
        )}
      </div>
    </div>
  );
}
