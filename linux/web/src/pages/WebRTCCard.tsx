import { useEffect, useState } from "react";
import { can, type User } from "../api";
import { webrtcApi, type WebRTCSettings } from "../shared/webrtc";

/** System page: WebRTC live-view status, the port to open, extra addresses. */
export default function WebRTCCard({ user }: { user: User }) {
  const [st, setSt] = useState<WebRTCSettings | null>(null);
  const [hosts, setHosts] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    webrtcApi
      .get()
      .then((s) => {
        setSt(s);
        setHosts(s.additionalHosts.join(", "));
      })
      .catch((e) => setError(e.message));
  }, []);

  async function save() {
    setBusy(true);
    setMsg("");
    setError("");
    try {
      const s = await webrtcApi.setHosts(hosts.split(/[\s,]+/).filter(Boolean));
      setSt(s);
      setHosts(s.additionalHosts.join(", "));
      setMsg("Saved.");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return error ? <div className="card"><h2>Live view</h2><div className="error">{error}</div></div> : null;
  return (
    <div className="card">
      <h2>Live view</h2>
      <div className="stat">
        <span>WebRTC (low latency)</span>
        <span className={`badge ${st.enabled ? "green" : "blue"}`}>{st.enabled ? "On" : "Off — HLS only"}</span>
      </div>
      {st.enabled && (
        <>
          <div className="stat">
            <span>Media port (open in the firewall)</span>
            <span className="mono">
              {st.port}/udp + {st.port}/tcp
            </span>
          </div>
          {st.configHosts.length > 0 && (
            <div className="stat">
              <span>Addresses from server config</span>
              <span className="mono">{st.configHosts.join(", ")}</span>
            </div>
          )}
          <p className="muted small" style={{ marginTop: 10 }}>
            Browsers fall back to HLS (a few seconds behind) when this port can't be reached. Video never leaves your network: no STUN or relay
            servers are used.
          </p>
          {msg && <div className="notice">{msg}</div>}
          {error && <div className="error">{error}</div>}
          {can.changeSettings(user) && (
            <>
              <label className="field">
                <span>Extra server addresses</span>
                <input value={hosts} onChange={(e) => setHosts(e.target.value)} placeholder="e.g. 192.168.1.20, nvr.home.lan" />
                <small>Only needed when browsers reach this server at an address it can't see itself (Docker, port forwarding, VPN).</small>
              </label>
              <button className="btn primary" disabled={busy} onClick={save}>
                Save
              </button>
            </>
          )}
        </>
      )}
    </div>
  );
}
