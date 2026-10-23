// The playback timeline bar's math, kept pure: the visible window (centered
// on the playhead, 1 min to 24 h wide), ruler ticks, pan and zoom mapping,
// navigation between recordings, and the in/out export selection.
// Times are Unix ms.

export const minSpanMs = 60e3;
export const maxSpanMs = 24 * 3600e3;

/** Speeds offered in the transport's speed menu. */
export const speeds = [0.25, 0.5, 1, 2, 4, 8, 16, 32];

export const clampSpan = (ms: number) => Math.min(maxSpanMs, Math.max(minSpanMs, ms));

/** Multiply the visible span; factor > 1 zooms out. Always within 1 min – 24 h. */
export function zoomSpan(span: number, factor: number): number {
  return clampSpan(span * factor);
}

/** The zoom factor for a wheel event (deltaY > 0 zooms out). Pinch arrives as ctrl+wheel and is stronger. */
export function wheelFactor(deltaY: number, pinch = false): number {
  const k = pinch ? 0.01 : 0.0015;
  return Math.exp(Math.max(-200, Math.min(200, deltaY)) * k);
}

/** The window shown when the playhead sits at the center. */
export function viewRange(center: number, span: number): { start: number; end: number } {
  return { start: center - span / 2, end: center + span / 2 };
}

export function timeToX(t: number, start: number, span: number, width: number): number {
  return ((t - start) / span) * width;
}

export function xToTime(x: number, start: number, span: number, width: number): number {
  return start + (x / Math.max(1, width)) * span;
}

/** Dragging the ruler by dx pixels to the right moves the time under the pointer: the playhead goes back. */
export function panCenter(center: number, dx: number, span: number, width: number): number {
  return center - (dx / Math.max(1, width)) * span;
}

const second = 1000;
const minute = 60 * second;
const hour = 60 * minute;
/** Tick spacings, from seconds to hours. */
export const tickSteps = [1, 2, 5, 10, 15, 30].map((s) => s * second).concat([1, 2, 5, 10, 15, 30].map((m) => m * minute), [1, 2, 3, 6, 12].map((h) => h * hour));

/** The smallest tick spacing that keeps ticks at least `minPx` apart. */
export function tickStep(span: number, width: number, minPx = 90): number {
  const want = (minPx / Math.max(1, width)) * span;
  return tickSteps.find((s) => s >= want) ?? tickSteps[tickSteps.length - 1];
}

