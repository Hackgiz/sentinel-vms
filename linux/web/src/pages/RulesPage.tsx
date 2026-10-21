import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { api, can, type Camera, type User } from "../api";
import { dayNames, minuteToTime, rulesApi, timeToMinute, type AlarmRule, type PushStatus, type RulesResponse } from "../rulesApi";
import "./rules.css";

const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };

const blankRule: Omit<AlarmRule, "id"> = {
  name: "",
  enabled: true,
  cameraIDs: [],
  kinds: ["Person"],
  minSeverity: "Info",
  schedule: false,
  fromMinute: 22 * 60,
  toMinute: 6 * 60,
  days: [],
  delivery: "push",
  instructions: "",
};

export default function RulesPage({ user }: { user: User }) {
  const [data, setData] = useState<RulesResponse | null>(null);
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<AlarmRule | Omit<AlarmRule, "id"> | null>(null);
  const [busy, setBusy] = useState(false);
  const mayEdit = can.changeSettings(user);

  const load = useCallback(() => {
    rulesApi.list().then(setData).catch((e) => setError(e.message));
  }, []);
  useEffect(() => {
    load();
    api.cameras().then(setCameras).catch(() => {});
  }, [load]);

  const camName = (id: string) => cameras.find((c) => c.id === id)?.name ?? "Removed camera";

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      load();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setBusy(false);
    }
  }

  const rules = data?.rules ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Alarm Rules</h1>
          <div className="sub">
            Rules add operator instructions to matching alarms and decide which alarms reach paired iPhones. Each camera's alarm setting still decides what raises an
            alarm.{data && ` Times are server time (${data.timeZone}).`}
          </div>
        </div>
        {mayEdit && (
          <button className="btn primary" onClick={() => setEditing({ ...blankRule })}>
            + New rule
          </button>
        )}
      </div>
      {error && !editing && <div className="error">{error}</div>}

      {data && rules.length === 0 ? (
        <div className="card rule-list">
          <div className="empty">
            <div className="big">⚐</div>
            No alarm rules. Alarms still appear on the dashboard; without a rule they get no instructions and aren't pushed to iPhones.
            {mayEdit && (
              <div style={{ marginTop: 14 }}>
                <button className="btn" disabled={busy} onClick={() => run(() => rulesApi.addSuggested())}>
                  Add suggested rules
                </button>
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="rule-list">
          {rules.map((r) => (
            <div key={r.id} className={`card rule-row ${r.enabled ? "" : "off"}`}>
              <div className="rule-head">
                <strong>{r.name}</strong>
                {mayEdit ? (
                  <label className="switch-row small">
                    <input type="checkbox" checked={r.enabled} disabled={busy} onChange={() => run(() => rulesApi.update({ ...r, enabled: !r.enabled }))} />
                    {r.enabled ? "On" : "Off"}
                  </label>
                ) : (
                  <span className={`badge ${r.enabled ? "green" : "blue"}`}>{r.enabled ? "On" : "Off"}</span>
                )}
              </div>
              <div className="rule-pills">
                <span className="badge blue">{r.kinds.length ? r.kinds.join(", ") : "Any alarm"}</span>
                <span className={`badge ${sevClass[r.minSeverity] ?? "blue"}`}>{r.minSeverity === "Info" ? "Any severity" : `${r.minSeverity}+`}</span>
                <span className="badge blue">
                  {r.cameraIDs.length === 0 ? "All cameras" : r.cameraIDs.length <= 2 ? r.cameraIDs.map(camName).join(", ") : `${r.cameraIDs.length} cameras`}
                </span>
                {r.schedule && (
                  <span className="badge blue">
                    {minuteToTime(r.fromMinute)}–{minuteToTime(r.toMinute)}
                    {r.toMinute < r.fromMinute ? " (overnight)" : ""}
                  </span>
                )}
                {r.days.length > 0 && <span className="badge blue">{r.days.map((d) => dayNames[d]).join(" ")}</span>}
                <span className={`badge ${r.delivery === "push" ? "amber" : "blue"}`}>{r.delivery === "push" ? "Dashboard + iPhone push" : "Dashboard only"}</span>
                <span className="grow" />
                {mayEdit && (
                  <button className="btn small" onClick={() => setEditing(r)}>
                    Edit
                  </button>
                )}
              </div>
              {r.instructions && <div className="rule-ins">{r.instructions}</div>}
            </div>
          ))}
        </div>
      )}

      {mayEdit && <PushSettings />}

      {editing && data && (
        <RuleEditor
          rule={editing}
          kinds={data.kinds}
          cameras={cameras}
          busy={busy}
          error={error}
          onCancel={() => {
            setEditing(null);
            setError("");
          }}
          onSave={async (r) => {
            const ok = await run(() => ("id" in r && r.id ? rulesApi.update(r as AlarmRule) : rulesApi.create(r)));
            if (ok) setEditing(null);
          }}
          onDelete={
            "id" in editing && editing.id
              ? async () => {
                  const id = (editing as AlarmRule).id;
                  if (!window.confirm(`Delete "${editing.name}"?`)) return;
                  if (await run(() => rulesApi.remove(id))) setEditing(null);
                }
              : undefined
          }
        />
      )}
    </>
  );
}

