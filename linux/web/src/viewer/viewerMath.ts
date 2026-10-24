// Pure logic for the unified camera viewer: the visible time window, mapping
// between time and timeline position, the data window to fetch, stepping,
// marker clustering and deep-link parsing. Times are Unix ms.

import { exportBlocker, mergeSpans, nextGap, prevGap, selectionRange, type Range, type Selection } from "../operator/timelineMath";
import type { Marker } from "../shared/time";

export const HOUR = 3600e3;
export const DAY = 24 * HOUR;
export const MIN_SPAN = 60e3;
export const MAX_SPAN = DAY;
/** Within this much of "now" a seek means "go live". */
export const LIVE_SNAP_MS = 5000;
/** Step size of the skip buttons and arrow keys. */
export const STEP_MS = 10e3;
/** Empty space kept right of "now" so the live edge isn't flush with the border. */
export const FUTURE_MARGIN = 0.04;

export const zoomLevels = [
  { label: "15m", ms: 15 * 60e3 },
  { label: "1h", ms: HOUR },
  { label: "6h", ms: 6 * HOUR },
  { label: "24h", ms: DAY },
];
export const rates = [0.5, 1, 2, 4, 8];

export interface View {
  start: number;
  end: number;
}

export const clampSpan = (ms: number) => Math.min(MAX_SPAN, Math.max(MIN_SPAN, ms));
const span = (v: View) => v.end - v.start;

/** The latest end a view may have: "now" plus the live-edge margin. */
export function maxViewEnd(now: number, spanMs: number): number {
  return now + spanMs * FUTURE_MARGIN;
}

/** The view that follows live: ends just after now. */
export function liveView(now: number, spanMs: number): View {
  const end = maxViewEnd(now, spanMs);
  return { start: end - spanMs, end };
}

/** A view of `spanMs` ending at `end`, not reaching into the future. */
export function viewEndingAt(end: number, spanMs: number, now: number): View {
  const e = Math.min(end, maxViewEnd(now, spanMs));
  return { start: e - spanMs, end: e };
}

/** A view that shows t at 80% of its width (so there is room ahead of the playhead). */
export function viewAround(t: number, spanMs: number, now: number): View {
  return viewEndingAt(t + spanMs * 0.2, spanMs, now);
}

/** Shift the view by deltaMs (negative = back in time), never into the future. */
export function panView(v: View, deltaMs: number, now: number): View {
  return viewEndingAt(v.end + deltaMs, span(v), now);
}

/** Change the span by `factor` (>1 zooms out) keeping the time under `anchor` where it was. */
export function zoomView(v: View, anchor: number, factor: number, now: number): View {
  const s = clampSpan(span(v) * factor);
  const f = span(v) === 0 ? 0.5 : (anchor - v.start) / span(v);
  const start = anchor - f * s;
  return viewEndingAt(start + s, s, now);
}

/** Does the view end at the live edge (so it should keep following now)? */
export function isFollowingNow(v: View, now: number): boolean {
  return v.end >= maxViewEnd(now, span(v)) - 1;
}

export const timeToFrac = (t: number, v: View) => (t - v.start) / span(v);
export const fracToTime = (f: number, v: View) => v.start + f * span(v);

/** Is t inside the middle of the view, clear of both edges? */
export function comfortablyInView(t: number, v: View, margin = 0.05): boolean {
  const f = timeToFrac(t, v);
  return f >= margin && f <= 1 - margin;
}

export interface DataWindow {
  from: number;
  to: number;
}

/** A 24 h window (the most the timeline API serves) around `center`, ending no later than now. */
export function windowAround(center: number, now: number): DataWindow {
  const to = Math.min(center + DAY / 2, now);
  return { from: to - DAY, to };
}

/** WAVE24(D2): a playlist covers the playhead plus or minus this much. */
export const PLAY_HALF_MS = HOUR;
/** ...and is replaced by the next window when the playhead comes this close to an end. */
export const PLAY_EDGE_MS = 10 * 60e3;

/**
 * The window a playlist (and the timeline whose media offsets it plays) is
 * built from: playhead plus or minus an hour, not reaching past now.
 */
export function playWindowAround(center: number, now: number): DataWindow {
  return { from: center - PLAY_HALF_MS, to: Math.min(center + PLAY_HALF_MS, now) };
}

/**
 * Should the playlist be rebuilt around t? True when t is outside the loaded
 * window or within 10 min of either end. The end only counts while the window
 * stops short of the present (a window that already reaches now has no later
 * footage to fetch; the engine's new-footage poll handles that).
 */
export function needsNextWindow(w: DataWindow, t: number, now: number, edge = PLAY_EDGE_MS): boolean {
  if (t < w.from || t >= w.to) return true;
  if (t < w.from + edge) return true;
  return t > w.to - edge && now - w.to > edge;
}

/** The window to fetch for a view. */
export function dataWindow(v: View, now: number): DataWindow {
  return windowAround((v.start + v.end) / 2, now);
}

/** Does the window cover everything of the view that can have footage (up to now)? */
export function windowCovers(w: DataWindow, v: View, now: number, slack = 60e3): boolean {
  return v.start >= w.from && Math.min(v.end, now) <= w.to + slack;
}

export const inWindow = (w: DataWindow, t: number) => t >= w.from && t < w.to;

/** Move t by deltaMs, never past now. */
export function stepTime(t: number, deltaMs: number, now: number): number {
  return Math.min(t + deltaMs, now);
}

/** Is a seek to t close enough to now to mean "live"? */
export function isLiveEdge(t: number, now: number): boolean {
  return t >= now - LIVE_SNAP_MS;
}

