import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, ApiError, type CameraInput, type DiscoveredDevice, type OnvifProfile } from "../api";

/** What the scan hands to the camera editor once a camera is chosen. */
export interface ScanPick {
  initial: CameraInput;
  profiles: OnvifProfile[];
  recommendedSub?: string;
}

type Target = { host: string; serviceURL: string; name: string; manual: boolean };

export default function ScanDialog({ onClose, onPick }: { onClose: () => void; onPick: (p: ScanPick) => void }) {
  const [devices, setDevices] = useState<DiscoveredDevice[]>([]);
  const [warnings, setWarnings] = useState<string[]>([]);
  const [subnets, setSubnets] = useState<string[]>([]);
  const [scanning, setScanning] = useState<"" | "quick" | "deep">("");
  const [scanned, setScanned] = useState(false);
  const [error, setError] = useState("");
  const [manualHost, setManualHost] = useState("");
  const [target, setTarget] = useState<Target | null>(null);

  async function scan(sweep: boolean) {
    setScanning(sweep ? "deep" : "quick");
    setError("");
    try {
      const r = await api.discover(sweep);
      setDevices(r.devices);
      setWarnings(r.warnings ?? []);
      setSubnets(r.subnets ?? []);
      setScanned(true);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setScanning("");
    }
  }

  const started = useRef(false);
  useEffect(() => {
    if (started.current) return; // StrictMode runs effects twice in dev
    started.current = true;
    scan(false);
  }, []);

  if (target) {
    return <ConnectStep target={target} onBack={() => setTarget(null)} onClose={onClose} onPick={onPick} />;
  }

  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && !scanning && onClose()}>
      <div className="modal wide">
        <h2>Scan network for cameras</h2>
        {error && <div className="error">{error}</div>}

        {scanning ? (
          <div className="scan-status">
            <span className="spinner" />
            {scanning === "quick" ? "Asking ONVIF cameras on the network to identify themselves…" : "Checking every address on the local network — this takes up to a minute…"}
          </div>
        ) : scanned && devices.length === 0 ? (
          <div className="empty">
            <div className="big">⌕</div>
            No cameras answered. Try <strong>Scan whole subnet</strong>, or enter the camera's IP address below.
          </div>
        ) : null}

        {devices.length > 0 && (
          <div className="scan-list">
            {devices.map((d) => (
              <div className="scan-row" key={d.host}>
                <div className="grow">
                  <strong>{d.name}</strong>
                  <div className="mono">
                    {d.host}
                    {(d.manufacturer || d.model) && ` · ${[d.manufacturer, d.model].filter(Boolean).join(" ")}`}
                    {d.openPorts && d.openPorts.length > 0 && ` · ports ${d.openPorts.join(", ")}`}
                  </div>
                </div>
                <span className={`badge ${d.source === "onvif" ? "blue" : "amber"}`}>{d.source === "onvif" ? "ONVIF" : "Port scan"}</span>
                {d.added ? (
                  <span className="badge green">Added</span>
                ) : (
                  <button className="btn small primary" onClick={() => setTarget({ host: d.host, serviceURL: d.serviceURL, name: d.source === "onvif" ? d.name : "", manual: false })}>
                    Add
                  </button>
                )}
              </div>
            ))}
          </div>
        )}

        {warnings.length > 0 && !scanning && (
          <div className="notice">
            {warnings.map((w) => (
              <div key={w}>{w}</div>
            ))}
          </div>
        )}
        {subnets.length > 0 && !scanning && <p className="mono">Swept {subnets.join(", ")}</p>}

        <form
          className="scan-manual"
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            const h = manualHost.trim();
            if (h) setTarget({ host: h, serviceURL: "", name: "", manual: true });
          }}
        >
          <label className="field grow">
            <span>Camera not listed? Enter its IP address</span>
            <input type="text" value={manualHost} onChange={(e) => setManualHost(e.target.value)} placeholder="192.168.1.64" />
          </label>
          <button className="btn" disabled={!manualHost.trim()}>
            Connect
          </button>
        </form>

        <div className="actions">
          <button type="button" className="btn" onClick={() => scan(false)} disabled={!!scanning}>
            Scan again
          </button>
          <button type="button" className="btn" onClick={() => scan(true)} disabled={!!scanning} title="TCP-checks every address on this machine's subnet for an RTSP port. Finds cameras that don't answer ONVIF discovery.">
            Scan whole subnet
          </button>
          <span className="grow" />
          <button type="button" className="btn" onClick={onClose} disabled={!!scanning}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}

function ConnectStep({ target, onBack, onClose, onPick }: { target: Target; onBack: () => void; onClose: () => void; onPick: (p: ScanPick) => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [fallbackURL, setFallbackURL] = useState("");

  async function connect(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    setFallbackURL("");
    try {
      const r = await api.probeCamera(target.host, target.serviceURL, username, password);
      const d = r.details;
      const name = target.name && target.name !== "ONVIF camera" ? target.name : [d.manufacturer, d.model].filter(Boolean).join(" ") || `Camera ${target.host}`;
      const mainURL = r.mainURL ?? d.profiles[0]?.rtspURL ?? "";
      const fps = d.profiles.find((p) => p.rtspURL === mainURL)?.fps || undefined;
      onPick({
        initial: { name, location: "", rtspURL: mainURL, subRTSPURL: "", username, password, recording: true, retentionDays: 0, fps },
        profiles: d.profiles,
        recommendedSub: r.subURL,
      });
    } catch (err) {
      const data = err instanceof ApiError ? err.data : null;
      if (data?.authFailed) setError("The camera rejected that username or password.");
      else {
        setError((err as Error).message);
        if (typeof data?.suggestedURL === "string") setFallbackURL(data.suggestedURL);
      }
      setBusy(false);
    }
  }

  function manual() {
    onPick({
      initial: { name: target.name || `Camera ${target.host}`, location: "", rtspURL: fallbackURL || `rtsp://${target.host}:554/`, subRTSPURL: "", username, password, recording: true, retentionDays: 0 },
      profiles: [],
    });
  }

  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && !busy && onClose()}>
      <form className="modal" onSubmit={connect}>
        <h2>Connect to {target.name || target.host}</h2>
        <p className="mono" style={{ marginTop: -8 }}>
          {target.host} — enter the camera's own login. Sentinel uses it to read the stream addresses over ONVIF.
        </p>
        {error && <div className="error">{error}</div>}
        <div className="grid-2">
          <label className="field">
            <span>Username</span>
            <input type="text" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" autoFocus />
          </label>
          <label className="field">
            <span>Password</span>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
          </label>
        </div>
        <small className="hint">TP-Link Tapo: use the camera account set under Advanced Settings → Camera Account, not your TP-Link cloud login.</small>
        <div className="actions">
          <button type="button" className="btn" onClick={onBack} disabled={busy}>
            Back
          </button>
          {(fallbackURL || error) && (
            <button type="button" className="btn" onClick={manual} disabled={busy}>
              Enter stream address manually
            </button>
          )}
          <span className="grow" />
          <button className="btn primary" disabled={busy}>
            {busy ? "Connecting…" : "Connect"}
          </button>
        </div>
      </form>
    </div>
  );
}

export function profileLabel(p: OnvifProfile): string {
  const parts = [p.name];
  if (p.width && p.height) parts.push(`${p.width}×${p.height}`);
  if (p.encoding) parts.push(p.encoding);
  if (p.fps) parts.push(`${p.fps} fps`);
  return parts.join(" · ");
}
