// Reports workspace: how busy the site was over a range. Totals, the series
// of detections as bars, alarms by severity and a row per camera. Counts
// come from /api/v2/reports/activity, already limited to the cameras this
// person can see.

import { useEffect, useMemo, useState } from "react";
import { errorMessage } from "../shared/api";
import { useData } from "./data";
import { useSigned } from "./session";
import { PageHeader } from "../shared/Cards";
import { RANGES, cameraRows, kindLabel, series, severityTotals, type Activity } from "./reportModel";

export default function ReportsView() {
  const { session } = useSigned();
  const data = useData();
  const [rangeId, setRangeId] = useState<string>("24h");
  const [report, setReport] = useState<Activity | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const range = RANGES.find((r) => r.id === rangeId) ?? RANGES[0];

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError("");
    const to = Date.now();
    session.client.reports
      .activity({ from: to - range.ms, to })
      .then((r) => { if (alive) setReport(r as Activity); })
      .catch((e) => { if (alive) setError(errorMessage(e)); })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [session.client, range.ms]);

  const names = useMemo(() => new Map((data.cameras ?? []).map((c) => [c.id, c.name])), [data.cameras]);
  const rows = useMemo(() => (report ? cameraRows(report, names) : []), [report, names]);
  const bars = useMemo(() => (report ? series(report, range.bucket) : []), [report, range.bucket]);
  const peak = Math.max(1, ...bars.map((b) => b.count));
  const sev = useMemo(() => (report ? severityTotals(report) : []), [report]);

  return (
    <div className="reports" style={{ display: "grid", gap: 16, alignContent: "start", overflow: "auto", height: "100%" }}>
      <PageHeader title="Reports" subtitle={report ? `${report.totals.events.toLocaleString()} detections over ${range.label.toLowerCase()}` : `Activity over ${range.label.toLowerCase()}`}>
        <div className="seg" role="group" aria-label="Range">
          {RANGES.map((r) => (
            <button key={r.id} type="button" className={r.id === rangeId ? "on" : ""} aria-pressed={r.id === rangeId} onClick={() => setRangeId(r.id)}>
              {r.label}
            </button>
          ))}
        </div>
      </PageHeader>

      {error && <div className="error">{error}</div>}
      {loading && !report && <p className="muted">Counting…</p>}

      {report && (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit,minmax(160px,1fr))", gap: 12 }}>
            <Stat label="Detections" value={report.totals.events} />
            <Stat label="Alarms" value={report.totals.alarms} />
            <Stat label="Cameras with activity" value={rows.filter((r) => r.events + r.alarms > 0).length} />
          </div>

          <section className="card" style={{ padding: 16 }}>
            <h3 style={{ marginTop: 0 }}>Detections {range.bucket === "hour" ? "per hour" : "per day"}</h3>
            {report.totals.events === 0 ? (
              <p className="muted">Nothing was detected in this range.</p>
            ) : (
              <div role="img" aria-label="Detections over time" style={{ display: "flex", alignItems: "flex-end", gap: 2, height: 140 }}>
                {bars.map((b) => (
                  <div
                    key={b.at}
                    title={`${new Date(b.at).toLocaleString()}: ${b.count}`}
                    style={{ flex: 1, minWidth: 2, height: `${Math.round((b.count / peak) * 100)}%`, background: b.count ? "var(--accent, #3b82f6)" : "transparent", borderRadius: 2 }}
                  />
                ))}
              </div>
            )}
          </section>

          <section className="card" style={{ padding: 16 }}>
            <h3 style={{ marginTop: 0 }}>Alarms by severity</h3>
            {sev.length === 0 ? (
              <p className="muted">No alarms in this range.</p>
            ) : (
              <ul style={{ margin: 0, paddingLeft: 18 }}>
                {sev.map((s) => <li key={s.severity}>{s.severity}: {s.count}</li>)}
              </ul>
            )}
          </section>

          <section className="card" style={{ padding: 16, overflowX: "auto" }}>
            <h3 style={{ marginTop: 0 }}>By camera</h3>
            {rows.length === 0 ? (
              <p className="muted">No camera activity in this range.</p>
            ) : (
              <table className="list" style={{ width: "100%" }}>
                <thead>
                  <tr><th>Camera</th><th>Detections</th><th>Alarms</th><th>Most often</th></tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr key={r.cameraId}>
                      <td>{r.name}</td>
                      <td>{r.events}</td>
                      <td>{r.alarms}</td>
                      <td>{r.topKind ? kindLabel(r.topKind) : "–"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="card" style={{ padding: 16 }}>
      <div className="muted" style={{ fontSize: 13 }}>{label}</div>
      <div style={{ fontSize: 28, fontWeight: 600 }}>{value.toLocaleString()}</div>
    </div>
  );
}
