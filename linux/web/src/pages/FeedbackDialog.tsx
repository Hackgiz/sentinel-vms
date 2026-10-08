import { useState, type FormEvent } from "react";
import { api } from "../api";

type Kind = "bug" | "idea" | "question";
const KINDS: { id: Kind; label: string; prompt: string }[] = [
  { id: "bug", label: "Something's wrong", prompt: "What went wrong, and how can we reproduce it?" },
  { id: "idea", label: "Idea", prompt: "What would make Sentinel better for you?" },
  { id: "question", label: "Question", prompt: "What would you like to know?" },
];

export default function FeedbackDialog({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState<Kind>("idea");
  const [message, setMessage] = useState("");
  const [email, setEmail] = useState("");
  const [diag, setDiag] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [sent, setSent] = useState(false);

  async function send(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.sendFeedback(kind, message, email, kind === "bug" && diag);
      setSent(true);
      setTimeout(onClose, 1600);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const k = KINDS.find((x) => x.id === kind)!;
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && !busy && onClose()}>
      <form className="modal" onSubmit={send}>
        <h2>Send feedback</h2>
        {sent ? (
          <div className="notice success">Thanks! Your feedback was sent. We read every one.</div>
        ) : (
          <>
            <div className="seg" style={{ marginBottom: 14 }}>
              {KINDS.map((x) => (
                <button type="button" key={x.id} className={kind === x.id ? "on" : ""} onClick={() => setKind(x.id)}>
                  {x.label}
                </button>
              ))}
            </div>
            {error && <div className="error">{error}</div>}
            <label className="field">
              <span>{k.prompt}</span>
              <textarea value={message} onChange={(e) => setMessage(e.target.value)} rows={6} maxLength={8000} autoFocus required />
            </label>
            <label className="field">
              <span>Your email (optional, so we can reply)</span>
              <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@example.com" />
            </label>
            {kind === "bug" && (
              <label className="check">
                <input type="checkbox" checked={diag} onChange={(e) => setDiag(e.target.checked)} />
                Include recent server activity (passwords, keys and addresses removed)
              </label>
            )}
            <p className="mono" style={{ marginTop: 10 }}>
              Sends your Sentinel version, Linux version and camera count. No video, camera addresses or passwords.
            </p>
            <div className="actions">
              <button type="button" className="btn" onClick={onClose} disabled={busy}>
                Cancel
              </button>
              <button className="btn primary" disabled={busy || !message.trim()}>
                {busy ? "Sending…" : "Send"}
              </button>
            </div>
          </>
        )}
      </form>
    </div>
  );
}
