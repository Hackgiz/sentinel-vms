import { useCallback, useEffect, useState } from "react";
import { api, can, formatBytes, type Alarm, type User } from "../api";
import { AlarmCard, PageHeader } from "../shared/Cards"; // WAVE21
import { v2 } from "../admin/client";
import { alarmPlaybackLink } from "../dashboardNav";
import { snoozeChoices } from "../shared/alarmKind";

export default function AlarmsPage({ user, onChange }: { user: User; onChange?: () => void }) {
  const [alarms, setAlarms] = useState<Alarm[]>([]);
  const [showAll, setShowAll] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [openLog, setOpenLog] = useState<string | null>(null);
  // PARITY-342 §4: an optional note per alarm (into the response log) and the snooze length.
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [snooze, setSnooze] = useState(15);

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
      setNotes((n) => ({ ...n, [a.id]: "" }));
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
      <PageHeader title="Alarms" subtitle={showAll ? `${alarms.length} alarms` : newCount ? `${newCount} new · ${alarms.length} open` : `${alarms.length} open`}>
        <div className="seg">
          <button className={!showAll ? "on" : ""} onClick={() => setShowAll(false)}>
            Open
          </button>
          <button className={showAll ? "on" : ""} onClick={() => setShowAll(true)}>
            All
          </button>
        </div>
      </PageHeader>
      {error && <div className="error">{error}</div>}
      {alarms.length === 0 ? (
        <div className="card">
          <div className="empty">
            <div className="big">✓</div>
            {showAll ? "No alarms yet." : "No open alarms. Motion and camera problems show up here."}
          </div>
        </div>
      ) : (
        <div className="op-card-list">
          {alarms.map((a) => (
            <AlarmCard
              key={a.id}
              alarm={a}
              attention={a.state === "New"}
              actionsAlways
              meta={
                <>
                  {a.cameraName ? `${a.cameraName} · ` : ""}
                  {when(a.lastEventAt)}
                  {a.eventCount > 1 && ` · ${a.eventCount} events`}
                  {a.owner && a.owner !== "Unassigned" && ` · ${a.owner}`}
                </>
              }
              actions={mayAct ? actionRow(a) : playLink(a)}
            >
              {(a.instructions?.length || a.evidence || openLog === a.id) && (
                <div className="dash-alarm-extra">
                  {a.instructions && a.instructions.length > 0 && (
                    <div className="alarm-instructions">
                      {a.instructions.map((i, n) => (
                        <div key={n}>
                          <strong>{i.rule}:</strong> {i.text}
                        </div>
                      ))}
                    </div>
                  )}
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
                  {openLog === a.id && (
                    <ul className="response-log">
                      {a.responseLog.map((l, i) => (
                        <li key={i}>{l}</li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
            </AlarmCard>
          ))}
        </div>
      )}
    </>
  );

  function note(a: Alarm): string {
    return (notes[a.id] ?? "").trim();
  }

  /** PARITY-342 §3: the alarm's recording, 10 s before it was raised. */
  function playLink(a: Alarm) {
    const href = alarmPlaybackLink(a);
    return href ? (
      <a className="btn small" href={href} title="Play the recording from just before the alarm">
        ▶ Recording
      </a>
    ) : undefined;
  }

  function actionRow(a: Alarm) {
    const active = a.state !== "Resolved" && a.state !== "False Alarm";
    return (
      <>
        {playLink(a)}
        {(a.state === "New" || a.state === "Snoozed") && (
          <button className="btn small primary" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Acknowledged", note(a)))}>
            Acknowledge
          </button>
        )}
        {a.state !== "Investigating" && a.state !== "Resolved" && a.state !== "False Alarm" && (
          <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Investigating", note(a)))}>
            Investigate
          </button>
        )}
        {(a.state === "New" || a.state === "Acknowledged") && (
          <span className="dash-snooze">
            <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => v2.alarms.setState(a.id, "Snoozed", note(a) || undefined, snooze))}>
              Snooze
            </button>
            <select className="select-inline" aria-label="Snooze for" value={snooze} onChange={(e) => setSnooze(Number(e.target.value))}>
              {snoozeChoices.map((m) => (
                <option key={m} value={m}>
                  {m} min
                </option>
              ))}
            </select>
          </span>
        )}
        {active && (
          <>
            <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "Resolved", note(a)))}>
              Resolve
            </button>
            <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "False Alarm", note(a)))}>
              False alarm
            </button>
          </>
        )}
        {(a.state === "Resolved" || a.state === "False Alarm") && (
          <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.setAlarmState(a.id, "New", note(a) || "Reopened"))}>
            Reopen
          </button>
        )}
        {a.hasClip && !a.evidence && (
          <button className="btn small" disabled={busy === a.id} onClick={() => act(a, () => api.lockEvidence(a.id))}>
            Lock evidence
          </button>
        )}
        <input
          type="text"
          className="dash-alarm-note"
          maxLength={500}
          value={notes[a.id] ?? ""}
          onChange={(e) => setNotes((n) => ({ ...n, [a.id]: e.target.value }))}
          placeholder="Note (optional)"
          aria-label="Note for the response log"
        />
        <span className="grow" />
        <button className="btn small" onClick={() => setOpenLog(openLog === a.id ? null : a.id)}>
          {openLog === a.id ? "Hide log" : "Log"}
        </button>
      </>
    );
  }
}

export function when(unix: number): string {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 60) return "Just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  const d = new Date(unix * 1000);
  if (s < 86400) return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return d.toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}
