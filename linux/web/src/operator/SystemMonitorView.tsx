// System Monitor: a card per recording server (status, version, uptime and
// CPU / memory / disk meters) above one table of every camera's health.
// Refreshes every 10 s, only while the page is visible (the one timer of
// this view); the Refresh button reads on demand. Offline transitions between
// readings go out on the notice bus. WAVE17: the Admin's Detection card
// below, on this session's client (controls only with ChangeSettings).
import { useCallback, useEffect, useRef, useState } from "react";
import DetectionCard from "../pages/DetectionCard";
import { errorMessage, type ServerHealth } from "../shared/api";
import { usePageVisible } from "./CameraGrid";
import { cameraRows, offlineTransitions, serverCard, type Level, type Meter } from "./healthModel";
import { emitNotice } from "./notices";
import { useSigned } from "./session";
import { PageHeader } from "../shared/Cards";

export const monitorIntervalMs = 10_000;

function MeterBar({ label, m }: { label: string; m: Meter }) {
  return (
    <div className="mon-meter" title={m.detail || undefined}>
      <span className="mon-meter-label">{label}</span>
      <span className="mon-bar" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={m.pct ?? undefined}>
        <span className={`mon-fill ${m.level}`} style={{ width: `${m.pct ?? 0}%` }} />
      </span>
      <span className={`mon-meter-value ${m.level}`}>{m.label}</span>
    </div>
  );
}

const stateWord: Record<Level, string> = { ok: "OK", warn: "Warning", crit: "Critical", off: "" };

export default function SystemMonitorView() {
  const { session, can } = useSigned();
  const client = session.client;
  const visible = usePageVisible();
  const [servers, setServers] = useState<ServerHealth[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [readAt, setReadAt] = useState(0);
  const prev = useRef<ServerHealth[] | null>(null);
  const inflight = useRef(false);

  const refresh = useCallback(async () => {
    if (inflight.current) return;
    inflight.current = true;
    setBusy(true);
    try {
      const next = await client.system.health();
      for (const n of offlineTransitions(prev.current, next)) emitNotice(n);
      prev.current = next;
      setServers(next);
      setReadAt(Date.now());
      setError("");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      inflight.current = false;
      setBusy(false);
    }
  }, [client]);

  // Read on entering (and on coming back to the tab), then every 10 s while it is visible.
  useEffect(() => {
    if (!visible) return;
    void refresh();
    const t = setInterval(() => void refresh(), monitorIntervalMs);
    return () => clearInterval(t);
  }, [visible, refresh]);

  const cards = (servers ?? []).map(serverCard);
  const rows = cameraRows(servers ?? []);

  return (
    <div className="mon">
      <PageHeader
        title="System Monitor"
        subtitle={servers ? `${servers.length} server${servers.length === 1 ? "" : "s"}${readAt ? ` · updated ${new Date(readAt).toLocaleTimeString()}` : ""}` : "Reading the servers…"}
      >
        <button className="btn small" onClick={() => void refresh()} disabled={busy}>
          {busy ? "Refreshing…" : "Refresh"}
        </button>
      </PageHeader>
      {error && <div className="error" role="alert">{error}</div>}
      {!servers && !error && (
        <div className="scan-status" role="status">
          <span className="spinner" aria-hidden="true" />
          Reading the servers…
        </div>
      )}

      <div className="mon-cards">
        {cards.map((c) => (
          <section key={c.id} className={`mon-card ${c.online ? "" : "offline"}`} aria-label={c.name}>
            <header>
              <span className={`mon-dot ${c.state}`} title={c.online ? stateWord[c.state] : "Offline"} />
              <strong>{c.name}</strong>
              <span className="mon-sub">{c.online ? "Online" : "Offline"}</span>
            </header>
            <div className="mon-facts">
              {c.version && <span>v{c.version}</span>}
              {c.uptime && <span>up {c.uptime}</span>}
              {c.latency && <span>{c.latency}</span>}
              {c.db && <span>DB {c.db}</span>}
              <span>
                {c.camerasOnline}/{c.cameras} cameras
              </span>
            </div>
            <MeterBar label="CPU" m={c.cpu} />
            <MeterBar label="Memory" m={c.mem} />
            <MeterBar label="Disk" m={c.disk} />
          </section>
        ))}
        {servers && cards.length === 0 && <p className="mon-sub">No recording servers.</p>}
      </div>

      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="list mon-table">
            <thead>
              <tr>
                <th>Server</th>
                <th>Camera</th>
                <th>Online</th>
                <th>Recording</th>
                <th>Last segment</th>
                <th>Ingest</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.serverId}:${r.id}`} className={`mon-row ${r.state}`}>
                  <td>{r.server}</td>
                  <td>{r.name}</td>
                  <td>
                    <span className={`mon-pill ${r.online ? "ok" : "crit"}`}>{r.online ? "Online" : "Offline"}</span>
                  </td>
                  <td>{r.recording ? <span className="mon-pill ok">Recording</span> : <span className="mon-sub">{r.online ? "Not recording" : "—"}</span>}</td>
                  <td className={`mon-seg ${r.lastSegmentLevel}`}>{r.lastSegment}</td>
                  <td className="mono">{r.kbps || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {can("viewLive") && (
        <div className="mon-detection">
          <DetectionCard client={client} canChange={can("changeSettings")} />
        </div>
      )}
    </div>
  );
}
