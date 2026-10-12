import { useEffect, useState } from "react";
import { api, type MQTTInput, type MQTTSettings } from "../api";

/** System → MQTT & Home Assistant. Rendered for users who can change settings. */
export default function MQTTCard() {
  const [st, setSt] = useState<MQTTSettings | null>(null);
  const [form, setForm] = useState<MQTTInput>({});
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");

  function load(s: MQTTSettings) {
    setSt(s);
    setForm({
      enabled: s.enabled,
      host: s.host,
      port: s.port,
      tls: s.tls,
      username: s.username,
      topicPrefix: s.topicPrefix,
      discoveryPrefix: s.discoveryPrefix,
      allowCommands: s.allowCommands,
    });
  }

  useEffect(() => {
    api.mqtt().then(load).catch(() => {});
    // Refresh the connection status while the page is open.
    const t = setInterval(() => api.mqtt().then((s) => setSt((prev) => (prev ? { ...prev, status: s.status } : s))).catch(() => {}), 5000);
    return () => clearInterval(t);
  }, []);

  const set = <K extends keyof MQTTInput>(k: K, v: MQTTInput[K]) => setForm((f) => ({ ...f, [k]: v }));
  const patch = (): MQTTInput => (password ? { ...form, password } : form);

  async function save(extra: MQTTInput = {}, ok = "Saved.") {
    setBusy(true);
    setError("");
    setMsg("");
    try {
      load(await api.saveMQTT({ ...patch(), ...extra }));
      setPassword("");
      setMsg(ok);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function test() {
    setBusy(true);
    setError("");
    setMsg("");
    try {
      const r = await api.testMQTT(patch());
      if (r.ok) setMsg("Connected to the broker.");
      else setError(r.error || "Connection failed.");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return null;
  const s = st.status;
  const badge =
    s.state === "connected" ? ["green", "Connected"] : s.state === "connecting" ? ["blue", "Connecting…"] : s.state === "error" ? ["red", "Not connected"] : ["blue", "Off"];
  return (
    <div className="card">
      <h2>MQTT & Home Assistant</h2>
      <div className="stat">
        <span>Broker</span>
        <span className={`badge ${badge[0]}`}>{badge[1]}</span>
      </div>
      {s.state === "error" && s.error && <div className="error" style={{ marginTop: 10 }}>{s.error}</div>}
      {s.dropped > 0 && (
        <div className="stat">
          <span>Messages dropped (broker unreachable)</span>
          <span>{s.dropped}</span>
        </div>
      )}
      <p className="muted small" style={{ marginTop: 10 }}>
        Publishes motion, people, vehicles, events, alarms, camera status and the latest snapshot to your MQTT broker. Home Assistant finds each camera automatically (MQTT discovery).
      </p>
      {msg && <div className="notice">{msg}</div>}
      {error && <div className="error">{error}</div>}
      <label className="check">
        <input type="checkbox" checked={!!form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
        Enabled
      </label>
      <div className="grid-2" style={{ gap: 10 }}>
        <label className="field">
          <span>Broker host</span>
          <input type="text" value={form.host ?? ""} onChange={(e) => set("host", e.target.value)} placeholder="homeassistant.local" autoComplete="off" />
        </label>
        <label className="field">
          <span>Port</span>
          <input type="number" min={1} max={65535} value={form.port ?? 1883} onChange={(e) => set("port", Number(e.target.value))} />
        </label>
        <label className="field">
          <span>Username</span>
          <input type="text" value={form.username ?? ""} onChange={(e) => set("username", e.target.value)} autoComplete="off" />
        </label>
        <label className="field">
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={st.hasPassword ? "•••••••• (saved)" : ""}
            autoComplete="new-password"
          />
        </label>
        <label className="field">
          <span>Topic prefix</span>
          <input type="text" value={form.topicPrefix ?? ""} onChange={(e) => set("topicPrefix", e.target.value)} placeholder="sentinel" />
        </label>
        <label className="field">
          <span>Discovery prefix</span>
          <input type="text" value={form.discoveryPrefix ?? ""} onChange={(e) => set("discoveryPrefix", e.target.value)} placeholder="homeassistant" />
        </label>
      </div>
      <label className="check">
        <input
          type="checkbox"
          checked={!!form.tls}
          onChange={(e) => {
            const on = e.target.checked;
            set("tls", on);
            if (on && form.port === 1883) set("port", 8883);
            if (!on && form.port === 8883) set("port", 1883);
          }}
        />
        Use TLS
      </label>
      <label className="check">
        <input type="checkbox" checked={!!form.allowCommands} onChange={(e) => set("allowCommands", e.target.checked)} />
        Allow Home Assistant to switch recording and detection
      </label>
      <p className="muted small" style={{ marginTop: -6 }}>
        Adds Recording and Detection switches to each camera. Anyone who can publish to your broker could then stop recording, so only turn this on if the broker requires a login. Every switch is recorded in the audit log.
      </p>
      <div className="alarm-actions" style={{ marginTop: 8 }}>
        <button className="btn primary" disabled={busy} onClick={() => save()}>
          Save
        </button>
        <button className="btn" disabled={busy || !form.host?.trim()} onClick={test}>
          {busy ? "Working…" : "Test connection"}
        </button>
        {st.hasPassword && (
          <button className="btn danger" disabled={busy} onClick={() => save({ password: "" }, "Password removed.")}>
            Remove password
          </button>
        )}
      </div>
    </div>
  );
}
