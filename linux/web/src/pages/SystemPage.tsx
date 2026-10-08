import { useEffect, useState } from "react";
import { api, can, formatBytes, type AIStatus, type SystemInfo, type User } from "../api";

export default function SystemPage({ user }: { user: User }) {
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [retention, setRetention] = useState(7);
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    const load = () => api.system().then(setInfo).catch((e) => setError(e.message));
    load();
    api.settings().then((s) => setRetention(s.defaultRetentionDays)).catch(() => {});
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, []);

  async function saveRetention() {
    setError("");
    setSaved("");
    try {
      const s = await api.saveSettings(retention);
      setRetention(s.defaultRetentionDays);
      setSaved("Saved — applies to cameras using the server default.");
    } catch (err) {
      setError((err as Error).message);
    }
  }

  const used = info?.diskTotalBytes && info.diskFreeBytes !== undefined ? 1 - info.diskFreeBytes / info.diskTotalBytes : 0;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>System</h1>
          <div className="sub">Server health and storage</div>
        </div>
      </div>
      {error && <div className="error">{error}</div>}
      {info && (
        <div className="grid-2">
          <div className="card">
            <h2>Media engine</h2>
            <div className="stat">
              <span>MediaMTX</span>
              <span className={`badge ${info.mediaRunning ? "green" : "red"}`}>
                <span className="dot" />
                {info.mediaRunning ? "Running" : "Stopped"}
              </span>
            </div>
            {info.mediaError && <div className="error" style={{ marginTop: 10 }}>{info.mediaError}</div>}
            <div className="stat">
              <span>Version</span>
              <span>{info.version}</span>
            </div>
            <div className="stat">
              <span>Platform</span>
              <span>{info.os}</span>
            </div>
          </div>
          <div className="card">
            <h2>Storage</h2>
            <div className="meter" style={{ margin: "4px 0 12px" }}>
              <div style={{ width: `${Math.round(used * 100)}%`, background: used > 0.9 ? "var(--red)" : used > 0.8 ? "var(--amber)" : undefined }} />
            </div>
            <div className="stat">
              <span>Free</span>
              <span>{info.diskFreeBytes !== undefined ? formatBytes(info.diskFreeBytes) : "—"}</span>
            </div>
            <div className="stat">
              <span>Recordings</span>
              <span>{formatBytes(info.recordingsBytes)}</span>
            </div>
            <div className="stat">
              <span>Location</span>
              <span className="mono">{info.recordingsDir}</span>
            </div>
          </div>
          <AICard user={user} />
          <div className="card">
            <h2>Retention</h2>
            {saved && <div className="notice">{saved}</div>}
            <label className="field">
              <span>Keep recordings for (server default)</span>
              <select value={retention} onChange={(e) => setRetention(Number(e.target.value))} disabled={!can.changeSettings(user)}>
                {[1, 3, 7, 14, 30, 60, 90].map((d) => (
                  <option key={d} value={d}>
                    {d} day{d === 1 ? "" : "s"}
                  </option>
                ))}
              </select>
            </label>
            {can.changeSettings(user) && (
              <button className="btn primary" onClick={saveRetention}>
                Save
              </button>
            )}
          </div>
        </div>
      )}
    </>
  );
}

function AICard({ user }: { user: User }) {
  const [st, setSt] = useState<AIStatus | null>(null);
  const [key, setKey] = useState("");
  const [limit, setLimit] = useState(300);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const admin = can.changeSettings(user);

  useEffect(() => {
    api
      .ai()
      .then((a) => {
        setSt(a);
        setLimit(a.dailyLimit);
      })
      .catch(() => {});
  }, []);

  async function save(patch: { enabled?: boolean; apiKey?: string; dailyLimit?: number }, ok: string) {
    setBusy(true);
    setError("");
    setMsg("");
    try {
      const a = await api.saveAI(patch);
      setSt(a);
      setKey("");
      setMsg(ok);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return null;
  return (
    <div className="card">
      <h2>AI</h2>
      <div className="stat">
        <span>Person & vehicle detection</span>
        <span className={`badge ${st.detector ? "green" : "blue"}`}>{st.detector ? "On this server" : "Not installed"}</span>
      </div>
      <div className="stat">
        <span>Claude descriptions & search</span>
        <span className={`badge ${st.enabled && st.hasKey ? "green" : "blue"}`}>{st.enabled && st.hasKey ? "On" : st.hasKey ? "Off" : "No key"}</span>
      </div>
      {st.enabled && (
        <div className="stat">
          <span>Descriptions today</span>
          <span>
            {st.usedToday} of {st.dailyLimit}
          </span>
        </div>
      )}
      <p className="muted small" style={{ marginTop: 10 }}>
        Uses your own Anthropic API key; snapshots go straight from this server to Anthropic, and you pay Anthropic directly. Descriptions use {st.visionModel}; search and digests use {st.searchModel}.
      </p>
      {st.lastError && <div className="error">{st.lastError}</div>}
      {msg && <div className="notice">{msg}</div>}
      {error && <div className="error">{error}</div>}
      {admin && (
        <>
          <label className="field">
            <span>Anthropic API key</span>
            <input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder={st.hasKey ? "•••••••• (saved)" : "sk-ant-…"} autoComplete="off" />
          </label>
          <div className="alarm-actions" style={{ marginTop: 0 }}>
            <button className="btn primary" disabled={busy || !key.trim()} onClick={() => save({ apiKey: key.trim(), enabled: true }, "Key verified and saved. AI is on.")}>
              {busy ? "Checking…" : "Save key"}
            </button>
            {st.hasKey && (
              <button className="btn" disabled={busy} onClick={() => save({ enabled: !st.enabled }, st.enabled ? "AI turned off." : "AI turned on.")}>
                {st.enabled ? "Turn off" : "Turn on"}
              </button>
            )}
            {st.hasKey && (
              <button className="btn danger" disabled={busy} onClick={() => save({ apiKey: "", enabled: false }, "Key removed.")}>
                Remove key
              </button>
            )}
          </div>
          <label className="field" style={{ marginTop: 12 }}>
            <span>Daily limit on descriptions</span>
            <select value={limit} onChange={(e) => save({ dailyLimit: Number(e.target.value) }, "Limit saved.").then(() => setLimit(Number(e.target.value)))}>
              {[50, 100, 300, 1000, 3000].map((n) => (
                <option key={n} value={n}>
                  {n} per day
                </option>
              ))}
            </select>
            <small>Stops a busy camera from running up your Anthropic bill. Search and digests aren't counted.</small>
          </label>
        </>
      )}
    </div>
  );
}
