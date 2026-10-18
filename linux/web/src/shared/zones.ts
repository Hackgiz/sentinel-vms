// Detection zones and privacy masks (WAVE7-FEATURES.md, Stream A): pure
// geometry shared by the admin editor and the video overlays. Polygons are
// vertex lists [x, y] normalized 0..1 against the full frame. The server
// validates the same rules (internal/store/zones.go); checking here just
// lets the editor explain a bad shape before saving.

export type Pt = [number, number];

export const minPoints = 3;
export const maxPoints = 20;
export const maxZones = 50;

/** Frame size assumed when a camera hasn't reported one (16:9). */
export const defaultFrame = { width: 1920, height: 1080 } as const;

const cross = (a: Pt, b: Pt, c: Pt) => (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0]);
const dot = (a: Pt, b: Pt, c: Pt) => (b[0] - a[0]) * (c[0] - a[0]) + (b[1] - a[1]) * (c[1] - a[1]);
const sgn = (v: number) => (v > 0 ? 1 : v < 0 ? -1 : 0);
const onSegment = (a: Pt, b: Pt, c: Pt) =>
  Math.min(a[0], b[0]) <= c[0] && c[0] <= Math.max(a[0], b[0]) && Math.min(a[1], b[1]) <= c[1] && c[1] <= Math.max(a[1], b[1]);

function segmentsTouch(a: Pt, b: Pt, c: Pt, d: Pt): boolean {
  const d1 = sgn(cross(a, b, c));
  const d2 = sgn(cross(a, b, d));
  const d3 = sgn(cross(c, d, a));
  const d4 = sgn(cross(c, d, b));
  if (d1 * d2 < 0 && d3 * d4 < 0) return true;
  return (d1 === 0 && onSegment(a, b, c)) || (d2 === 0 && onSegment(a, b, d)) || (d3 === 0 && onSegment(c, d, a)) || (d4 === 0 && onSegment(c, d, b));
}

/** Absolute shoelace area (normalized units). */
export function polygonArea(pts: Pt[]): number {
  let s = 0;
  for (let i = 0; i < pts.length; i++) {
    const j = (i + 1) % pts.length;
    s += pts[i][0] * pts[j][1] - pts[j][0] * pts[i][1];
  }
  return Math.abs(s) / 2;
}

/** Why a polygon can't be saved, or null when it is fine. */
export function polygonProblem(pts: Pt[]): string | null {
  const n = pts.length;
  if (n < minPoints || n > maxPoints) return `A zone needs ${minPoints} to ${maxPoints} points.`;
  for (const p of pts) if (!p.every((v) => Number.isFinite(v) && v >= 0 && v <= 1)) return "A point is outside the picture.";
  for (let i = 0; i < n; i++) {
    const q = pts[(i + 1) % n];
    if (pts[i][0] === q[0] && pts[i][1] === q[1]) return "Two neighbouring points are in the same place.";
  }
  for (let i = 0; i < n; i++) {
    const a = pts[i];
    const b = pts[(i + 1) % n];
    for (let j = i + 1; j < n; j++) {
      const c = pts[j];
      const d = pts[(j + 1) % n];
      if (j === i + 1 || (i === 0 && j === n - 1)) {
        // Neighbouring edges meet at one vertex; doubling back along each other is out.
        const [shared, far1, far2] = i === 0 && j === n - 1 ? [a, b, c] : [b, a, d];
        if (cross(shared, far1, far2) === 0 && dot(shared, far1, far2) > 0) return "The outline folds back on itself.";
        continue;
      }
      if (segmentsTouch(a, b, c, d)) return "The outline crosses itself.";
    }
  }
  if (polygonArea(pts) < 1e-6) return "The zone has no area.";
  return null;
}

export const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

/** A pointer position inside `rect` as a normalized point. */
export function toPoint(clientX: number, clientY: number, rect: { left: number; top: number; width: number; height: number }): Pt {
  return [clamp01((clientX - rect.left) / (rect.width || 1)), clamp01((clientY - rect.top) / (rect.height || 1))];
}

/** Whether a click at `p` should close the polygon being drawn (near its first vertex). */
export function closesPolygon(pts: Pt[], p: Pt, tolerance = 0.025): boolean {
  if (pts.length < minPoints) return false;
  return Math.hypot(p[0] - pts[0][0], p[1] - pts[0][1]) <= tolerance;
}

/** Rounds to 4 decimals so saved zones are stable across edits. */
export const roundPoint = (p: Pt): Pt => [Math.round(p[0] * 1e4) / 1e4, Math.round(p[1] * 1e4) / 1e4];

/** SVG `points` attribute for a polygon in a viewBox of width × height. */
export function svgPoints(pts: Pt[], width = 1, height = 1): string {
  return pts.map(([x, y]) => `${+(x * width).toFixed(2)},${+(y * height).toFixed(2)}`).join(" ");
}

/** Frame size to overlay masks on: the camera's reported stream size, else 16:9. */
export function frameSize(status?: { width?: number; height?: number }): { width: number; height: number } {
  const w = status?.width ?? 0;
  const h = status?.height ?? 0;
  return w > 0 && h > 0 ? { width: w, height: h } : { ...defaultFrame };
}

/** Whether exporting this camera's clips re-encodes (it has privacy masks). */
export const exportReencodes = (masks?: unknown[]) => (masks?.length ?? 0) > 0;
