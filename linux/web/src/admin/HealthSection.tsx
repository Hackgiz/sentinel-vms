import { useEffect, useState } from "react";
import type { RecordingServer } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { fmtAgo } from "../shared/time";
import { v2 } from "./client";
import { StorageRow } from "./OverviewSection";
import { Empty, ErrorBanner, Loading, OnlineBadge, useLiveReload } from "./ui";

export default function HealthSection({ canManage }: { canManage: boolean }) {
  const servers = useLoad(() => v2.servers.list(), []);
  const [now, setNow] = useState(Date.now());
  useLiveReload(servers.reload, ["recorder.status"]);
  useEffect(() => {
    const t = setInterval(() => {
      setNow(Date.now());
      void servers.reload();
    }, 15_000);
    return () => clearInterval(t);
  }, [servers.reload]);
  const list = (servers.data ?? []).filter((s) => !s.revoked);

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Health</h1>
          <div className="sub">Media engine, capabilities and configuration of every recording server</div>
        </div>
        {canManage && (
          <a className="btn" href="#/admin/servers">
            Manage servers
          </a>
        )}
      </div>
      <ErrorBanner error={servers.error} onRetry={servers.reload} />
      {servers.data === null ? (
        servers.error ? null : (
          <div className="card">
            <Loading />
          </div>
        )
      ) : list.length === 0 ? (
        <div className="card">
          <Empty icon="▤" title="No recording servers" />
        </div>
      ) : (
        <div className="grid-2">
          {list.map((s) => (
            <HealthCard key={s.id} server={s} now={now} />
          ))}
        </div>
      )}
    </>
  );
}

function Yes({ on, label }: { on: boolean; label: string }) {
  return <span className={`badge ${on ? "green" : "blue"}`}>{on ? label : `No ${label.toLowerCase()}`}</span>;
}

function HealthCard({ server: s, now }: { server: RecordingServer; now: number }) {
  return (
    <section className="card" aria-label={s.name}>
      <div className="health-head">
        <h2>{s.name}</h2>
        <OnlineBadge online={s.online} />
      </div>
      <div className="stat">
        <span>Media engine</span>
        <span className={`badge ${s.mediaRunning ? "green" : "red"}`}>
          <span className="dot" />
          {s.mediaRunning ? "Running" : "Stopped"}
        </span>
      </div>
      {s.mediaError && <div className="error" style={{ margin: "8px 0" }}>{s.mediaError}</div>}
      <div className="stat">
        <span>Capabilities</span>
        <span className="chip-grid" style={{ justifyContent: "flex-end" }}>
          <Yes on={s.capabilities.detection} label="Motion detection" />
          <Yes on={s.capabilities.objects} label="Object detection" />
          <Yes on={s.capabilities.webrtc} label="WebRTC" />
        </span>
      </div>
      <div className="stat">
        <span>Config revision</span>
        <span className="mono">{s.configRev}</span>
      </div>
      <div className="stat">
        <span>Version</span>
        <span className="mono">{s.version || "—"}</span>
      </div>
      <div className="stat">
        <span>Cameras</span>
        <span>{s.cameras}</span>
      </div>
      <div className="stat">
        <span>Last seen</span>
        <span>{s.online ? "Now" : fmtAgo(s.lastSeenAt, now)}</span>
      </div>
      <div className="stat">
        <span>Video</span>
        <span className="mono">{s.publicUrl || "Through the management server"}</span>
      </div>
      <div style={{ marginTop: 10 }}>
        <StorageRow server={s} />
      </div>
    </section>
  );
}
