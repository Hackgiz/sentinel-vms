import { useEffect, useMemo, useState } from "react";
import qrcode from "qrcode-generator";
import { api, ApiError, type Camera, type HomeKitCamera, type HomeKitStatus } from "../api";

/** System → Apple Home. Rendered for users who can change settings; hidden when the server has no bridge. */
export default function HomeKitCard() {
  const [st, setSt] = useState<HomeKitStatus | null>(null);
  const [cams, setCams] = useState<HomeKitCamera[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    api
      .homekit()
      .then((s) => live && setSt(s))
      .catch(() => {}); // 404 = not supported on this server: no card
    api.homekitCameras().then((c) => live && setCams(c)).catch(() => {});
    // Pick up pairing as soon as the Home app finishes.
    const t = setInterval(() => api.homekit().then((s) => live && setSt(s)).catch(() => {}), 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);

  async function run(fn: () => Promise<void>, ok = "") {
    setBusy(true);
    setError("");
    setMsg("");
    try {
      await fn();
      setMsg(ok);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const toggle = (enabled: boolean) => run(async () => setSt(await api.setHomeKit(enabled)), enabled ? "Apple Home is on." : "Apple Home is off.");

  function reset() {
    if (!confirm("Reset Apple Home? Every Apple device is disconnected and Sentinel has to be added to the Home app again with a new code.")) return;
    run(async () => setSt(await api.resetHomeKit()), "Pairings reset. Add Sentinel again with the new code.");
  }

  // Updating a camera takes the whole record; the secrets stay on the server.
  function setCamera(c: HomeKitCamera, homekit: boolean) {
    run(async () => {
      const full: Camera | undefined = (await api.cameras()).find((x) => x.id === c.id);
      if (!full) throw new ApiError("Camera not found.", 404);
      await api.updateCamera(c.id, {
        name: full.name,
        location: full.location,
        rtspURL: full.rtspURL,
        subRTSPURL: full.subRTSPURL,
        username: full.username,
        recording: full.recording,
        retentionDays: full.retentionDays,
        homekit,
      });
      setCams((list) => list.map((x) => (x.id === c.id ? { ...x, homekit } : x)));
      setSt(await api.homekit());
    });
  }

  if (!st) return null;
  return (
    <div className="card">
      <h2>Apple Home</h2>
      <p className="muted small">
        Shows your cameras in the Apple Home app with live view and motion sensors. Works on iPhone, iPad, Apple TV and HomeView. Your Apple device must be on the same network as this server.
      </p>
      {msg && <div className="notice">{msg}</div>}
      {error && <div className="error">{error}</div>}
      {st.err && <div className="error">{st.err}</div>}
      <label className="check">
        <input type="checkbox" checked={st.enabled} disabled={busy} onChange={(e) => toggle(e.target.checked)} />
        Enabled
      </label>
      {st.enabled && (
        <>
          <div className="stat">
            <span>Home app</span>
            {st.paired ? <span className="badge green">Paired</span> : <span className="badge amber">Waiting to be added</span>}
          </div>
          {!st.paired && st.setupURI && (
            <div className="pair-active">
              <QRCode text={st.setupURI} />
              <div className="pair-info">
                <div className="muted small">Setup code</div>
                <div className="pair-code">{st.setupCode}</div>
                <div className="muted small">In the Home app tap + → Add Accessory, then scan this code or type the setup code.</div>
              </div>
            </div>
          )}
          {cams.length > 0 && (
            <>
              <h3>Cameras in Apple Home</h3>
              {cams.map((c) => (
                <label className="check" key={c.id}>
                  <input type="checkbox" checked={c.homekit} disabled={busy} onChange={(e) => setCamera(c, e.target.checked)} />
                  {c.name}
                </label>
              ))}
            </>
          )}
          <div className="alarm-actions" style={{ marginTop: 8 }}>
            <button className="btn danger" disabled={busy} onClick={reset}>
              Reset pairings
            </button>
          </div>
          <p className="muted small">Resetting issues a new code and requires adding Sentinel to the Home app again.</p>
        </>
      )}
    </div>
  );
}

function QRCode({ text }: { text: string }) {
  const { size, path } = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(text);
    qr.make();
    const n = qr.getModuleCount();
    let d = "";
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (qr.isDark(r, c)) d += `M${c + 4} ${r + 4}h1v1h-1z`;
    return { size: n + 8, path: d };
  }, [text]);
  return (
    <svg className="qr" viewBox={`0 0 ${size} ${size}`} role="img" aria-label="Apple Home setup QR code" shapeRendering="crispEdges">
      <rect width={size} height={size} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
