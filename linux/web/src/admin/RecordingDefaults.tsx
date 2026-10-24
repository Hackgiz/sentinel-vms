// Wave 22: the server's recording defaults (GET/PATCH /api/v2/settings/recording):
// mode, trigger, pre/post-roll, how long each tier is kept, and deleting the
// oldest video when the disk runs low. Shown as Admin → Servers → Recording
// and as the Recording card on the System page.
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { ApiError } from "../shared/api";
import { useLoad } from "../shared/hooks";
import {
  clampMinFree,
  keepDayChoices,
  minFreeChoices,
  modeChoices,
  postRollChoices,
  preRollChoices,
  recordingApi,
  rollOrDefault,
  triggerChoices,
  type RecordTrigger,
  type RecordingMode,
  type RecordingSettings,
} from "../shared/recordingModes";
import { v2 } from "./client";
import { ErrorBanner, Loading } from "./ui";

const days = (d: number) => `${d} day${d === 1 ? "" : "s"}`;

function withCurrent(choices: number[], v: number) {
  return choices.includes(v) ? choices : [...choices, v].sort((a, b) => a - b);
}

/** The changed fields only. */
export function settingsPatch(cur: RecordingSettings, next: RecordingSettings): Partial<RecordingSettings> {
  const p: Partial<RecordingSettings> = {};
  for (const k of Object.keys(next) as (keyof RecordingSettings)[]) if (next[k] !== cur[k]) (p as Record<string, unknown>)[k] = next[k];
  return p;
}

/** "Delete the oldest video when free space drops under …": "Off" or "10%". */
export const minFreeLabel = (pct: number) => (clampMinFree(pct) === 0 ? "Off" : `${clampMinFree(pct)}%`);

function DefaultsForm({ settings, canEdit, onSaved }: { settings: RecordingSettings; canEdit: boolean; onSaved: (s: RecordingSettings) => void }) {
  const [f, setF] = useState(settings);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => setF(settings), [settings]);
  const set = (patch: Partial<RecordingSettings>) => {
    setSaved(false);
    setF((x) => ({ ...x, ...patch }));
  };
  const changes = settingsPatch(settings, f);
  const mode = modeChoices.find((m) => m.value === f.defaultMode);
  // WAVE24(D1): null/absent reads as the built-in default; 0 is a literal 0 s.
  const pre = rollOrDefault(f.defaultPreRollSeconds, 5);
  const post = rollOrDefault(f.defaultPostRollSeconds, 15);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      onSaved(await recordingApi.patch(v2, changes));
      setSaved(true);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} aria-label="Recording defaults">
      <ErrorBanner error={error} />
      {saved && (
        <div className="notice success" role="status">
          Saved. Applies to cameras that use the server default.
        </div>
      )}
      <label className="field">
        <span>Default mode</span>
        <select value={f.defaultMode} disabled={!canEdit} onChange={(e) => set({ defaultMode: e.target.value as RecordingMode })}>
          {modeChoices.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </select>
        <small>
          {mode?.line}
          {f.defaultMode === "dual" ? " Cameras without a sub-stream record events only." : ""}
        </small>
      </label>
      <div className="grid-3">
        <label className="field">
          <span>Start recording on</span>
          <select value={f.defaultTrigger} disabled={!canEdit} onChange={(e) => set({ defaultTrigger: e.target.value as RecordTrigger })}>
            {triggerChoices.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </select>
          <small>For events and low-res modes.</small>
        </label>
        <label className="field">
          <span>Before the event</span>
          <select value={pre} disabled={!canEdit} onChange={(e) => set({ defaultPreRollSeconds: Number(e.target.value) })}>
            {withCurrent(preRollChoices, pre).map((s) => (
              <option key={s} value={s}>
                {s} s
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>After the event</span>
          <select value={post} disabled={!canEdit} onChange={(e) => set({ defaultPostRollSeconds: Number(e.target.value) })}>
            {withCurrent(postRollChoices, post).map((s) => (
              <option key={s} value={s}>
                {s} s
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="grid-2">
        <label className="field">
          <span>Keep full-res recordings</span>
          <select value={f.defaultRetentionDays} disabled={!canEdit} onChange={(e) => set({ defaultRetentionDays: Number(e.target.value) })}>
            {withCurrent(keepDayChoices, f.defaultRetentionDays).map((d) => (
              <option key={d} value={d}>
                {days(d)}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>Keep low-res recordings</span>
          <select value={f.defaultLowResRetentionDays} disabled={!canEdit} onChange={(e) => set({ defaultLowResRetentionDays: Number(e.target.value) })}>
            {withCurrent(keepDayChoices, f.defaultLowResRetentionDays).map((d) => (
              <option key={d} value={d}>
                {days(d)}
              </option>
            ))}
          </select>
          <small>The 24/7 sub-stream in low-res mode.</small>
        </label>
      </div>
      <label className="field">
        <span>Delete the oldest video when free space drops under</span>
        <select value={clampMinFree(f.minFreePercent)} disabled={!canEdit} onChange={(e) => set({ minFreePercent: Number(e.target.value) })}>
          <option value={0}>Off</option>
          {withCurrent(minFreeChoices, clampMinFree(f.minFreePercent))
            .filter((p) => p > 0)
            .map((p) => (
              <option key={p} value={p}>
                {p}%
              </option>
            ))}
        </select>
        <small>Low-res video goes first. Evidence locks are never deleted.</small>
      </label>
      {canEdit && (
        <button className="btn primary" disabled={busy || Object.keys(changes).length === 0}>
          {busy ? "Saving…" : "Save"}
        </button>
      )}
    </form>
  );
}

/** The card body; `fallback` shows instead when the server has no recording settings (older server). */
export function RecordingDefaultsCard({ canEdit, fallback }: { canEdit: boolean; fallback?: ReactNode }) {
  const s = useLoad(() => recordingApi.get(v2), []);
  if (s.error && fallback && s.error instanceof ApiError && s.error.status === 404) return <>{fallback}</>;
  return (
    <div className="card">
      <h2>Recording</h2>
      <ErrorBanner error={s.error} onRetry={s.reload} />
      {s.data ? <DefaultsForm settings={s.data} canEdit={canEdit} onSaved={(n) => s.setData(n)} /> : s.error ? null : <Loading />}
    </div>
  );
}

/** Admin → Servers → Recording. */
export default function RecordingSection({ canEdit }: { canEdit: boolean }) {
  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Recording</h1>
          <div className="sub">What cameras set to "Use server default" record, and how long video is kept</div>
        </div>
      </div>
      <RecordingDefaultsCard canEdit={canEdit} />
    </>
  );
}
