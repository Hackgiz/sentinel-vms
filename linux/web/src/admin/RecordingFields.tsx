// Wave 22: the "Recording" section of the camera editor: mode, trigger,
// pre/post-roll and how long each tier is kept.
import type { ReactNode } from "react";
import {
  keepDayChoices,
  lowResDaysOf,
  modeChoices,
  modeLabel,
  noTriggerWarning,
  postRollChoices,
  preRollChoices,
  recommendedMode,
  recordingDefaults,
  resolveMode,
  storageEstimate,
  triggerChoices,
  triggerLabel,
  usesEvents,
  type CameraRecordTrigger,
  type CameraRecordingMode,
  type RecordingForm,
  type RecordingSettings,
} from "../shared/recordingModes";

const days = (d: number) => `${d} day${d === 1 ? "" : "s"}`;

function KeepSelect({ value, onChange, defaultLabel, label }: { value: number; onChange: (d: number) => void; defaultLabel: string; label: string }) {
  return (
    <label className="field">
      <span>{label}</span>
      <select value={value} onChange={(e) => onChange(Number(e.target.value))}>
        <option value={0}>{defaultLabel}</option>
        {keepDayChoices.map((d) => (
          <option key={d} value={d}>
            {days(d)}
          </option>
        ))}
        {value !== 0 && !keepDayChoices.includes(value) && <option value={value}>{days(value)}</option>}
      </select>
    </label>
  );
}

function SecondsSelect({ value, onChange, choices, defaultS, label }: { value: number; onChange: (s: number) => void; choices: number[]; defaultS: number; label: string }) {
  return (
    <label className="field">
      <span>{label}</span>
      <select value={value} onChange={(e) => onChange(Number(e.target.value))}>
        <option value={0}>Server default ({defaultS} s)</option>
        {choices.map((s) => (
          <option key={s} value={s}>
            {s} s
          </option>
        ))}
        {value !== 0 && !choices.includes(value) && <option value={value}>{value} s</option>}
      </select>
    </label>
  );
}

export default function RecordingFields({
  form: f,
  onChange,
  hasSub,
  motionLevel,
  analytics,
  server,
  kbps,
  legacy = false,
  onFindStreams,
}: {
  form: RecordingForm;
  onChange: (patch: Partial<RecordingForm>) => void;
  /** The camera has a sub-stream (needed for dual). */
  hasSub: boolean;
  motionLevel: number;
  /** Camera analytics on (ONVIF address and the box ticked). */
  analytics: boolean;
  /** Server defaults; null when they couldn't be read. */
  server: RecordingSettings | null;
  /** The main stream's live ingest rate (System Monitor), for the GB/day line. */
  kbps?: number | null;
  /** The server predates recording modes: only 24/7 or off. */
  legacy?: boolean;
  /** Saved cameras: the disabled dual option's hint links to Find streams. */
  onFindStreams?: () => void;
}) {
  const d = server ?? recordingDefaults;
  const effective = resolveMode(f.recordingMode, server?.defaultMode, hasSub);
  const recommended = recommendedMode(hasSub);
  const warning = noTriggerWarning(effective, motionLevel, analytics);
  const estimate = storageEstimate(effective, kbps);
  const dualFellBack = f.recordingMode === "default" && server?.defaultMode === "dual" && !hasSub;
  const pick = (m: CameraRecordingMode) => onChange({ recordingMode: m });

  const needsSub: ReactNode = onFindStreams ? (
    <>
      Needs a second stream.{" "}
      <button
        type="button"
        className="linkish"
        onClick={(e) => {
          e.preventDefault(); // inside the option's <label>
          onFindStreams();
        }}
      >
        Find streams
      </button>{" "}
      to add one.
    </>
  ) : (
    "Needs a second stream. Add one above to use this."
  );
  const options: { value: CameraRecordingMode; label: string; line: ReactNode; disabled?: boolean; tag?: string }[] = legacy
    ? [
        { value: "default", label: "Record 24/7", line: "Records the main stream around the clock." },
        { value: "off", label: "Off (live only)", line: "Live view only. Nothing is recorded." },
      ]
    : [
        {
          value: "default",
          label: server ? `Use server default (${modeLabel(server.defaultMode)})` : "Use server default",
          line: "Follows the server's recording settings (Admin → Servers → Recording).",
        },
        ...modeChoices.map((m) => ({
          value: m.value as CameraRecordingMode,
          label: m.label,
          line: m.value === "dual" && !hasSub ? needsSub : m.line,
          disabled: m.value === "dual" && !hasSub,
          tag: m.value === recommended ? "Recommended" : undefined,
        })),
      ];

  return (
    <fieldset className="field rec-section">
      <legend>Recording</legend>
      <div className="rec-modes" role="radiogroup" aria-label="Recording mode">
        {options.map((o) => (
          <label key={o.value} className={`rec-mode${o.disabled ? " disabled" : ""}${f.recordingMode === o.value ? " on" : ""}`}>
            <input type="radio" name="recordingMode" value={o.value} checked={f.recordingMode === o.value} disabled={o.disabled} onChange={() => pick(o.value)} />
            <span>
              <strong>{o.label}</strong>
              {o.tag && <span className="badge green rec-tag">{o.tag}</span>}
              <small>{o.line}</small>
            </span>
          </label>
        ))}
      </div>
      {dualFellBack && <div className="notice small">This camera has no second stream, so the server default records events only here.</div>}
      {warning && <div className="notice warn">{warning}</div>}
      {estimate && <p className="muted small rec-estimate">{estimate}</p>}
      {!legacy && usesEvents(effective) && (
        <div className="grid-3">
          <label className="field">
            <span>Start recording on</span>
            <select value={f.recordTrigger} onChange={(e) => onChange({ recordTrigger: e.target.value as CameraRecordTrigger })}>
              <option value="default">Server default ({triggerLabel(d.defaultTrigger)})</option>
              {triggerChoices.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </select>
          </label>
          <SecondsSelect label="Before the event" value={f.preRollSeconds} onChange={(s) => onChange({ preRollSeconds: s })} choices={preRollChoices} defaultS={d.defaultPreRollSeconds} />
          <SecondsSelect label="After the event" value={f.postRollSeconds} onChange={(s) => onChange({ postRollSeconds: s })} choices={postRollChoices} defaultS={d.defaultPostRollSeconds} />
        </div>
      )}
      {effective !== "off" && (
        <div className="grid-2">
          <KeepSelect
            label={effective === "dual" ? "Keep full-res" : "Keep recordings"}
            value={f.retentionDays}
            onChange={(v) => onChange({ retentionDays: v })}
            defaultLabel={server ? `Server default (${days(d.defaultRetentionDays)})` : "Server default"}
          />
          {!legacy && effective === "dual" && (
            <KeepSelect
              label="Keep low-res"
              value={f.lowResRetentionDays}
              onChange={(v) => onChange({ lowResRetentionDays: v })}
              defaultLabel={`Server default (${days(lowResDaysOf(0, server, f.retentionDays || d.defaultRetentionDays))})`}
            />
          )}
        </div>
      )}
    </fieldset>
  );
}
