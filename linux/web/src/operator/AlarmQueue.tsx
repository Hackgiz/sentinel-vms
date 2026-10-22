// The alarm queue: open alarms live from the event stream, the selected
// alarm's instructions, live video and response log, state changes
// (AlarmState in openapi.yaml) and evidence lock.
import { useEffect, useMemo, useState } from "react";
import { errorMessage, fetchAll, type Alarm, type AlarmState, type Evidence } from "../shared/api";
import { fmtAgo } from "../shared/time";
import VideoTile from "../shared/VideoTile";
import { KindIcon } from "../shared/KindIcon";
import { AlarmCard } from "../shared/Cards"; // WAVE20
import { useData } from "./data";
import { useSigned } from "./session";
import type { PlaybackRequest } from "./Workspace";

const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };
const stateClass: Record<string, string> = { New: "red", Acknowledged: "amber", Investigating: "amber", Snoozed: "blue", Resolved: "green", "False Alarm": "blue" };
const sevRank: Record<string, number> = { Critical: 0, Warning: 1, Info: 2 };

/** The next states an operator can move an alarm to, by its state. */
export function nextStates(state: AlarmState): { state: AlarmState; label: string; primary?: boolean }[] {
  switch (state) {
    case "New":
      return [
        { state: "Acknowledged", label: "Acknowledge", primary: true },
        { state: "Investigating", label: "Investigate" },
        { state: "Snoozed", label: "Snooze" },
        { state: "False Alarm", label: "False alarm" },
      ];
    case "Acknowledged":
      return [
        { state: "Investigating", label: "Investigate", primary: true },
        { state: "Resolved", label: "Resolve" },
        { state: "Snoozed", label: "Snooze" },
        { state: "False Alarm", label: "False alarm" },
      ];
    case "Investigating":
      return [
        { state: "Resolved", label: "Resolve", primary: true },
        { state: "False Alarm", label: "False alarm" },
      ];
    case "Snoozed":
      return [
        { state: "Acknowledged", label: "Acknowledge", primary: true },
        { state: "Resolved", label: "Resolve" },
      ];
    default:
      return [];
  }
}

/** New first, then by severity, then newest activity. */
export function queueOrder(a: Alarm, b: Alarm): number {
  const na = a.state === "New" ? 0 : 1;
  const nb = b.state === "New" ? 0 : 1;
  return na - nb || (sevRank[a.severity] ?? 3) - (sevRank[b.severity] ?? 3) || b.lastEventAt - a.lastEventAt;
}

