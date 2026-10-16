import { useCallback, useEffect, useState } from "react";
import { ApiError, errorMessage } from "../shared/api";
import { v2 } from "./client";
import { ErrorBanner, Loading, Modal } from "./ui";
import {
  bounded,
  changeOf,
  formOfStream,
  problemWith,
  reconnects,
  resolutionChoices,
  streamTitle,
  useSlider,
  type Stream,
  type StreamForm,
  type StreamRange,
} from "./streamSettings";
import "./streamSettings.css";

// TODO(stream-settings follow-ups): codec switching, audio / multicast / OSD
// settings, and applying one setting to many cameras.

const url = (cameraId: string) => `/api/v2/cameras/${encodeURIComponent(cameraId)}/stream-settings`;

/** Per-stream encoder settings for one camera, asked of the camera itself. */
export default function StreamSettingsDialog({ camera, onClose }: { camera: { id: string; name: string }; onClose: () => void }) {
  const [streams, setStreams] = useState<Stream[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await v2.request<{ streams: Stream[] }>("get", url(camera.id));
      setStreams(r.streams);
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, [camera.id]);

  useEffect(() => {
    void load();
  }, [load]);

  const noOnvif = error instanceof ApiError && error.status === 409;
  return (
    <Modal
      title={`Stream settings — ${camera.name}`}
      wide
      onClose={onClose}
      actions={
        <button type="button" className="btn" onClick={onClose}>
          Close
        </button>
      }
    >
      {error ? (
        <div className="notice" role="status">
          <strong>This camera doesn't support remote configuration.</strong>
          <p className="muted small">
            {noOnvif
              ? "It was added without an ONVIF address, so Sentinel can't read or change its encoder settings. Add the ONVIF address in the camera editor to enable this."
              : "Sentinel couldn't read its encoder settings. The camera may be offline, use an unsupported codec (such as H.265-only) or not offer ONVIF configuration."}
          </p>
          {!noOnvif && <p className="muted small">{errorMessage(error)}</p>}
          {!noOnvif && (
            <button type="button" className="btn small" onClick={() => (setError(null), setStreams(null), void load())}>
              Try again
            </button>
          )}
        </div>
      ) : streams === null ? (
        <Loading />
      ) : (
        <div className="ss-list">
          {streams.map((s) => (
            <StreamCard key={s.configToken} cameraId={camera.id} stream={s} onApplied={load} />
          ))}
        </div>
      )}
    </Modal>
  );
}

function StreamCard({ cameraId, stream, onApplied }: { cameraId: string; stream: Stream; onApplied: () => Promise<void> }) {
  const [form, setForm] = useState<StreamForm>(() => formOfStream(stream));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState(false);
  // A re-fetch after Apply brings the camera's values: show those.
  useEffect(() => setForm(formOfStream(stream)), [stream]);

  const change = changeOf(stream, form);
  const problem = problemWith(stream, form);
  const set = <K extends keyof StreamForm>(k: K, v: StreamForm[K]) => {
    setDone(false);
    setForm((f) => ({ ...f, [k]: v }));
  };
  const o = stream.options;
  const resolutions = resolutionChoices(stream);

  const apply = async () => {
    if (!change) return;
    setBusy(true);
    setError(null);
    try {
      await v2.request<void>("put", `${url(cameraId)}/${encodeURIComponent(stream.configToken)}`, change);
      await onApplied();
      setDone(true);
    } catch (e) {
      setError(e); // the camera's own text, verbatim
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="ss-card card" aria-label={streamTitle(stream)}>
      <header className="ss-head">
        <h3>{streamTitle(stream)}</h3>
        {stream.inUse && stream.name && <span className="muted small">{stream.name}</span>}
        <span className="badge blue" title="The codec can't be changed here">
          {stream.encoding || "Unknown codec"}
        </span>
      </header>
      <div className="grid-2">
        <label className="field">
          <span>Resolution</span>
          {o.resolutions.length > 0 ? (
            <select value={form.resolution} onChange={(e) => set("resolution", e.target.value)} disabled={busy}>
              {resolutions.map((r) => (
                <option key={r.key} value={r.key}>
                  {r.width} × {r.height}
                </option>
              ))}
            </select>
          ) : (
            <input type="text" value={`${stream.current.width} × ${stream.current.height}`} readOnly />
          )}
        </label>
        <Num label="Frame rate (fps)" value={form.fps} range={o.fps} disabled={busy} onChange={(v) => set("fps", v)} />
        <Num label="Bitrate (kbit/s)" value={form.bitrate} range={o.bitrate} disabled={busy} onChange={(v) => set("bitrate", v)} />
        <Num label="Keyframe interval (frames)" value={form.govLength} range={o.gov} disabled={busy} onChange={(v) => set("govLength", v)} />
        <Num label="Quality" value={form.quality} range={o.quality} disabled={busy} onChange={(v) => set("quality", v)} />
      </div>
      {reconnects(change) && <div className="notice small">The live stream will briefly reconnect while the camera applies this.</div>}
      {problem && <div className="field-problem">{problem}</div>}
      <ErrorBanner error={error} />
      <div className="ss-actions">
        {done && !change && <span className="muted small">Applied.</span>}
        <button type="button" className="btn primary small" disabled={busy || !change || !!problem} onClick={() => void apply()}>
          {busy ? "Applying…" : "Apply"}
        </button>
      </div>
    </section>
  );
}

/** A bounded whole-number setting: a slider where the range is tight, a plain input when the camera reported no range. */
function Num({ label, value, range, disabled, onChange }: { label: string; value: number; range: StreamRange; disabled: boolean; onChange: (v: number) => void }) {
  const read = (s: string) => (s === "" ? 0 : Math.round(Number(s)) || 0);
  return (
    <label className="field">
      <span>{label}</span>
      <div className="ss-num">
        {useSlider(range) && (
          <input type="range" min={range.min} max={range.max} value={Math.min(Math.max(value, range.min), range.max)} disabled={disabled} onChange={(e) => onChange(read(e.target.value))} aria-label={`${label} slider`} />
        )}
        <input
          type="number"
          min={bounded(range) ? range.min : 1}
          max={bounded(range) ? range.max : undefined}
          step={1}
          value={value || ""}
          disabled={disabled}
          onChange={(e) => onChange(read(e.target.value))}
        />
      </div>
      <small>{bounded(range) ? `${range.min} to ${range.max}` : "No limit reported by the camera"}</small>
    </label>
  );
}
