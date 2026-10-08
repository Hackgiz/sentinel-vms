import { useEffect, useState, type FormEvent } from "react";
import { api, type Camera, type CameraInput, type OnvifProfile } from "../api";
import ScanDialog, { profileLabel, type ScanPick } from "./ScanDialog";

const blank: CameraInput = { name: "", location: "", rtspURL: "", subRTSPURL: "", username: "", password: "", recording: true, retentionDays: 0 };

export default function CamerasPage() {
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [editing, setEditing] = useState<Camera | "new" | ScanPick | null>(null);
  const [scanOpen, setScanOpen] = useState(false);
  const [error, setError] = useState("");

  const load = () => api.cameras().then(setCameras).catch((e) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Cameras</h1>
          <div className="sub">{cameras.length} configured</div>
        </div>
        <button className="btn" onClick={() => setScanOpen(true)}>
          Scan network
        </button>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + Add camera
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      <div className="card table-wrap">
        {cameras.length === 0 ? (
          <div className="empty">
            <div className="big">◉</div>
            Use <strong>Scan network</strong> to find ONVIF cameras, or add one with its RTSP address (e.g. rtsp://192.168.1.20:554/stream1).
          </div>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Stream</th>
                <th>Recording</th>
                <th>Keep</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {cameras.map((c) => (
                <tr key={c.id}>
                  <td>
                    <strong>{c.name}</strong>
                    {c.location && <div className="mono">{c.location}</div>}
                  </td>
                  <td>
                    <span className={`badge ${c.online ? "green" : "red"}`}>
                      <span className="dot" />
                      {c.status}
                    </span>
                  </td>
                  <td className="mono">
                    {c.rtspURL}
                    {c.subRTSPURL && <div>sub: {c.subRTSPURL}</div>}
                  </td>
                  <td>{c.recording ? <span className="badge red">24/7</span> : <span className="badge blue">Live only</span>}</td>
                  <td>{c.retentionDays > 0 ? `${c.retentionDays} day${c.retentionDays === 1 ? "" : "s"}` : "Default"}</td>
                  <td style={{ textAlign: "right" }}>
                    <button className="btn small" onClick={() => setEditing(c)}>
                      Edit
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {scanOpen && (
        <ScanDialog
          onClose={() => setScanOpen(false)}
          onPick={(p) => {
            setScanOpen(false);
            setEditing(p);
          }}
        />
      )}
      {editing && (
        <CameraEditor
          camera={editing === "new" || "initial" in editing ? null : editing}
          pick={editing !== "new" && "initial" in editing ? editing : undefined}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            load();
          }}
        />
      )}
    </>
  );
}

function CameraEditor({ camera, pick, onClose, onSaved }: { camera: Camera | null; pick?: ScanPick; onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState<CameraInput>(
    camera
      ? { name: camera.name, location: camera.location, rtspURL: camera.rtspURL, subRTSPURL: camera.subRTSPURL, username: camera.username, recording: camera.recording, retentionDays: camera.retentionDays, motionLevel: camera.motionLevel, alertOn: camera.alertOn }
      : (pick?.initial ?? blank),
  );
  const profiles: OnvifProfile[] = pick?.profiles ?? [];
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const set = <K extends keyof CameraInput>(k: K, v: CameraInput[K]) => setForm((f) => ({ ...f, [k]: v }));

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (camera) await api.updateCamera(camera.id, form);
      else await api.addCamera(form);
      onSaved();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  async function remove() {
    if (!camera) return;
    setBusy(true);
    try {
      await api.deleteCamera(camera.id);
      onSaved();
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  }

  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <form className="modal" onSubmit={save}>
        <h2>{camera ? `Edit ${camera.name}` : pick ? "Add discovered camera" : "Add camera"}</h2>
        {error && <div className="error">{error}</div>}
        <label className="field">
          <span>Name</span>
          <input type="text" value={form.name} onChange={(e) => set("name", e.target.value)} required autoFocus />
        </label>
        <label className="field">
          <span>Location</span>
          <input type="text" value={form.location} onChange={(e) => set("location", e.target.value)} placeholder="Front door, Warehouse…" />
        </label>
        {profiles.length > 0 && (
          <div className="notice">
            {profiles.length === 1
              ? "Found the camera's stream and filled it in below."
              : `Found ${profiles.length} streams on this camera. Pick which to record and which to watch live.`}
          </div>
        )}
        {profiles.length > 0 && (
          <label className="field">
            <span>Record this stream</span>
            <select
              value={form.rtspURL}
              onChange={(e) => {
                const url = e.target.value;
                setForm((f) => ({ ...f, rtspURL: url, fps: profiles.find((p) => p.rtspURL === url)?.fps || f.fps }));
              }}
            >
              {!profiles.some((p) => p.rtspURL === form.rtspURL) && <option value={form.rtspURL}>Custom address</option>}
              {profiles.map((p) => (
                <option key={p.token} value={p.rtspURL}>
                  {profileLabel(p)}
                </option>
              ))}
            </select>
          </label>
        )}
        {profiles.length > 1 && (
          <label className="field">
            <span>Live view stream</span>
            <select value={form.subRTSPURL} onChange={(e) => set("subRTSPURL", e.target.value)}>
              <option value="">Same as recording</option>
              {profiles
                .filter((p) => p.rtspURL !== form.rtspURL)
                .map((p) => (
                  <option key={p.token} value={p.rtspURL}>
                    {profileLabel(p)}
                    {p.rtspURL === pick?.recommendedSub ? " (recommended)" : ""}
                  </option>
                ))}
            </select>
            <small>A low-res stream for live view saves CPU, but opens a second connection. Budget cameras like Tapo allow only two, so leave this on "Same as recording" if recording drops out.</small>
          </label>
        )}
        <label className="field">
          <span>Main stream (RTSP)</span>
          <input type="text" value={form.rtspURL} onChange={(e) => set("rtspURL", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream1" required />
          <small>Recorded at full quality.</small>
        </label>
        <label className="field">
          <span>Sub-stream (optional)</span>
          <input type="text" value={form.subRTSPURL} onChange={(e) => set("subRTSPURL", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream2" />
          <small>Low-resolution stream used for live viewing — saves CPU and bandwidth.</small>
        </label>
        <div className="grid-2">
          <label className="field">
            <span>Username</span>
            <input type="text" value={form.username} onChange={(e) => set("username", e.target.value)} autoComplete="off" />
          </label>
          <label className="field">
            <span>Password</span>
            <input
              type="password"
              value={form.password ?? ""}
              onChange={(e) => set("password", e.target.value)}
              placeholder={camera?.hasPassword ? "•••••• (unchanged)" : ""}
              autoComplete="new-password"
            />
          </label>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.recording} onChange={(e) => set("recording", e.target.checked)} />
          Record 24/7
        </label>
        <label className="field">
          <span>Motion detection</span>
          <select value={form.motionLevel ?? 2} onChange={(e) => set("motionLevel", Number(e.target.value))}>
            <option value={0}>Off</option>
            <option value={1}>Low sensitivity (large movement only)</option>
            <option value={2}>Medium sensitivity</option>
            <option value={3}>High sensitivity (small or distant movement)</option>
          </select>
          <small>Finds movement; people and vehicles are then detected on frames with motion.</small>
        </label>
        <label className="field">
          <span>Raise alarms for</span>
          <select value={form.alertOn ?? 0} onChange={(e) => set("alertOn", Number(e.target.value))} disabled={(form.motionLevel ?? 2) === 0}>
            <option value={0}>Automatic (people & vehicles when detection is installed)</option>
            <option value={2}>People & vehicles</option>
            <option value={3}>People only</option>
            <option value={1}>Any motion</option>
          </select>
          <small>Everything still appears under Events; this only decides what becomes an alarm.</small>
        </label>
        <label className="field">
          <span>Keep recordings</span>
          <select value={form.retentionDays} onChange={(e) => set("retentionDays", Number(e.target.value))}>
            <option value={0}>Server default</option>
            {[1, 3, 7, 14, 30, 90].map((d) => (
              <option key={d} value={d}>
                {d} day{d === 1 ? "" : "s"}
              </option>
            ))}
          </select>
        </label>
        <div className="actions">
          {camera &&
            (confirmDelete ? (
              <button type="button" className="btn danger" onClick={remove} disabled={busy}>
                Confirm delete
              </button>
            ) : (
              <button type="button" className="btn danger" onClick={() => setConfirmDelete(true)}>
                Delete…
              </button>
            ))}
          <span className="grow" />
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy}>
            {camera ? "Save" : "Add camera"}
          </button>
        </div>
        {confirmDelete && <p className="mono" style={{ marginBottom: 0 }}>Deleting keeps existing recordings on disk.</p>}
      </form>
    </div>
  );
}
