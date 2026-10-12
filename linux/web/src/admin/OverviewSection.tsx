import { useEffect } from "react";
import { fetchAll, type RecordingServer } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { formatBytes, v2 } from "./client";
import { Empty, ErrorBanner, Loading, OnlineBadge, useLiveReload, useLiveMessages } from "./ui";

const alarmCap = 1000;

export default function OverviewSection() {
  const servers = useLoad(() => v2.servers.list(), []);
  const cameras = useLoad(() => v2.cameras.list(), []);
  const alarms = useLoad(() => fetchAll((cursor) => v2.alarms.page({ state: "open", cursor, limit: 500 }), alarmCap), []);

  useLiveReload(servers.reload, ["recorder.status"]);
  useLiveReload(cameras.reload, ["config.changed"]);
  useLiveReload(alarms.reload, ["alarm.new", "alarm.updated"]);
  // camera.status: patch in place, no refetch.
  useLiveMessages((m) => {
    if (m.type !== "camera.status") return;
    cameras.setData((list) => list?.map((c) => (c.id === m.data.cameraId ? { ...c, status: { ...c.status, online: m.data.online } } : c)) ?? list);
  });
  // Storage and last-seen move without events; a slow poll keeps them current.
  useEffect(() => {
    const t = setInterval(() => void servers.reload(), 30_000);
    return () => clearInterval(t);
  }, [servers.reload]);

  const s = servers.data ?? [];
  const c = cameras.data ?? [];
  const active = s.filter((x) => !x.revoked);
  const sOnline = active.filter((x) => x.online).length;
  const cOnline = c.filter((x) => x.status.online).length;
  const open = alarms.data?.length ?? 0;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Overview</h1>
          <div className="sub">The whole site at a glance</div>
        </div>
      </div>
      <ErrorBanner error={servers.error || cameras.error} onRetry={() => (servers.reload(), cameras.reload())} />
      <div className="admin-stats">
        <StatCard
          title="Recording servers"
          loading={servers.data === null && !servers.error}
          value={`${sOnline}/${active.length}`}
          note={active.length - sOnline > 0 ? `${active.length - sOnline} offline` : "All online"}
          tone={active.length - sOnline > 0 ? "red" : "green"}
          href="#/admin/servers"
        />
        <StatCard
          title="Cameras"
          loading={cameras.data === null && !cameras.error}
          value={`${cOnline}/${c.length}`}
          note={c.length - cOnline > 0 ? `${c.length - cOnline} offline` : c.length ? "All online" : "None yet"}
          tone={c.length - cOnline > 0 ? "red" : "green"}
          href="#/admin/cameras"
        />
        <StatCard
          title="Open alarms"
          loading={alarms.data === null && !alarms.error}
          value={alarms.error ? "—" : open >= alarmCap ? `${alarmCap - 1}+` : String(open)}
          note={alarms.error ? "Couldn't load alarms" : open ? "Waiting for an operator" : "Nothing open"}
          tone={open ? "amber" : "green"}
          href="#/alarms"
        />
      </div>

      <div className="card" style={{ marginTop: 14 }}>
        <h2>Storage per server</h2>
        {servers.data === null && !servers.error ? (
          <Loading />
        ) : active.length === 0 ? (
          <Empty icon="▤" title="No recording servers">
            <a href="#/admin/servers">Enroll a recording server</a> to start recording.
          </Empty>
        ) : (
          <div className="storage-list">
            {active.map((x) => (
              <StorageRow key={x.id} server={x} />
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function StatCard({ title, value, note, tone, href, loading }: { title: string; value: string; note: string; tone: "red" | "green" | "amber"; href: string; loading: boolean }) {
  return (
    <a className="card stat-card" href={href}>
      <div className="stat-title">{title}</div>
      {loading ? (
        <Loading />
      ) : (
        <>
          <div className="stat-value">{value}</div>
          <span className={`badge ${tone}`}>{note}</span>
        </>
      )}
    </a>
  );
}

export function StorageRow({ server }: { server: RecordingServer }) {
  const st = server.storage;
  const used = st?.diskTotalBytes && st.diskFreeBytes !== undefined ? 1 - st.diskFreeBytes / st.diskTotalBytes : null;
  return (
    <div className="storage-row">
      <div className="storage-name">
        <strong>{server.name}</strong> <OnlineBadge online={server.online} />
      </div>
      <div
        className="meter"
        role="meter"
        aria-label={`${server.name} disk used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={used === null ? undefined : Math.round(used * 100)}
      >
        <div style={{ width: `${Math.round((used ?? 0) * 100)}%`, background: used !== null && used > 0.9 ? "var(--red)" : used !== null && used > 0.8 ? "var(--amber)" : undefined }} />
      </div>
      <div className="mono storage-figures">
        {st ? (
          <>
            {formatBytes(st.recordingsBytes)} recordings
            {st.diskFreeBytes !== undefined && ` · ${formatBytes(st.diskFreeBytes)} free`}
            {st.diskTotalBytes !== undefined && ` of ${formatBytes(st.diskTotalBytes)}`}
          </>
        ) : (
          "No storage report yet"
        )}
      </div>
    </div>
  );
}
