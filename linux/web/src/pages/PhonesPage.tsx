import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import qrcode from "qrcode-generator";
import { api, type PairedDevice, type PairingState, type RemoteStatus } from "../api";

const APP_STORE = "https://apps.apple.com/us/app/sentinel-vms/id6780667120";

export default function PhonesPage() {
  const [pairing, setPairing] = useState<PairingState | null>(null);
  const [devices, setDevices] = useState<PairedDevice[]>([]);
  const [remote, setRemote] = useState<RemoteStatus | null>(null);
  const [chosenURL, setChosenURL] = useState("");
  const [error, setError] = useState("");
  const [justPaired, setJustPaired] = useState("");
  const [busy, setBusy] = useState(false);
  const knownIDs = useRef<Set<string> | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [p, d, r] = await Promise.all([api.pairing(), api.devices(), api.remote()]);
      setPairing(p);
      setRemote(r);
      setDevices(d);
      if (knownIDs.current) {
        const fresh = d.find((x) => !knownIDs.current!.has(x.id));
        if (fresh) setJustPaired(fresh.name);
      }
      knownIDs.current = new Set(d.map((x) => x.id));
      setChosenURL((u) => u || p.urls?.[0] || "");
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Poll while a code is showing (to catch the phone pairing) or the tunnel is starting.
  const polling = !!pairing?.active || remote?.state === "starting" || remote?.state === "retrying";
  useEffect(() => {
    if (!polling) return;
    const t = setInterval(refresh, 2500);
    return () => clearInterval(t);
  }, [polling, refresh]);

  async function start() {
    setBusy(true);
    setError("");
    setJustPaired("");
    try {
      setPairing(await api.startPairing(chosenURL));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function cancel() {
    setPairing(await api.cancelPairing());
  }

  async function toggleRemote(on: boolean) {
    setBusy(true);
    setError("");
    try {
      setRemote(await api.setRemote(on));
      setTimeout(refresh, 1500);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function revoke(d: PairedDevice) {
    if (!confirm(`Revoke ${d.name}? It will be signed out and has to pair again.`)) return;
    try {
      await api.revokeDevice(d.id);
      refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const remoteReady = remote?.state === "connected" && !!remote.url;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>iPhone</h1>
          <div className="sub">
            Watch live cameras and recordings with the free{" "}
            <a href={APP_STORE} target="_blank" rel="noopener noreferrer">
              Sentinel VMS app
            </a>{" "}
            from the App Store.
          </div>
        </div>
      </div>
      {error && <div className="error">{error}</div>}
      {justPaired && <div className="notice success">✓ {justPaired} is paired. Open the app to see your cameras.</div>}

      <div className="phones-grid">
        <div className="card">
          <h2>Pair an iPhone</h2>
          {pairing?.active && pairing.payload ? (
            <ActivePairing pairing={pairing} remoteReady={remoteReady} onCancel={cancel} onExpired={refresh} />
          ) : (
            <>
              <ol className="steps">
                <li>
                  Install <strong>Sentinel VMS</strong> from the App Store on the iPhone.
                </li>
                <li>Click <strong>Pair iPhone</strong> below to show a one-time QR code.</li>
                <li>In the app, tap <strong>Scan Pairing QR</strong> and point it at this screen.</li>
              </ol>
              {(pairing?.urls?.length ?? 0) > 1 && (
                <label className="field">
                  <span>Address the iPhone uses on your network</span>
                  <select value={chosenURL} onChange={(e) => setChosenURL(e.target.value)}>
                    {pairing!.urls!.map((u) => (
                      <option key={u}>{u}</option>
                    ))}
                  </select>
                </label>
              )}
              {(pairing?.urls?.length ?? 0) === 0 && <div className="notice">This server has no network address the iPhone could reach.</div>}
              <button className="btn primary" onClick={start} disabled={busy || !chosenURL}>
                Pair iPhone
              </button>
            </>
          )}
        </div>

        <div className="card">
          <h2>Remote access</h2>
          <p className="muted">
            Lets paired iPhones reach this server on cellular or other Wi-Fi through a secure Cloudflare tunnel. No port forwarding or account needed.
            Only the iPhone API goes through the tunnel; this dashboard never does.
          </p>
          {!remote ? null : !remote.available ? (
            <div className="notice">
              Remote access needs <code>cloudflared</code>, which isn't installed on this server. iPhones still work on the same network.
            </div>
          ) : (
            <>
              <div className="remote-row">
                <RemoteBadge r={remote} />
                <span className="grow" />
                <button className={`btn ${remote.enabled ? "" : "primary"}`} onClick={() => toggleRemote(!remote.enabled)} disabled={busy}>
                  {remote.enabled ? "Turn off" : "Turn on"}
                </button>
              </div>
              {remote.url && <div className="mono remote-url">{remote.url}</div>}
              {remote.error && remote.state !== "connected" && <div className="mono">{remote.error}</div>}
              {remote.enabled && <p className="muted small">The address changes when the server restarts. Paired iPhones pick up the new one automatically the next time they're on your network.</p>}
            </>
          )}
        </div>
      </div>

      <div className="card table-wrap">
        <h2>Paired iPhones</h2>
        {devices.length === 0 ? (
          <div className="empty">No iPhones paired yet.</div>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Device</th>
                <th>Paired</th>
                <th>Last seen</th>
                <th>Alerts</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {devices.map((d) => (
                <tr key={d.id}>
                  <td>
                    <strong>{d.name}</strong>
                    {d.pairedBy && <div className="mono">by {d.pairedBy}</div>}
                  </td>
                  <td>{new Date(d.pairedAt * 1000).toLocaleString()}</td>
                  <td>{d.lastSeenAt ? ago(d.lastSeenAt) : "Never"}</td>
                  <td>{d.hasPush ? <span className="badge green">On</span> : <span className="badge blue">Not allowed</span>}</td>
                  <td style={{ textAlign: "right" }}>
                    <button className="btn small danger" onClick={() => revoke(d)}>
                      Revoke
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}

function ActivePairing({ pairing, remoteReady, onCancel, onExpired }: { pairing: PairingState; remoteReady: boolean; onCancel: () => void; onExpired: () => void }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const left = Math.max(0, Math.round((pairing.expiresAt ?? 0) - now / 1000));
  useEffect(() => {
    if (left === 0) onExpired();
  }, [left, onExpired]);

  return (
    <div className="pair-active">
      <QRCode text={pairing.payload!} />
      <div className="pair-info">
        <div className="muted small">Pairing code</div>
        <div className="pair-code">{pairing.code}</div>
        <div className="muted small">
          Expires in {Math.floor(left / 60)}:{String(left % 60).padStart(2, "0")} · works once
        </div>
        <div className="mono" style={{ marginTop: 10 }}>
          {pairing.url}
        </div>
        {!remoteReady && <div className="notice small">Remote access isn't connected, so pair while the iPhone is on this network.</div>}
        <p className="muted small">Can't scan? In the app tap "Enter Server Info Manually" and type the address and code above.</p>
        <button className="btn" onClick={onCancel}>
          Cancel
        </button>
      </div>
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
    <svg className="qr" viewBox={`0 0 ${size} ${size}`} role="img" aria-label="Pairing QR code" shapeRendering="crispEdges">
      <rect width={size} height={size} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}

function RemoteBadge({ r }: { r: RemoteStatus }) {
  if (r.state === "connected") return <span className="badge green"><span className="dot" />Connected</span>;
  if (r.state === "starting") return <span className="badge amber"><span className="dot" />Connecting…</span>;
  if (r.state === "retrying") return <span className="badge amber"><span className="dot" />Reconnecting…</span>;
  return <span className="badge blue">Off</span>;
}

function ago(unix: number): string {
  const s = Math.max(0, Date.now() / 1000 - unix);
  if (s < 90) return "Just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return new Date(unix * 1000).toLocaleDateString();
}