/** The rule form; also used by Sentinel Admin (admin/RulesSection.tsx) for API v2 rules. */
export function RuleEditor(props: {
  rule: AlarmRule | Omit<AlarmRule, "id">;
  kinds: string[];
  cameras: Pick<Camera, "id" | "name">[];
  busy: boolean;
  error: string;
  onCancel: () => void;
  onSave: (r: AlarmRule | Omit<AlarmRule, "id">) => void;
  onDelete?: () => void;
  /** Extra form fields (Sentinel Admin: time profile, webhook actions), shown above "Rule is on". */
  extra?: ReactNode;
  /** Shown above the fields (Sentinel Admin: a drafted rule's explanation and warnings). */
  intro?: ReactNode;
  /** Shown under "All cameras" while it's ticked (Sentinel Admin: drafts and templates). */
  allCamerasNote?: string;
}) {
  const [r, setR] = useState(props.rule);
  const isNew = !("id" in props.rule && props.rule.id);
  const set = (patch: Partial<AlarmRule>) => setR((x) => ({ ...x, ...patch }));
  const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const allCameras = r.cameraIDs.length === 0;
  const [pickCameras, setPickCameras] = useState(!allCameras);

  const problem =
    r.name.trim() === ""
      ? "Give the rule a name."
      : pickCameras && r.cameraIDs.length === 0
        ? "Pick at least one camera, or choose all cameras."
        : r.schedule && r.fromMinute === r.toMinute
          ? "The schedule starts and ends at the same time."
          : "";

  function submit(e: FormEvent) {
    e.preventDefault();
    if (problem) return;
    props.onSave({ ...r, cameraIDs: pickCameras ? r.cameraIDs : [] });
  }

  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && props.onCancel()}>
      <form className="modal wide" onSubmit={submit}>
        <h2>{isNew ? "New alarm rule" : "Edit alarm rule"}</h2>
        {props.error && <div className="error">{props.error}</div>}
        {props.intro}
        <label className="field">
          <span>Name</span>
          <input type="text" value={r.name} maxLength={120} placeholder="e.g. After-hours person at loading dock" onChange={(e) => set({ name: e.target.value })} autoFocus />
        </label>

        <div className="field">
          <label className="field" style={{ marginBottom: 6 }}>
            <span>When</span>
          </label>
          <div className="chip-grid">
            <label className={`chip ${r.kinds.length === 0 ? "on" : ""}`}>
              <input type="checkbox" checked={r.kinds.length === 0} onChange={() => set({ kinds: [] })} />
              Any alarm
            </label>
            {props.kinds.map((k) => (
              <label key={k} className={`chip ${r.kinds.includes(k) ? "on" : ""}`}>
                <input type="checkbox" checked={r.kinds.includes(k)} onChange={() => set({ kinds: toggle(r.kinds, k) })} />
                {k}
              </label>
            ))}
          </div>
        </div>

        <label className="field" style={{ marginTop: 12 }}>
          <span>Minimum severity</span>
          <select value={r.minSeverity} onChange={(e) => set({ minSeverity: e.target.value as AlarmRule["minSeverity"] })}>
            <option value="Info">Any severity</option>
            <option value="Warning">Warning or Critical</option>
            <option value="Critical">Critical only</option>
          </select>
        </label>

        <div className="field">
          <label className="check">
            <input type="checkbox" checked={!pickCameras} onChange={() => setPickCameras(!pickCameras)} />
            All cameras
          </label>
          {!pickCameras && props.allCamerasNote && <small className="hint">{props.allCamerasNote}</small>}
          {pickCameras && (
            <div className="chip-grid">
              {props.cameras.length === 0 && <span className="muted small">No cameras yet.</span>}
              {props.cameras.map((c) => (
                <label key={c.id} className={`chip ${r.cameraIDs.includes(c.id) ? "on" : ""}`}>
                  <input type="checkbox" checked={r.cameraIDs.includes(c.id)} onChange={() => set({ cameraIDs: toggle(r.cameraIDs, c.id) })} />
                  {c.name}
                </label>
              ))}
            </div>
          )}
          <small className="hint" style={{ marginTop: 6 }}>
            Storage alarms aren't tied to a camera and always pass this filter.
          </small>
        </div>

        <div className="field">
          <label className="check">
            <input type="checkbox" checked={r.schedule} onChange={() => set({ schedule: !r.schedule })} />
            Only during certain hours
          </label>
          {r.schedule && (
            <div className="time-row">
              <label className="field">
                <span>From</span>
                <input type="time" value={minuteToTime(r.fromMinute)} onChange={(e) => set({ fromMinute: timeToMinute(e.target.value) })} />
              </label>
              <label className="field">
                <span>Until</span>
                <input type="time" value={minuteToTime(r.toMinute)} onChange={(e) => set({ toMinute: timeToMinute(e.target.value) })} />
              </label>
              {r.toMinute < r.fromMinute && <span className="muted small">Runs overnight, past midnight.</span>}
            </div>
          )}
          <div className="chip-grid" style={{ marginTop: 10 }}>
            {dayNames.map((d, i) => (
              <label key={d} className={`chip ${r.days.includes(i) ? "on" : ""}`}>
                <input type="checkbox" checked={r.days.includes(i)} onChange={() => set({ days: toggle(r.days, i) })} />
                {d}
              </label>
            ))}
          </div>
          <small className="hint" style={{ marginTop: 6 }}>
            No days selected = every day. An overnight window belongs to the day it starts (Fri 22:00–06:00 includes early Saturday).
          </small>
        </div>

        <label className="field">
          <span>Notify</span>
          <select value={r.delivery} onChange={(e) => set({ delivery: e.target.value as AlarmRule["delivery"] })}>
            <option value="push">Dashboard + iPhone push</option>
            <option value="dashboard">Dashboard only</option>
          </select>
        </label>

        <label className="field">
          <span>Operator instructions</span>
          <textarea rows={3} maxLength={2000} value={r.instructions} onChange={(e) => set({ instructions: e.target.value })} />
          <small>Shown on every alarm this rule matches, on the dashboard and in the iPhone app — e.g. "Call site manager at 555-0100, then review the dock camera."</small>
        </label>

        {props.extra}

        <label className="check">
          <input type="checkbox" checked={r.enabled} onChange={() => set({ enabled: !r.enabled })} />
          Rule is on
        </label>

        <div className="actions">
          {props.onDelete && (
            <button type="button" className="btn danger" disabled={props.busy} onClick={props.onDelete}>
              Delete rule
            </button>
          )}
          <span className="grow" />
          {problem && <span className="muted small">{problem}</span>}
          <button type="button" className="btn" onClick={props.onCancel}>
            Cancel
          </button>
          <button className="btn primary" disabled={props.busy || !!problem}>
            {isNew ? "Create rule" : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
}

