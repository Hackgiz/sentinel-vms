import { useEffect, useState } from "react";
import { useLoad } from "../shared/hooks";
import { Empty, ErrorBanner, Loading } from "./ui";
import { systemApi, type SystemTask } from "./systemApi";

const kindLabel: Record<SystemTask["kind"], string> = { discovery: "Discovery", export: "Export", import: "Import" };
const stateTone: Record<SystemTask["state"], string> = { running: "blue", done: "green", failed: "red" };

function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export default function TasksSection() {
  const tasks = useLoad(() => systemApi.tasks(), []);
  const [now, setNow] = useState(Date.now());
  // The pane's one interval; it stops while the tab is hidden.
  useEffect(() => {
    const t = setInterval(() => {
      if (document.hidden) return;
      setNow(Date.now());
      void tasks.reload();
    }, 3000);
    return () => clearInterval(t);
  }, [tasks.reload]);
  const list = tasks.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Current tasks</h1>
          <div className="sub">Camera scans, evidence exports and imports on the management server. Finished tasks stay listed for 15 minutes.</div>
        </div>
      </div>
      <ErrorBanner error={tasks.error} onRetry={tasks.reload} />
      <div className="card table-wrap">
        {tasks.data === null ? (
          tasks.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="⚙" title="Nothing running" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Task</th>
                <th>Kind</th>
                <th>State</th>
                <th>Progress</th>
                <th>Started</th>
                <th>Duration</th>
              </tr>
            </thead>
            <tbody>
              {list.map((t) => (
                <tr key={t.id}>
                  <td>
                    {t.title}
                    {t.error && <div className="error small">{t.error}</div>}
                  </td>
                  <td>{kindLabel[t.kind] ?? t.kind}</td>
                  <td>
                    <span className={`badge ${stateTone[t.state]}`}>{t.state}</span>
                  </td>
                  <td style={{ minWidth: 120 }}>
                    {t.progress < 0 ? (
                      <span className="muted small">{t.state === "running" ? "Working…" : "—"}</span>
                    ) : (
                      <div className="meter" role="progressbar" aria-label={`${t.title} progress`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={t.progress}>
                        <div style={{ width: `${t.progress}%` }} />
                      </div>
                    )}
                  </td>
                  <td className="mono nowrap">{new Date(t.startedAt).toLocaleTimeString()}</td>
                  <td className="mono nowrap">{duration((t.finishedAt || now) - t.startedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
