import { useEffect, useState } from "react";
import { api, can, formatBytes, type AIStatus, type SystemInfo, type UpdateStatus, type User } from "../api";
import MQTTCard from "./MQTTCard";
import HomeKitCard from "./HomeKitCard";
import WebRTCCard from "./WebRTCCard";
import HardwareCard from "./HardwareCard";
import DetectionCard from "./DetectionCard";
import { PageHeader } from "../shared/Cards"; // WAVE21

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
      <PageHeader title="System" subtitle="Server health and storage" />
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
            {info.recordingsDir === undefined ? (
              // A Management Server has no recorder of its own: recordings and
              // disks live on the Recording Servers (Admin → Servers).
              <p className="muted small">Recordings are stored on the Recording Servers. See Admin → Recording Servers for their disks.</p>
            ) : (
              <>
                <div className="meter" style={{ margin: "4px 0 12px" }}>
                  <div style={{ width: `${Math.round(used * 100)}%`, background: used > 0.9 ? "var(--red)" : used > 0.8 ? "var(--amber)" : undefined }} />
                </div>
                <div className="stat">
                  <span>Free</span>
                  <span>{info.diskFreeBytes !== undefined ? formatBytes(info.diskFreeBytes) : "—"}</span>
                </div>
                <div className="stat">
                  <span>Recordings</span>
                  <span>{info.recordingsBytes !== undefined ? formatBytes(info.recordingsBytes) : "—"}</span>
                </div>
                <div className="stat">
                  <span>Location</span>
                  <span className="mono">{info.recordingsDir}</span>
                </div>
              </>
            )}
          </div>
          <UpdatesCard user={user} />
          <AICard user={user} />
          {can.changeSettings(user) && <MQTTCard />}
          {can.changeSettings(user) && <HomeKitCard />}
          <WebRTCCard user={user} />
          <HardwareCard user={user} />
          <DetectionCard user={user} />
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

function UpdatesCard({ user }: { user: User }) {
  const [st, setSt] = useState<UpdateStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const admin = can.changeSettings(user);

  useEffect(() => {
    api.update().then(setSt).catch(() => {});
  }, []);

  async function save(patch: { checkEnabled?: boolean; checkNow?: boolean }) {
    setBusy(true);
    setError("");
    try {
      setSt(await api.saveUpdate(patch));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return null;
  const checked = st.checkedAt ? new Date(st.checkedAt * 1000).toLocaleString() : "Not yet";
  return (
    <div className="card">
      <h2>Updates</h2>
      <div className="stat">
        <span>This server</span>
        <span>{st.current}</span>
      </div>
      <div className="stat">
        <span>Newest release</span>
        <span className={`badge ${st.available ? "amber" : "green"}`}>
          {st.available ? `${st.latest} available` : st.latest ? "Up to date" : "Unknown"}
        </span>
      </div>
      <div className="stat">
        <span>Last checked</span>
        <span>{checked}</span>
      </div>
      {(error || st.error) && <div className="error" style={{ marginTop: 10 }}>{error || st.error}</div>}
      {st.available && (
        <>
          {st.notes && <p className="small" style={{ marginTop: 10 }}>{st.notes}</p>}
          {st.docker ? (
            <>
              <p className="muted small" style={{ marginTop: 10 }}>
                On the Docker host, in the folder with your docker-compose.yml, pull the new image and restart. Cameras, users and recordings are kept.
              </p>
              <div className="code-block">{`docker compose pull\ndocker compose up -d`}</div>
            </>
          ) : (
            <>
              <p className="muted small" style={{ marginTop: 10 }}>
                Download the new version, copy it to this server, then run the same commands as a fresh install. Cameras, users and recordings are kept.
              </p>
              <div className="code-block">{`tar xzf sentinel-${st.latest}-linux-*.tar.gz\ncd sentinel-${st.latest}-linux-*/\nsudo ./install.sh`}</div>
              <a className="btn primary" href={st.url} target="_blank" rel="noopener noreferrer">
                Download {st.latest}
              </a>
            </>
          )}
        </>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        Once a day this server asks sentvms.com for the newest version number. It sends only the server's version; nothing about your cameras or footage.
      </p>
      {admin && (
        <div className="alarm-actions" style={{ marginTop: 8 }}>
          <button className="btn" disabled={busy} onClick={() => save({ checkNow: true })}>
            {busy ? "Checking…" : "Check now"}
          </button>
          <button className="btn" disabled={busy} onClick={() => save({ checkEnabled: !st.checkEnabled })}>
            {st.checkEnabled ? "Turn off daily check" : "Turn on daily check"}
          </button>
        </div>
      )}
    </div>
  );
}
