// "Share…" on an evidence row: pick an expiry, max uses and a note; the link
// is then shown once (the server keeps only its hash) with a copy button.
import { useState } from "react";
import { CopyButton, ErrorBanner, Modal } from "../admin/ui";
import type { EvidenceCase } from "../shared/api";
import { fromLocalInput, toLocalInput } from "./exportsModel";
import { useSigned } from "./session";
import { shareBlocker, shareBody, sharePresets, type SharePreset } from "./shareModel";

export default function ShareDialog({ evidence, onClose, onCreated }: { evidence: EvidenceCase; onClose: () => void; onCreated: () => void }) {
  const client = useSigned().session.client;
  const [preset, setPreset] = useState<SharePreset>("24h");
  const [customAt, setCustomAt] = useState(0);
  const [maxUses, setMaxUses] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [url, setUrl] = useState("");
  const blocker = shareBlocker(preset, customAt, maxUses);

  async function create() {
    if (blocker || busy) return;
    setBusy(true);
    setError(null);
    try {
      const r = await client.evidence.share(evidence.id, shareBody(preset, customAt, maxUses, note));
      setUrl(r.url);
      onCreated();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  if (url) {
    return (
      <Modal
        title="Share link created"
        onClose={onClose}
        actions={
          <button type="button" className="btn primary" onClick={onClose}>
            Done
          </button>
        }
      >
        <p className="small">
          Copy it now: this link is shown only once. Anyone on this network who opens it can watch and download {evidence.caseId} until it expires or you revoke it.
        </p>
        <input type="text" readOnly aria-label="Share link" className="mono small" style={{ width: "100%" }} value={url} onFocus={(e) => e.currentTarget.select()} />
        <p>
          <CopyButton text={url} label="Copy link" />
        </p>
      </Modal>
    );
  }

  return (
    <Modal
      title={`Share ${evidence.caseId}`}
      onClose={onClose}
      onSubmit={() => void create()}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!!blocker || busy} title={blocker}>
            {busy ? "Creating…" : "Create link"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Expires after</span>
        <select value={preset} onChange={(e) => setPreset(e.target.value as SharePreset)}>
          {sharePresets.map((p) => (
            <option key={p.value} value={p.value}>
              {p.label}
            </option>
          ))}
        </select>
      </label>
      {preset === "custom" && (
        <label className="field">
          <span>Expires at</span>
          <input type="datetime-local" value={toLocalInput(customAt)} onChange={(e) => setCustomAt(fromLocalInput(e.target.value))} />
        </label>
      )}
      <label className="field">
        <span>Max uses (blank = unlimited)</span>
        <input type="number" min={0} max={1000} step={1} value={maxUses} onChange={(e) => setMaxUses(e.target.value)} />
      </label>
      <label className="field">
        <span>Note (shown on the page)</span>
        <input type="text" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} />
      </label>
      {blocker && <div className="muted small">{blocker}</div>}
    </Modal>
  );
}
