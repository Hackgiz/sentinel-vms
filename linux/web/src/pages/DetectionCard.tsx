// System → Detection (wave 16): which detector model runs and how often,
// what is actually running, and the Models table (download, cancel, delete)
// for every optional model. Arithmetic and labels are in detectionModel.ts.
// The Operator's System Monitor hosts it too (WAVE17) with its bearer client
// and permission check.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { can, formatBytes, type User } from "../api";
import { detectionApi, type DetectorSettings, type Model } from "../detectionApi";
import { errorMessage, type ApiClient } from "../shared/api";
import {
  anyDownloading,
  detectorChoices,
  downloadPercent,
  modelActions,
  pendingNote,
  purposeOf,
  rateOptions,
  savedModel,
  sortModels,
  stateLabel,
  stateTone,
  statusLine,
  type ModelAction,
} from "./detectionModel";

/**
 * The Admin passes `user`; a host page passes `client` and `canChange`
 * instead (the controls show only with ChangeSettings).
 */
export type DetectionCardProps = { user: User; client?: undefined; canChange?: undefined } | { user?: undefined; client: ApiClient; canChange: boolean };

/** Whether the card shows its controls. Pure, for the tests. */
export const detectionControls = (p: DetectionCardProps): boolean => (p.user ? can.changeSettings(p.user) : p.canChange);

export default function DetectionCard(props: DetectionCardProps) {
  const admin = detectionControls(props);
  const api = useMemo(() => detectionApi(props.client), [props.client]);
  const [settings, setSettings] = useState<DetectorSettings | null>(null);
  const [models, setModels] = useState<Model[] | null>(null);
  const [dir, setDir] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  const loaded = useRef(false);

  const load = useCallback(async () => {
    try {
      const [d, m] = await Promise.all([api.detector.get(), api.models.list()]);
      setSettings(d);
      setModels(m.items);
      setDir(m.dir);
      loaded.current = true;
    } catch (e) {
      // Before the first answer (a server without these endpoints) the card stays hidden.
      if (loaded.current) setError(errorMessage(e));
    }
  }, [api]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll quickly while a download runs, slowly otherwise.
  const downloading = models ? anyDownloading(models) : false;
  useEffect(() => {
    const t = setInterval(() => !document.hidden && void load(), downloading ? 1500 : 15000);
    return () => clearInterval(t);
  }, [downloading, load]);

  const sorted = useMemo(() => sortModels(models ?? []), [models]);

  async function save(patch: { model?: string; rate?: number }, ok: string) {
    setBusy("settings");
    setError("");
    setSaved("");
    try {
      setSettings(await api.detector.put(patch));
      setSaved(ok);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }

  async function act(m: Model, a: ModelAction) {
    if (a === "delete" && !window.confirm(`Delete ${m.title}? Features that use it stop after the next restart.`)) return;
    setBusy(m.id);
    setError("");
    setSaved("");
    try {
      if (a === "download") await api.models.download(m.id);
      else if (a === "cancel") await api.models.cancel(m.id);
      else await api.models.remove(m.id);
      await load();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy("");
    }
  }

  if (!settings || !models) return null;
  const selected = savedModel(settings);
  const choices = detectorChoices(models, selected);
  const note = pendingNote(settings);
  const actionLabel: Record<ModelAction, string> = { download: "Download", cancel: "Cancel", delete: "Delete" };

  return (
    <>
      <div className="card">
        <h2>Detection</h2>
        <div className="stat">
          <span>Running</span>
          <span className="small">{statusLine(settings, models)}</span>
        </div>
        {note && <div className="notice" style={{ marginTop: 10 }}>{note}</div>}
        {saved && <div className="notice success" style={{ marginTop: 10 }}>{saved}</div>}
        {error && <div className="error" style={{ marginTop: 10 }}>{error}</div>}
        <label className="field" style={{ marginTop: 12 }}>
          <span>Detector model</span>
          <select value={selected} disabled={!admin || busy !== ""} onChange={(e) => void save({ model: e.target.value }, "Model saved. Restart Sentinel to switch.")}>
            {choices.map((c) => (
              <option key={c.id} value={c.id} disabled={!c.installed}>
                {c.title}
                {c.installed ? "" : " (not installed)"}
              </option>
            ))}
          </select>
          <small>{choices.find((c) => c.id === selected)?.note || "Bigger models find smaller and farther objects but need more time per frame. Download more in the Models table."}</small>
        </label>
        <label className="field">
          <span>Detection rate while there is motion</span>
          <select value={settings.rate} disabled={!admin || busy !== ""} onChange={(e) => void save({ rate: Number(e.target.value) }, "Rate saved. Cameras use it now.")}>
            {rateOptions.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
          <small>Passes per second per camera. More passes track moving objects better and use more CPU.</small>
        </label>
      </div>

      <div className="card det-models">
        <h2>Models</h2>
        <p className="muted small">
          Optional models behind detection, appearance search, audio classes, faces, plates and semantic search. Downloads come from the source each model lists and are checked against a pinned SHA-256.
        </p>
        <div className="table-wrap">
          <table className="list">
            <thead>
              <tr>
                <th>Model</th>
                <th>Used for</th>
                <th>Size</th>
                <th>License</th>
                <th>State</th>
                {admin && (
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {sorted.map((m) => {
                const pct = downloadPercent(m);
                const actions = modelActions(m, selected, admin);
                return (
                  <tr key={m.id}>
                    <td>
                      <strong>{m.title}</strong>
                      {m.note && <div className="muted small">{m.note}</div>}
                    </td>
                    <td>{purposeOf(m.purpose)}</td>
                    <td className="mono nowrap">{formatBytes(m.sizeBytes)}</td>
                    <td className="small">{m.source ? <a href={m.source} target="_blank" rel="noopener noreferrer">{m.license}</a> : m.license}</td>
                    <td style={{ minWidth: 120 }}>
                      <span className={`badge ${stateTone[m.state] ?? ""}`} title={m.download?.error || undefined}>
                        {stateLabel(m)}
                      </span>
                      {m.state === "downloading" &&
                        (pct === null ? (
                          <div className="muted small">Starting…</div>
                        ) : (
                          <div className="meter det-meter" role="progressbar" aria-label={`${m.title} download`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
                            <div style={{ width: `${pct}%` }} />
                          </div>
                        ))}
                      {m.download?.state === "failed" && m.download.error && <div className="error small">{m.download.error}</div>}
                    </td>
                    {admin && (
                      <td className="row-actions">
                        {actions.map((a) => (
                          <button key={a} type="button" className={`btn small ${a === "delete" ? "danger" : ""}`} disabled={busy !== ""} onClick={() => void act(m, a)}>
                            {busy === m.id ? "Working…" : actionLabel[a]}
                          </button>
                        ))}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {dir && (
          <p className="muted small" style={{ marginTop: 10 }}>
            To install by hand, put the model file in <span className="mono">{dir}</span> on the management server. It counts once its SHA-256 matches.
          </p>
        )}
      </div>
    </>
  );
}
