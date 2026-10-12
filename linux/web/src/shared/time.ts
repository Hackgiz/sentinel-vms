// Time helpers shared by the timeline and the playback views. Unix ms.

/** A stretch of time; recorded spans also carry `media` (seconds into the window's playlist). */
export interface TimeSpan {
  start: number;
  end: number;
  media?: number;
}

/** A tick on the timeline: a detection event or an alarm flag. */
export interface Marker {
  at: number; // Unix ms
  cameraID: string;
  kind: string; // Motion | Person | Vehicle | Animal | alarm
  label: string;
  alarm: boolean;
  severity?: string;
}

/** Timeline markers from an API v2 Timeline (all times already ms). */
export function markersFromV2(
  cameraId: string,
  tl: { events: { createdAt: number; kind: string; label?: string }[]; alarms: { createdAt: number; kind: string; title: string; severity: string }[] },
): Marker[] {
  const out: Marker[] = tl.events.map((e) => ({ at: e.createdAt, cameraID: cameraId, kind: e.kind, label: e.label || e.kind, alarm: false }));
  for (const a of tl.alarms) out.push({ at: a.createdAt, cameraID: cameraId, kind: a.kind, label: a.title, alarm: true, severity: a.severity });
  return out.sort((x, y) => x.at - y.at);
}

/** Local midnight of the day containing t, and of the next day (DST-safe). */
export function dayBounds(t: number): [number, number] {
  const d = new Date(t);
  const start = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const end = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime();
  return [start, end];
}

const clockFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
const hmFmt = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hour12: false });

export const fmtClock = (t: number) => clockFmt.format(new Date(t));
export const fmtHM = (t: number) => hmFmt.format(new Date(t));

/** "3 min ago" style, for last-seen columns. */
export function fmtAgo(t: number, now = Date.now()): string {
  if (!t) return "Never";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 10) return "Just now";
  if (s < 60) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h ago`;
  return new Date(t).toLocaleDateString();
}
