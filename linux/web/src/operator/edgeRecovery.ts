// Edge recording recovery (Wave 9, stream C), kept pure for tests: which
// stretch "Recover from camera" would fetch, the recovered spans the
// timeline hatches, and the job's one-line state. Times are Unix ms.

import type { EdgeRecovery } from "../shared/api";
import { mergeSpans, type Range } from "./timelineMath";

/** Longest range one recovery may cover, as the server's link.MaxEdgeRecoveryMs. */
export const maxEdgeRecoveryMs = 24 * 3600e3;
/** Holes shorter than this are keyframe slack between segments, not gaps (the server's edgeMinGap). */
export const minEdgeGapMs = 10e3;

/** The stretches made of recovered footage (spans flagged `edge`), merged: the bar hatches these. */
export function edgeRanges(spans: ReadonlyArray<Range & { edge?: boolean }>): Range[] {
  return mergeSpans(spans.filter((s) => s.edge));
}

/** How much of `r` no span covers. */
export function uncoveredMs(spans: Range[], r: Range): number {
  let covered = 0;
  for (const s of mergeSpans(spans, 0)) {
    const a = Math.max(s.start, r.start);
    const b = Math.min(s.end, r.end);
    if (b > a) covered += b - a;
  }
  return Math.max(0, r.end - r.start - covered);
}

/**
 * The hole around `t`: from the end of the footage before it to the start
 * of the footage after it, within `bounds` and not past `now`. Null when
 * `t` sits on footage or the hole is shorter than minEdgeGapMs.
 */
export function gapAt(spans: Range[], t: number, bounds: Range, now: number): Range | null {
  const end0 = Math.min(bounds.end, now);
  if (t < bounds.start || t >= end0) return null;
  let start = bounds.start;
  let end = end0;
  for (const s of mergeSpans(spans)) {
    if (t >= s.start && t < s.end) return null;
    if (s.end <= t) start = Math.max(start, s.end);
    else if (s.start > t) {
      end = Math.min(end, s.start);
      break;
    }
  }
  return end - start >= minEdgeGapMs ? { start, end } : null;
}

/**
 * What "Recover from camera" fetches: the in/out selection when part of it
 * is unrecorded, else the gap under the playhead. Clamped to now and to
 * the 24 h a recovery may cover (keeping the start). Null when there is
 * nothing to recover.
 */
export function recoverTarget(sel: Range | null, spans: Range[], playhead: number | null, bounds: Range, now: number): Range | null {
  let r: Range | null = null;
  if (sel) {
    const s = { start: sel.start, end: Math.min(sel.end, now) };
    if (s.end > s.start && uncoveredMs(spans, s) >= minEdgeGapMs) r = s;
  }
  if (!r && playhead !== null) r = gapAt(spans, playhead, bounds, now);
  if (!r) return null;
  return { start: r.start, end: Math.min(r.end, r.start + maxEdgeRecoveryMs) };
}

/** The button's tooltip: what will be recovered. */
export function recoverTitle(target: Range | null): string {
  if (!target) return "Select a gap (put the playhead in it, or mark in/out around it) to recover it from the camera's SD card.";
  const min = Math.max(1, Math.round((target.end - target.start) / 60e3));
  return `Pull the camera's own SD-card footage into the gaps of this ${min >= 60 ? `${Math.round((min / 60) * 10) / 10} h` : `${min} min`} range.`;
}

/** Whether a job is still being worked on (and so should be polled). */
export function edgeJobActive(j: Pick<EdgeRecovery, "state"> | null): boolean {
  return !!j && j.state === "running";
}

const footage = (s: number) => (s >= 3600 ? `${Math.round((s / 3600) * 10) / 10} h` : s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`);

/** One line for a job's state. */
export function edgeJobText(j: EdgeRecovery): string {
  switch (j.state) {
    case "running":
      return j.pieces ? `Recovering from camera… ${j.progress}%` : "Asking the camera what it recorded…";
    case "done":
      if (!j.gaps) return "No gaps in that range: nothing to recover.";
      if (!j.recovered) return "The camera had no footage for those gaps.";
      return `Recovered ${footage(j.seconds)} from the camera (${j.recovered} segment${j.recovered === 1 ? "" : "s"}).${j.error ? ` ${j.error}` : ""}`;
    case "canceled":
      return "Recovery canceled.";
    default:
      return `Recovery failed: ${j.error ?? "unknown error"}`;
  }
}
