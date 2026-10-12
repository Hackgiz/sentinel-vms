import { useEffect, useState } from "react";
import { api, can, type HWAccelStatus, type User } from "../api";

const methodLabel: Record<string, string> = { vaapi: "VAAPI (GPU)", qsv: "Quick Sync (GPU)", videotoolbox: "VideoToolbox (GPU)", software: "CPU" };

/** System page card: hardware decode + detection acceleration, and the Auto/Off setting. */
export default function HardwareCard({ user }: { user: User }) {
  const [st, setSt] = useState<HWAccelStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const admin = can.changeSettings(user);

  useEffect(() => {
    const load = () => api.hwaccel().then(setSt).catch(() => {});
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, []);

  async function setMode(mode: "auto" | "off") {
    setBusy(true);
    setError("");
    try {
      setSt(await api.setHWAccel(mode));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return null;
  const dec = st.decode;
  const gpu = dec.usable.length > 0;
  const det = st.detection;
  return (
    <div className="card">
      <h2>Hardware acceleration</h2>
      <div className="stat">
        <span>Video decode for detection</span>
        <span className={`badge ${st.mode === "off" ? "blue" : gpu ? "green" : "blue"}`}>
          {st.mode === "off" ? "Off" : gpu ? dec.usable.map((m) => methodLabel[m] ?? m).join(", ") : "CPU only"}
        </span>
      </div>
      {gpu && (
        <div className="stat">
          <span>GPU</span>
          <span className="mono small">
            {dec.device}
            {dec.driver ? ` · ${dec.driver}` : ""}
          </span>
        </div>
      )}
      <div className="stat">
        <span>Person & vehicle detection</span>
        <span className={`badge ${det && det.engine !== "CPU" ? "green" : "blue"}`}>
          {det ? `${det.engine}${det.device ? ` (${det.device})` : ""}` : "Not installed"}
        </span>
      </div>
      {det && det.inferences > 0 && (
        <div className="stat">
          <span>Detection time per frame</span>
          <span>
            {det.avgMs.toFixed(1)} ms avg · {det.inferences} frames
          </span>
        </div>
      )}
      {st.cameras.length > 0 && (
        <>
          <p className="muted small" style={{ marginTop: 10, marginBottom: 4 }}>
            Decode per camera
          </p>
          {st.cameras.map((c) => (
            <div className="stat" key={c.cameraId} title={c.reason || undefined}>
              <span>{c.name}</span>
              <span className={`badge ${c.method === "software" ? "blue" : "green"}`}>{methodLabel[c.method] ?? c.method}</span>
            </div>
          ))}
        </>
      )}
      {(dec.notes.length > 0 || det?.note) && (
        <ul className="muted small" style={{ marginTop: 10, paddingLeft: 18 }}>
          {dec.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
          {det?.note && <li>Detection: {det.note}</li>}
        </ul>
      )}
      {st.accelerators.map((a) => (
        <div className="stat" key={a.path} title={a.note}>
          <span>{a.name}</span>
          <span className={`badge ${a.supported ? "green" : "amber"}`}>{a.supported ? "Via OpenVINO" : "Not supported yet"}</span>
        </div>
      ))}
      {st.restartRequired && <div className="notice">Restart Sentinel to apply the change to detection (decode has already switched).</div>}
      {error && <div className="error">{error}</div>}
      {admin && (
        <label className="field" style={{ marginTop: 12 }}>
          <span>Hardware acceleration</span>
          <select value={st.mode} disabled={busy} onChange={(e) => setMode(e.target.value as "auto" | "off")}>
            <option value="auto">Auto (use the GPU when it works)</option>
            <option value="off">Off (CPU only)</option>
          </select>
          <small>
            {dec.hardware.os === "darwin"
              ? "Auto decodes with VideoToolbox and detects with Core ML, falling back to the CPU if either fails."
              : "Auto falls back to the CPU per camera if the GPU fails. See the README for VAAPI drivers and the render group."}
          </small>
        </label>
      )}
    </div>
  );
}
