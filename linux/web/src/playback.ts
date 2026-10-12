// Time math for timeline playback. Wall times are Unix ms; media times are
// seconds into a camera's playback.m3u8 for the same window (the server lays
// recorded spans end to end, so gaps take no media time).

import type { Timeline, TimelineSpan } from "./api";
import type { Marker } from "./shared/time";

export { dayBounds, fmtClock, fmtHM, type Marker } from "./shared/time";

/** Media time that plays wall time t, or null when t is in a gap. */
export function wallToMedia(spans: TimelineSpan[], t: number): number | null {
  for (const s of spans) {
    if (t >= s.start && t < s.end) return s.media + (t - s.start) / 1000;
  }
  return null;
}

/** Wall time shown at media time m (clamped to the last span's end). */
export function mediaToWall(spans: TimelineSpan[], m: number): number | null {
  for (let i = 0; i < spans.length; i++) {
    const s = spans[i];
    const len = (s.end - s.start) / 1000;
    if (m >= s.media && (m < s.media + len || i === spans.length - 1)) {
      return Math.min(s.start + (m - s.media) * 1000, s.end);
    }
  }
  return null;
}

/** Start of the first span that begins after t, or null. */
export function nextSpanStart(spans: TimelineSpan[], t: number): number | null {
  for (const s of spans) if (s.start > t) return s.start;
  return null;
}

/** Is there any recording in (a, b)? */
export function recordedBetween(spans: TimelineSpan[], a: number, b: number): boolean {
  return spans.some((s) => s.start < b && s.end > a);
}

export function markersOf(cameraID: string, tl: Timeline): Marker[] {
  const out: Marker[] = tl.events.map((e) => ({
    at: e.createdAt * 1000,
    cameraID,
    kind: e.kind,
    label: e.label || e.kind,
    alarm: false,
  }));
  for (const a of tl.alarms) out.push({ at: a.createdAt * 1000, cameraID, kind: a.kind, label: a.title, alarm: true, severity: a.severity });
  return out.sort((x, y) => x.at - y.at);
}

/** Previous/next marker relative to t; a little slack so repeated presses step. */
export function adjacentMarker(markers: Marker[], t: number, dir: 1 | -1): Marker | null {
  if (dir > 0) return markers.find((m) => m.at > t + 500) ?? null;
  for (let i = markers.length - 1; i >= 0; i--) if (markers[i].at < t - 4000) return markers[i];
  return null;
}

export function dateInputValue(t: number): string {
  const d = new Date(t);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export function parseDateInput(v: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v);
  return m ? new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3])).getTime() : null;
}

export function fmtDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${String(sec).padStart(2, "0")}s` : `${sec}s`;
}
