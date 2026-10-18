// Export options (WAVE8-DETECT-EVIDENCE.md, Stream D): a watermark and
// pixelated regions. Pure helpers shared by the export forms and the region
// picker; the server validates the same rules (internal/playback/regions.go)
// and composes the watermark text itself, so the client only says "on".
import type { Pt } from "../shared/zones";

/** A normalized rectangle of the frame (0-1, origin top left). */
export interface Region {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

export interface ExportOptions {
  watermark: boolean;
  blurRegions: Region[];
}

export const noOptions: ExportOptions = { watermark: false, blurRegions: [] };

export const maxRegions = 8;
export const minRegionSize = 0.01;

const round4 = (v: number) => Math.round(v * 1e4) / 1e4;

/** The rectangle spanned by two corner points, in any order. */
export function rectFrom(a: Pt, b: Pt): Region {
  return { x0: round4(Math.min(a[0], b[0])), y0: round4(Math.min(a[1], b[1])), x1: round4(Math.max(a[0], b[0])), y1: round4(Math.max(a[1], b[1])) };
}

/** Why a region can't be used, or null when it is fine. */
export function regionProblem(r: Region): string | null {
  for (const v of [r.x0, r.y0, r.x1, r.y1]) if (!Number.isFinite(v) || v < 0 || v > 1) return "The region is outside the picture.";
  if (r.x1 - r.x0 < minRegionSize || r.y1 - r.y0 < minRegionSize) return "The region is too small.";
  return null;
}

/** Adds a drawn region; a tiny drag (a click) or a ninth region is ignored. */
export function addRegion(list: Region[], r: Region): Region[] {
  if (list.length >= maxRegions || regionProblem(r)) return list;
  return [...list, r];
}

export const removeRegion = (list: Region[], index: number): Region[] => list.filter((_, i) => i !== index);

/** The fields to merge into an export request (nothing when no option is on). */
export function optionsBody(o: ExportOptions): { watermark?: true; blurRegions?: Region[] } {
  return { ...(o.watermark ? { watermark: true as const } : {}), ...(o.blurRegions.length ? { blurRegions: o.blurRegions } : {}) };
}

/** Whether the options make the server re-encode the clip. */
export const optionsReencode = (o: ExportOptions) => o.watermark || o.blurRegions.length > 0;

/** "watermark, 2 pixelated regions" ("" with no options). */
export function optionsSummary(o: ExportOptions): string {
  const parts: string[] = [];
  if (o.watermark) parts.push("watermark");
  const n = o.blurRegions.length;
  if (n) parts.push(`${n} pixelated region${n === 1 ? "" : "s"}`);
  return parts.join(", ");
}