export interface Tick {
  t: number;
  /** Aligned to a larger unit (the minute, hour or midnight). */
  major: boolean;
  label: string;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** Ticks in [start, end], aligned to local time, labelled hh:mm:ss, hh:mm or the date at midnight. */
export function ticks(start: number, end: number, step: number): Tick[] {
  const out: Tick[] = [];
  const offset = (t: number) => new Date(t).getTimezoneOffset() * minute;
  // Align in local time: shift by the zone offset, floor to the step, shift back.
  let t = Math.ceil((start - offset(start)) / step) * step + offset(start);
  for (let guard = 0; t <= end && guard < 2000; guard++, t += step) {
    const d = new Date(t);
    const atMidnight = d.getHours() === 0 && d.getMinutes() === 0 && d.getSeconds() === 0;
    let label: string;
    if (atMidnight && step >= minute) label = `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
    else if (step < minute) label = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
    else label = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    const major = step < minute ? d.getSeconds() === 0 : step < hour ? d.getMinutes() === 0 : atMidnight;
    out.push({ t, major, label });
  }
  return out;
}

export interface Range {
  start: number;
  end: number;
  /** Wave 22: "low" = the 24/7 sub-stream of a dual-mode camera. Absent = full. */
  quality?: "full" | "low";
}

/** Merge touching or overlapping ranges (within `tolerance` ms), ignoring quality, oldest first. */
function mergeRaw(spans: Range[], tolerance: number): Range[] {
  const sorted = spans.filter((s) => s.end > s.start).map((s) => ({ start: s.start, end: s.end })).sort((a, b) => a.start - b.start);
  const out: Range[] = [];
  for (const s of sorted) {
    const last = out[out.length - 1];
    if (last && s.start <= last.end + tolerance) last.end = Math.max(last.end, s.end);
    else out.push(s);
  }
  return out;
}

/** `low` minus every `full` range (both sorted and disjoint). */
function subtract(low: Range[], full: Range[]): Range[] {
  const out: Range[] = [];
  for (const l of low) {
    let a = l.start;
    for (const f of full) {
      if (f.end <= a) continue;
      if (f.start >= l.end) break;
      if (f.start > a) out.push({ start: a, end: f.start, quality: "low" });
      a = Math.max(a, f.end);
      if (a >= l.end) break;
    }
    if (a < l.end) out.push({ start: a, end: l.end, quality: "low" });
  }
  return out;
}

/**
 * Merge touching or overlapping spans (within `tolerance` ms) into recording
 * stretches, oldest first. Full-res and low-res never merge into each other:
 * full wins where both exist, and low-res keeps `quality: "low"` on what is
 * left of it (the timeline draws it lighter). Full-res ranges carry no
 * `quality`, as before.
 */
export function mergeSpans(spans: Range[], tolerance = 1000): Range[] {
  const full = mergeRaw(spans.filter((s) => s.quality !== "low"), tolerance);
  const lowSpans = spans.filter((s) => s.quality === "low");
  if (!lowSpans.length) return full;
  const low = subtract(mergeRaw(lowSpans, tolerance), full);
  return [...full, ...low].sort((a, b) => a.start - b.start);
}

/** Where anything is recorded, whatever the quality: for skipping gaps. */
export function mergeCoverage(spans: Range[], tolerance = 1000): Range[] {
  return mergeRaw(spans, tolerance);
}

/** True when any of the spans is low-res (the timeline then shows its legend). */
export const hasLowRes = (spans: ReadonlyArray<Range>) => spans.some((s) => s.quality === "low");

/**
 * "Next gap »|": skip the next stretch with no recording and land on the
 * start of the footage after it. When the playhead is already in a gap,
 * that is the next recording. Null when nothing is recorded later.
 * A switch between low-res and full-res is not a gap.
 */
export function nextGap(spans: Range[], t: number): number | null {
  for (const s of mergeCoverage(spans)) if (s.start > t) return s.start;
  return null;
}

/**
 * "|« prev gap": mirror of nextGap. Skip back over the gap before the
 * current recording and land just before the footage that precedes it.
 */
export function prevGap(spans: Range[], t: number, lead = 1000): number | null {
  let best: number | null = null;
  for (const s of mergeCoverage(spans)) {
    if (s.end <= t - lead && !(t >= s.start && t < s.end)) best = Math.max(best ?? -Infinity, s.end - lead);
  }
  return best;
}

/** Export selection: in and out points marked on the ruler. */
export interface Selection {
  inAt: number | null;
  outAt: number | null;
}

export const emptySelection: Selection = { inAt: null, outAt: null };

/** Mark the in point; an out point at or before it is dropped. */
export function setIn(sel: Selection, t: number): Selection {
  return { inAt: t, outAt: sel.outAt !== null && sel.outAt <= t ? null : sel.outAt };
}

/** Mark the out point; an in point at or after it is dropped. */
export function setOut(sel: Selection, t: number): Selection {
  return { inAt: sel.inAt !== null && sel.inAt >= t ? null : sel.inAt, outAt: t };
}

/** The selected range, or null until both points are marked. */
export function selectionRange(sel: Selection): Range | null {
  return sel.inAt !== null && sel.outAt !== null && sel.outAt > sel.inAt ? { start: sel.inAt, end: sel.outAt } : null;
}

/** Why the selection can't be exported, or "" when it can. */
export function exportBlocker(sel: Selection, maxMs: number): string {
  const r = selectionRange(sel);
  if (!r) return "Mark an in and an out point first.";
  if (r.end - r.start > maxMs) return `Exports are limited to ${Math.round(maxMs / 60e3)} minutes.`;
  return "";
}