export default function AlarmQueue({ onPlayback, onOpenManager }: { onPlayback: (r: PlaybackRequest) => void; onOpenManager?: (alarmId: string) => void }) {
  const { session, can } = useSigned();
  const data = useData();
  const [showClosed, setShowClosed] = useState(false);
  const [closed, setClosed] = useState<Alarm[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [snooze, setSnooze] = useState(15);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [evidence, setEvidence] = useState<Record<string, Evidence>>({});

  // Closed alarms are read on demand (the stream keeps the open ones current).
  useEffect(() => {
    if (!showClosed) return;
    let alive = true;
    fetchAll((cursor) => session.client.alarms.page({ state: "all", cursor, limit: 200 }), 400)
      .then((all) => alive && setClosed(all.filter((a) => a.state === "Resolved" || a.state === "False Alarm")))
      .catch((e) => alive && setError(errorMessage(e)));
    return () => {
      alive = false;
    };
  }, [showClosed, session.client]);

  const list = useMemo(() => (showClosed ? [...closed].sort((a, b) => b.updatedAt - a.updatedAt) : [...data.alarms].sort(queueOrder)), [showClosed, closed, data.alarms]);
  const alarm = list.find((a) => a.id === selected) ?? list[0] ?? null;
  const camera = alarm?.cameraId ? data.camera(alarm.cameraId) : undefined;

  useEffect(() => {
    setNote("");
    setError("");
  }, [alarm?.id]);

  async function setState(a: Alarm, state: AlarmState) {
    setBusy(true);
    setError("");
    try {
      const updated = await session.client.alarms.setState(a.id, state, note.trim() || undefined, state === "Snoozed" ? snooze : undefined);
      data.putAlarm(updated);
      if (!showClosed && (state === "Resolved" || state === "False Alarm")) setSelected(null);
      setNote("");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function lock(a: Alarm) {
    setBusy(true);
    setError("");
    try {
      const ev = await session.client.alarms.lockEvidence(a.id);
      setEvidence((m) => ({ ...m, [a.id]: ev }));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const mayAct = can("acknowledgeAlarms");
  return (
    <div className="op-alarms">
      <aside className="op-alarm-list" aria-label="Alarms">
        <div className="op-alarm-list-head">
          <div className="seg">
            <button className={!showClosed ? "on" : ""} onClick={() => setShowClosed(false)}>
              Open {data.alarms.length > 0 && `(${data.alarms.length})`}
            </button>
            <button className={showClosed ? "on" : ""} onClick={() => setShowClosed(true)}>
              Closed
            </button>
          </div>
        </div>
        {list.length === 0 && (
          <div className="empty">
            <div className="big">✓</div>
            {showClosed ? "No closed alarms." : "No open alarms."}
          </div>
        )}
        {list.map((a) => (
          <AlarmCard
            key={a.id}
            alarm={a}
            selected={a.id === alarm?.id}
            attention={a.state === "New"}
            onOpen={() => setSelected(a.id)}
            meta={`${a.cameraName || a.kind} · ${fmtAgo(a.lastEventAt)}${a.eventCount > 1 ? ` · ${a.eventCount} events` : ""}${a.owner ? ` · ${a.owner}` : ""}`}
          />
        ))}
      </aside>

      <section className="op-alarm-detail">
        {!alarm ? (
          <div className="empty">Select an alarm.</div>
        ) : (
          <>
            <div className="alarm-head">
              <span className={`badge ${sevClass[alarm.severity] ?? "blue"}`}>{alarm.severity}</span>
              <h2 className="grow">
                <KindIcon kind={alarm.kind} />
                {alarm.title}
                {alarm.cameraName && <span className="muted"> · {alarm.cameraName}</span>}
              </h2>
              <span className={`badge ${stateClass[alarm.state] ?? "blue"}`}>{alarm.state}</span>
            </div>
            <div className="alarm-detail">{alarm.detail}</div>
            <div className="muted small">
              {alarm.kind} · first {new Date(alarm.createdAt).toLocaleString()} · last {new Date(alarm.lastEventAt).toLocaleString()}
              {alarm.owner && ` · owner ${alarm.owner}`}
            </div>

            {alarm.instructions && alarm.instructions.length > 0 && (
              <div className="op-instructions" role="note">
                <div className="op-kicker">Instructions</div>
                {alarm.instructions.map((i, n) => (
                  <div key={n} className="op-instruction">
                    <strong>{i.rule}</strong>
                    <div className="op-pre">{i.text}</div>
                  </div>
                ))}
              </div>
            )}

            <div className="op-alarm-video">
              {camera ? (
                <VideoTile client={session.client} camera={camera} route={session.route} webrtc={data.webrtcFor(camera)} />
              ) : (
                <div className="tile">
                  <div className="placeholder">{alarm.cameraId ? "Camera not available" : "No camera for this alarm"}</div>
                </div>
              )}
            </div>

            {error && <div className="error op-mt">{error}</div>}
            {mayAct && nextStates(alarm.state).length > 0 && (
              <>
                <label className="field op-mt">
                  <span>Note (goes into the response log)</span>
                  <input type="text" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} placeholder="What you saw or did" />
                </label>
                <div className="alarm-actions">
                  {nextStates(alarm.state).map((n) => (
                    <button key={n.state} className={`btn ${n.primary ? "primary" : ""}`} disabled={busy} onClick={() => void setState(alarm, n.state)}>
                      {n.label}
                      {n.state === "Snoozed" && ` ${snooze} min`}
                    </button>
                  ))}
                  {nextStates(alarm.state).some((n) => n.state === "Snoozed") && (
                    <select className="select-inline" aria-label="Snooze for" value={snooze} onChange={(e) => setSnooze(Number(e.target.value))}>
                      {[5, 15, 30, 60, 240].map((m) => (
                        <option key={m} value={m}>
                          {m} min
                        </option>
                      ))}
                    </select>
                  )}
                </div>
              </>
            )}
            <div className="alarm-actions">
              {alarm.cameraId && (
                <button className="btn" onClick={() => onPlayback({ cameraId: alarm.cameraId!, at: alarm.createdAt - 10_000 })}>
                  ▶ Play recording
                </button>
              )}
              {onOpenManager && (
                <button className="btn" onClick={() => onOpenManager(alarm.id)} title="Open this alarm in the Alarm Manager">
                  Open in Alarm Manager
                </button>
              )}
              {can("exportEvidence") && alarm.cameraId && (
                <button className="btn" disabled={busy || !!evidence[alarm.id]} onClick={() => void lock(alarm)} title="Keep the recording behind this alarm as evidence">
                  🔒 Lock evidence
                </button>
              )}
            </div>
            {evidence[alarm.id] && (
              <div className="notice op-mt">
                Locked as case <strong>{evidence[alarm.id].caseId}</strong> · {evidence[alarm.id].range}
                <div className="mono">SHA-256 {evidence[alarm.id].sha256}</div>
              </div>
            )}

            {alarm.responseLog.length > 0 && (
              <>
                <div className="op-kicker op-mt">Response log</div>
                <ul className="response-log">
                  {alarm.responseLog.map((l, i) => (
                    <li key={i}>{l}</li>
                  ))}
                </ul>
              </>
            )}
          </>
        )}
      </section>
    </div>
  );
}