export function PushSettings() {
  const [st, setSt] = useState<PushStatus | null>(null);
  const [error, setError] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState("");
  const [keyID, setKeyID] = useState("");
  const [teamID, setTeamID] = useState("");
  const [topic, setTopic] = useState("");

  const load = useCallback(() => {
    rulesApi
      .push()
      .then((s) => {
        setSt(s);
        setKeyID(s.apnsKeyID);
        setTeamID(s.apnsTeamID);
        setTopic(s.apnsTopic);
      })
      .catch((e) => setError(e.message));
  }, []);
  useEffect(load, [load]);

  async function save(patch: Parameters<typeof rulesApi.savePush>[0], done = "") {
    setBusy(true);
    setError("");
    setNote("");
    try {
      const s = await rulesApi.savePush(patch);
      setSt(s);
      setKey("");
      if (done) setNote(done);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function test() {
    setBusy(true);
    setError("");
    setNote("");
    try {
      const r = await rulesApi.testPush();
      setNote(`Test sent to ${r.sent} iPhone${r.sent === 1 ? "" : "s"}.${r.failed.length ? " Some failed: " + r.failed.join("; ") : ""}`);
      load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return error ? <div className="error">{error}</div> : null;

  return (
    <div className="card push-card">
      <h2>iPhone push notifications</h2>
      <div className="muted small">
        Alarms matching a rule set to "Dashboard + iPhone push" are sent to every paired iPhone that allowed notifications ({st.pushDevices} of {st.devices} paired). A
        burst of alarms from one camera sends one notification per minute.
      </div>
      {error && (
        <div className="error" style={{ marginTop: 10 }}>
          {error}
        </div>
      )}
      {note && (
        <div className="notice" style={{ marginTop: 10 }}>
          {note}
        </div>
      )}

      <div className="push-modes">
        <label className={`push-mode ${st.mode === "off" ? "on" : ""}`}>
          <input type="radio" name="pushmode" checked={st.mode === "off"} disabled={busy} onChange={() => save({ mode: "off" }, "Push notifications are off.")} />
          <div>
            <strong>Off</strong>
            <div className="muted small">Alarms show on the dashboard and in the iPhone app's Alarms tab only.</div>
          </div>
        </label>
        {/* WAVE12(C): Sentinel Cloud's push relay */}
        <label className={`push-mode ${st.mode === "cloud" ? "on" : ""}`}>
          <input
            type="radio"
            name="pushmode"
            checked={st.mode === "cloud"}
            disabled={busy || !st.cloudReady}
            onChange={() => save({ mode: "cloud" }, "Sending through Sentinel Cloud.")}
          />
          <div>
            <strong>Sentinel Cloud</strong>
            <div className="muted small">
              {st.cloudReady
                ? "For the App Store Sentinel Mobile app: alerts go through Sentinel Cloud's push relay to iPhones signed in to this server."
                : "Link this server to Sentinel Cloud to use it."}
            </div>
          </div>
        </label>
        <label className={`push-mode ${st.mode === "relay" ? "on" : ""}`}>
          <input type="radio" name="pushmode" checked={st.mode === "relay"} disabled={busy} onChange={() => save({ mode: "relay" }, "Registered with the Sentinel push relay.")} />
          <div>
            <strong>Sentinel push relay</strong>
            <div className="muted small">
              For the App Store Sentinel Mobile app. This server registers once with {st.relayURL} and sends alert text, camera name and alarm id through it; the relay
              only delivers to iPhones paired with this server.
            </div>
          </div>
        </label>
        <label className={`push-mode ${st.mode === "direct" ? "on" : ""}`}>
          <input
            type="radio"
            name="pushmode"
            checked={st.mode === "direct"}
            disabled={busy || !st.apnsHasKey}
            onChange={() => save({ mode: "direct" }, "Sending directly to Apple.")}
          />
          <div>
            <strong>Direct to Apple (your own APNs key)</strong>
            <div className="muted small">For people who build Sentinel Mobile themselves under their own Apple team. Enter the key below first.</div>
          </div>
        </label>
      </div>

      <details open={st.mode === "direct"}>
        <summary className="small" style={{ cursor: "pointer", marginBottom: 10 }}>
          Your own APNs key {st.apnsHasKey ? "(saved)" : ""}
        </summary>
        <label className="field">
          <span>AuthKey_XXXXXXXXXX.p8 contents</span>
          <textarea
            rows={4}
            value={key}
            placeholder={st.apnsHasKey ? "Saved — paste a new key to replace it" : "Paste the contents of the .p8 key file"}
            onChange={(e) => setKey(e.target.value)}
            style={{ width: "100%" }}
          />
          <small>Stored encrypted with this server's install key.</small>
        </label>
        <div className="time-row">
          <label className="field">
            <span>Key ID</span>
            <input type="text" value={keyID} maxLength={10} onChange={(e) => setKeyID(e.target.value.toUpperCase())} />
          </label>
          <label className="field">
            <span>Team ID</span>
            <input type="text" value={teamID} maxLength={10} onChange={(e) => setTeamID(e.target.value.toUpperCase())} />
          </label>
          <label className="field">
            <span>App bundle ID</span>
            <input type="text" value={topic} onChange={(e) => setTopic(e.target.value)} />
          </label>
          <button className="btn" disabled={busy} onClick={() => save({ apnsKey: key || undefined, apnsKeyID: keyID, apnsTeamID: teamID, apnsTopic: topic }, "APNs key saved.")}>
            Save key
          </button>
        </div>
      </details>

      <div className="field" style={{ marginTop: 14 }}>
        <label className="check">
          <input type="checkbox" checked={st.quietEnabled} disabled={busy} onChange={() => save({ quietEnabled: !st.quietEnabled })} />
          Quiet hours — only Critical alarms are pushed
        </label>
        {st.quietEnabled && (
          <div className="time-row">
            <label className="field">
              <span>From</span>
              <input type="time" value={minuteToTime(st.quietFrom)} disabled={busy} onChange={(e) => save({ quietFrom: timeToMinute(e.target.value) })} />
            </label>
            <label className="field">
              <span>Until</span>
              <input type="time" value={minuteToTime(st.quietTo)} disabled={busy} onChange={(e) => save({ quietTo: timeToMinute(e.target.value) })} />
            </label>
          </div>
        )}
      </div>

      <div className="switch-row" style={{ marginTop: 6 }}>
        <button className="btn" disabled={busy || !st.ready || st.pushDevices === 0} onClick={test}>
          Send test push
        </button>
        <span className="muted small">
          {st.ready ? `Ready · ${st.sent} sent${st.failed ? `, ${st.failed} failed` : ""}${st.dropped ? `, ${st.dropped} dropped` : ""}` : "Not set up"}
          {st.lastSentAt ? ` · last ${new Date(st.lastSentAt * 1000).toLocaleString()}` : ""}
        </span>
      </div>
      {st.lastError && <div className="muted small" style={{ marginTop: 6 }}>Last error: {st.lastError}</div>}
    </div>
  );
}
