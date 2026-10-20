// The arithmetic behind the Operator's Reports view, kept pure so it can be
// tested without a server: turns the activity response into per-camera rows,
// per-kind totals and a time series for the bars.

export interface ActivityEvent { cameraId: string; kind: string; hour: number; count: number }
export interface ActivityAlarm { cameraId: string; kind: string; severity: string; count: number }
export interface Activity {
  from: number;
  to: number;
  totals: { events: number; alarms: number };
  events: ActivityEvent[];
  alarms: ActivityAlarm[];
}

export interface CameraRow {
  cameraId: string;
  name: string;
  events: number;
  alarms: number;
  topKind: string | null;
}

export const RANGES = [
  { id: "24h", label: "Last 24 hours", ms: 24 * 3600_000, bucket: "hour" as const },
  { id: "7d", label: "Last 7 days", ms: 7 * 24 * 3600_000, bucket: "day" as const },
  { id: "31d", label: "Last 31 days", ms: 31 * 24 * 3600_000, bucket: "day" as const },
];

/** One row per camera, busiest first. Names fall back to the camera id. */
export function cameraRows(a: Activity, names: Map<string, string>): CameraRow[] {
  const rows = new Map<string, CameraRow & { kinds: Map<string, number> }>();
  const row = (id: string) => {
    let r = rows.get(id);
    if (!r) {
      r = { cameraId: id, name: names.get(id) ?? id, events: 0, alarms: 0, topKind: null, kinds: new Map() };
      rows.set(id, r);
    }
    return r;
  };
  for (const e of a.events) {
    const r = row(e.cameraId);
    r.events += e.count;
    r.kinds.set(e.kind, (r.kinds.get(e.kind) ?? 0) + e.count);
  }
  for (const al of a.alarms) row(al.cameraId).alarms += al.count;
  return [...rows.values()]
    .map(({ kinds, ...r }) => {
      let top: string | null = null, best = 0;
      for (const [k, n] of kinds) if (n > best) { best = n; top = k; }
      return { ...r, topKind: top };
    })
    .sort((x, y) => y.events + y.alarms - (x.events + x.alarms) || x.name.localeCompare(y.name));
}

/** Alarm counts per severity, in the order an operator reads them. */
export function severityTotals(a: Activity): { severity: string; count: number }[] {
  const order = ["Critical", "Warning", "Info"];
  const rank = (s: string) => (order.includes(s) ? order.indexOf(s) : order.length);
  const m = new Map<string, number>();
  for (const al of a.alarms) m.set(al.severity, (m.get(al.severity) ?? 0) + al.count);
  return [...m.entries()]
    .map(([severity, count]) => ({ severity, count }))
    .sort((x, y) => rank(x.severity) - rank(y.severity) || y.count - x.count);
}

/** Event counts per bucket across all cameras: hours for a day, days for longer ranges. */
export function series(a: Activity, bucket: "hour" | "day"): { at: number; count: number }[] {
  const key = (t: number) => {
    if (bucket === "hour") return Math.floor(t / 3600_000) * 3600_000;
    const d = new Date(t);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  };
  const m = new Map<number, number>();
  for (const e of a.events) {
    const k = key(e.hour);
    m.set(k, (m.get(k) ?? 0) + e.count);
  }
  const out: { at: number; count: number }[] = [];
  const step = bucket === "hour" ? 3600_000 : 24 * 3600_000;
  for (let t = key(a.from); t < a.to; t += step) out.push({ at: t, count: m.get(t) ?? 0 });
  return out;
}

export function kindLabel(kind: string): string {
  return kind ? kind : "Other";
}