export interface MarkerCluster {
  at: number;
  count: number;
  alarm: boolean;
  critical: boolean;
  kind: string;
  label: string;
}

/**
 * Merge markers that would land within `minPx` of each other on a timeline
 * `widthPx` wide, so a busy hour stays legible. Alarms outrank person/vehicle
 * detections, which outrank plain motion.
 */
export function clusterMarkers(markers: Marker[], v: View, widthPx: number, minPx = 5): MarkerCluster[] {
  const msPerPx = span(v) / Math.max(1, widthPx);
  const rank = (m: Marker) => (m.alarm ? (m.severity === "Critical" ? 3 : 2) : m.kind.toLowerCase() === "motion" ? 0 : 1);
  const out: MarkerCluster[] = [];
  let anchor = -Infinity;
  let best: Marker | null = null;
  for (const m of markers) {
    if (m.at < v.start || m.at > v.end) continue;
    const cur = out[out.length - 1];
    if (cur && (m.at - anchor) / msPerPx < minPx) {
      cur.count++;
      if (best && rank(m) > rank(best)) {
        best = m;
        Object.assign(cur, { alarm: m.alarm, critical: m.alarm && m.severity === "Critical", kind: m.kind, label: m.label });
      }
    } else {
      anchor = m.at;
      best = m;
      out.push({ at: m.at, count: 1, alarm: m.alarm, critical: m.alarm && m.severity === "Critical", kind: m.kind, label: m.label });
    }
  }
  return out;
}

export interface ViewerRoute {
  /** First hash segment: live, playback, ... */
  page: string;
  camera: string | null;
  /** Playback time from a deep link, Unix ms. */
  t: number | null;
}

/** Parse `#/live`, `#/live/<camera>`, `#/playback/<camera>/<unix ms>`. */
export function parseViewerRoute(hash: string): ViewerRoute {
  const parts = hash.replace(/^#\/?/, "").split("?")[0].split("/");
  let camera: string | null = null;
  if (parts[1]) {
    try {
      camera = decodeURIComponent(parts[1]);
    } catch {
      camera = parts[1];
    }
  }
  const t = parts[2] && /^\d{9,}$/.test(parts[2]) ? Number(parts[2]) : null;
  return { page: parts[0] ?? "", camera, t };
}

export function viewerHash(page: "live" | "playback", camera: string | null, t?: number | null): string {
  if (!camera) return `#/${page}`;
  return `#/${page}/${encodeURIComponent(camera)}${t ? `/${Math.round(t)}` : ""}`;
}

const p2 = (n: number) => String(n).padStart(2, "0");

/** "Tue, 14 Oct · 09:41:07" style stamp. */
export function fmtStamp(t: number): string {
  const d = new Date(t);
  const date = d.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
  return `${date} · ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`;
}

// ---- recording lanes, gap/event skip, export range -----------------------------

/**
 * Split the visible cameras into the two recording lanes: the selected
 * camera's own spans (solid bands) and one merged "any recording" lane for
 * all the others.
 */
export function lanesFor(selectedId: string | null, ids: string[], timelines: Record<string, { spans: Range[] } | undefined>): { selected: Range[]; any: Range[] } {
  const selected = selectedId ? mergeSpans(timelines[selectedId]?.spans ?? []) : [];
  const any = mergeSpans(ids.filter((id) => id !== selectedId).flatMap((id) => timelines[id]?.spans ?? []));
  return { selected, any };
}

/** The camera whose lane is drawn: the expanded one, else the last clicked tile if still shown, else the first. */
export function selectedCamera(expanded: string | null, clicked: string | null, ids: string[]): string | null {
  if (expanded && ids.includes(expanded)) return expanded;
  if (clicked && ids.includes(clicked)) return clicked;
  return ids[0] ?? null;
}

export interface Skip {
  kind: "gap" | "event";
  at: number;
}

/**
 * One pair of buttons, two jobs: skip over the next gap in the selected
 * camera's footage, or, when an event comes before that gap, jump to the
 * event. Null when neither exists in that direction.
 */
export function skipTarget(spans: Range[], markers: { at: number }[], t: number, dir: 1 | -1): Skip | null {
  const g = dir > 0 ? nextGap(spans, t) : prevGap(spans, t);
  let e: number | null = null;
  if (dir > 0) e = markers.find((m) => m.at > t + 500)?.at ?? null;
  else {
    for (let i = markers.length - 1; i >= 0; i--) {
      if (markers[i].at < t - 4000) {
        e = markers[i].at;
        break;
      }
    }
  }
  if (g === null && e === null) return null;
  if (g === null) return { kind: "event", at: e! };
  if (e === null) return { kind: "gap", at: g };
  const gapFirst = dir > 0 ? g <= e : g >= e;
  return gapFirst ? { kind: "gap", at: g } : { kind: "event", at: e };
}

/** Can the in/out selection be exported for a camera with these spans? Empty string when it can. */
export function exportProblem(sel: Selection, spans: Range[], maxMs: number): string {
  const b = exportBlocker(sel, maxMs);
  if (b) return b;
  const r = selectionRange(sel)!;
  return spans.some((s) => s.start < r.end && s.end > r.start) ? "" : "Nothing was recorded in that range on this camera.";
}

/** "-2h 05m" / "-45s": how far behind live the playhead is. */
export function fmtBehind(t: number, now: number): string {
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `-${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `-${m}m ${p2(s % 60)}s`;
  return `-${Math.floor(m / 60)}h ${p2(m % 60)}m`;
}
