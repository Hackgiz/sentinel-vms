import { useCallback, useEffect, useState } from "react";
import { api, can, formatBytes, type Alarm, type User } from "../api";

const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };
const stateClass: Record<string, string> = { New: "red", Acknowledged: "amber", Investigating: "amber", Snoozed: "blue", Resolved: "green", "False Alarm": "blue" };

export default function AlarmsPage({ user, onChange }: { user: User; onChange?: () => void }) {
  const [alarms, setAlarms] = useState<Alarm[]>([]);
  const [showAll, setShowAll] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const load = useCallback(() => {
    api.alarms(showAll).then(setAlarms).catch((e) => setError(e.message));
  }, [showAll]);
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);

  async function act(a: Alarm, fn: () => Promise<unknown>) {
    setBusy(a.id);
    setError("");
    try {
      await fn();
      load();
      onChange?.();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }

  const mayAct = can.acknowledge(user);
  const newCount = alarms.filter((a) => a.state === "New").length;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Alarms</h1>
          <div className="sub">{showAll ? `${alarms.length} alarms` : newCount ? `${newCount} new · ${alarms.length} open` : `${alarms.length} open`}</div>
        </div>
        <div className="seg">
          <button className={!showAll ? "on" : ""} onClick={() => setShowAll(false)}>
            Open
          </button>
          <button className={showAll ? "on" : ""} onClick={() => setShowAll(true)}>
            All
          </button>
        </div>
      </div>
      {error && <div className="error">{error}</div>}
      {alarms.length === 0 ? (
        <div className="card">
          <div className="empty">
            <div className="big">✓</div>
            {showAll ? "No alarms yet." : "No open alarms. Motion and camera problems show up here."}
          </div>
        </div>
      ) : (
        <div className="alarm-list">
          {alarms.map((a) => (
            <div key={a.id} className={`card alarm ${a.state === "New" ? "is-new" : ""}`}>
              <div className="alarm-head">
                <span className={`badge ${sevClass[a.severity] ?? "blue"}`}>{a.severity}</span>
                <strong className="grow">
                  {a.title}
                  {a.cameraName && <span className="muted"> · {a.cameraName}</span>}
                </strong>
                <span className={`badge ${stateClass[a.state] ?? "blue"}`}>{a.state}</span>
              </div>
              <div className="alarm-detail">{a.detail}</div>
              <div className="mono">
                {when(a.lastEventAt)}
                {a.eventCount > 1 && ` · ${a.eventCount} events`}
                {a.owner && a.owner !== "Unassigned" && ` · ${a.owner}`}
              </div>
              {a.evidence && (
                <div className="evidence-row">
                  <span className="badge green">Evidence locked</span>
                  <span className="mono">
                    {a.evidence.caseID} · {a.evidence.range} · {formatBytes(a.evidence.size)} · sha256 {a.evidence.sha256.slice(0, 12)}…
                  </span>
                  <a className="btn small" href={`/api/v1/evidence/${a.evidence.id}/file`}>
                    Download
                  </a>
                </div>
              )}
              {mayAct && (
                <div className="alarm-actions">
                  {(a.state === "New" || a.state === "Snoozed") && (
                    <button className="btn small primary" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Acknowledged"))}>
                      Acknowledge
                    </button>
                  )}
                  {a.state !== "Investigating" && a.state !== "Resolved" && a.state !== "False Alarm" && (
                    <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Investigating"))}>
                      Investigate
                    </button>
                  )}
                  {a.state !== "Resolved" && a.state !== "False Alarm" && (
                    <>
                      <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Resolved"))}>
                        Resolve
                      </button>
                      <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "False Alarm"))}>
                        False alarm
                      </button>
                    </>
                  )}
                  {(a.state === "Resolved" || a.state === "False Alarm") && (
                    <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "New", "Reopened"))}>
                      Reopen
                    </button>
                  )}
                  {a.hasClip && !a.evidence && (
                    <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.lockEvidence(a.id))}>
                      Lock evidence
                    </button>
                  )}
                  <span className="grow" />
                  <button className="btn small" onClick={() => setOpen(open === a.id ? null : a.id)}>
                    {open === a.id ? "Hide log" : "Log"}
                  </button>
                </div>
              )}
              {open === a.id && (
                <ul className="response-log">
                  {a.responseLog.map((l, i) => (
                    <li key={i}>{l}</li>
                  ))}
                </ul>
              )}
            </div>
          ))}
        </div>
      )}
    </>
  );
}

export function when(unix: number): string {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 60) return "Just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  const d = new Date(unix * 1000);
  if (s < 86400) return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return d.toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}
