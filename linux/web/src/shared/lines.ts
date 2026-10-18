// Line crossing tripwires (WAVE8-DETECT-EVIDENCE.md, Stream A): pure geometry
// for the zones editor. A line is exactly two points p0 -> p1, normalized
// 0..1 against the full frame. The server validates the same rules
// (internal/store/zones_lines.go) and does the real crossing detection
// (internal/detect/lines.go); this only lets the editor explain a bad line
// and draw which way "left to right" points.

import type { Pt } from "./zones";

export type LineDirection = "ltr" | "rtl" | "both";

export const linePoints = 2;
/** Shortest line the server accepts (normalized units). */
export const minLineLength = 0.01;

export const lineDirections: { value: LineDirection; label: string }[] = [
  { value: "both", label: "Either way" },
  { value: "ltr", label: "Left to right" },
  { value: "rtl", label: "Right to left" },
];

export const isLineDirection = (v: unknown): v is LineDirection => v === "ltr" || v === "rtl" || v === "both";

/** The direction stored in a line's meta; damaged or missing meta watches both ways, as the server does. */
export function directionOf(meta: unknown): LineDirection {
  const d = (meta as { direction?: unknown } | null | undefined)?.direction;
  return isLineDirection(d) ? d : "both";
}

/** Wave 9: the alarm severity a crossing raises; "Info" logs the event only. */
export type LineSeverity = "Critical" | "Warning" | "Info";

export const lineSeverities: { value: LineSeverity; label: string }[] = [
  { value: "Critical", label: "Critical alarm" },
  { value: "Warning", label: "Warning alarm" },
  { value: "Info", label: "Event only" },
];

export const isLineSeverity = (v: unknown): v is LineSeverity => v === "Critical" || v === "Warning" || v === "Info";

/** The severity stored in a line's meta; damaged or missing meta is Critical, as the server does. */
export function severityOf(meta: unknown): LineSeverity {
  const s = (meta as { severity?: unknown } | null | undefined)?.severity;
  return isLineSeverity(s) ? s : "Critical";
}

/** Why a line can't be saved, or null when it is fine. */
export function lineProblem(pts: Pt[]): string | null {
  if (pts.length !== linePoints) return `A line needs exactly ${linePoints} points.`;
  for (const p of pts) if (!p.every((v) => Number.isFinite(v) && v >= 0 && v <= 1)) return "A point is outside the picture.";
  if (pts[0][0] === pts[1][0] && pts[0][1] === pts[1][1]) return "The two points of a line are in the same place.";
  if (Math.hypot(pts[0][0] - pts[1][0], pts[0][1] - pts[1][1]) < minLineLength) return "The line is too short to cross.";
  return null;
}

/**
 * Which side of p0 -> p1 a point is on: positive = the right-hand side
 * ("ltr" crossings end here), negative = the left, as seen standing at p0
 * looking at p1 on the picture (y grows downward). Mirrors the server.
 */
export const sideOf = (p0: Pt, p1: Pt, p: Pt): number => (p1[0] - p0[0]) * (p[1] - p0[1]) - (p1[1] - p0[1]) * (p[0] - p0[0]);

export interface LineArrow {
  /** Arrow tail and head for one counted direction, in the same normalized space. */
  from: Pt;
  to: Pt;
}

/**
 * The arrows showing which way crossings count, drawn from the line's middle
 * perpendicular to it. "ltr" points toward the right-hand side (the side the
 * crossing ends on), "rtl" toward the left, "both" gets one each way.
 * `length` is in normalized units; `aspect` (width / height of the picture)
 * keeps the arrow perpendicular on screen rather than in normalized space.
 */
export function directionArrows(p0: Pt, p1: Pt, direction: LineDirection, length = 0.09, aspect = 16 / 9): LineArrow[] {
  // Work in screen-proportional space (x scaled by aspect), then scale back.
  const dx = (p1[0] - p0[0]) * aspect;
  const dy = p1[1] - p0[1];
  const len = Math.hypot(dx, dy);
  if (len === 0) return [];
  // Right-hand normal in y-down screen space is (-dy, dx).
  const nx = -dy / len;
  const ny = dx / len;
  const mid: Pt = [(p0[0] + p1[0]) / 2, (p0[1] + p1[1]) / 2];
  const toward = (sign: number): LineArrow => ({
    from: mid,
    to: [mid[0] + (sign * nx * length) / aspect, mid[1] + sign * ny * length],
  });
  if (direction === "ltr") return [toward(1)];
  if (direction === "rtl") return [toward(-1)];
  return [toward(1), toward(-1)];
}

/** SVG polygon points for an arrowhead at `to`, for an arrow coming from `from`. */
export function arrowHead(from: Pt, to: Pt, size = 0.025, aspect = 16 / 9): string {
  const dx = (to[0] - from[0]) * aspect;
  const dy = to[1] - from[1];
  const len = Math.hypot(dx, dy);
  if (len === 0) return "";
  const ux = dx / len;
  const uy = dy / len;
  // Base corners: back along the arrow, +/- half a size across it (screen space).
  const corner = (side: number): Pt => [to[0] + (-ux * size - uy * side * (size / 2)) / aspect, to[1] + -uy * size + ux * side * (size / 2)];
  const a = corner(1);
  const b = corner(-1);
  const f = (p: Pt) => `${p[0].toFixed(4)},${p[1].toFixed(4)}`;
  return `${f(to)} ${f(a)} ${f(b)}`;
}
