// Pure helpers for the Exports workspace, so they can be tested.

import type { EvidenceCase } from "../shared/api";
import { maxExportMs } from "./export";

/** A range handed from Playback to the Exports workspace through the data context. */
export interface ExportDraft {
  cameraId: string;
  from: number;
  to: number;
}

/** datetime-local value of a Unix-ms time ("" when unset). */
export function toLocalInput(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60e3);
  return d.toISOString().slice(0, 16);
}

/** Unix ms of a datetime-local value (0 when empty or invalid). */
export function fromLocalInput(v: string): number {
  const t = v ? new Date(v).getTime() : 0;
  return Number.isFinite(t) ? t : 0;
}

/** Why the form can't be submitted, or "" when it can. */
export function exportBlocker(cameraIds: string[], from: number, to: number, now = Date.now()): string {
  if (!cameraIds.length) return "Pick at least one camera.";
  if (!from || !to) return "Pick a start and an end.";
  if (to <= from) return "The end must be after the start.";
  if (from > now) return "The start is in the future.";
  if (to - from > maxExportMs) return `Exports are limited to ${Math.round(maxExportMs / 60e3)} minutes.`;
  return "";
}

/** Shared label for a multi-camera selection ("" for a single camera). */
export function batchLabel(count: number, at: number): string {
  if (count < 2) return "";
  const d = new Date(at);
  const p = (n: number) => String(n).padStart(2, "0");
  return `Batch ${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())} (${count} cameras)`;
}

/** The note stored on each case of a batch: the shared label, then the operator's words. */
export function composeNote(label: string, note: string): string {
  return [label, note.trim()].filter(Boolean).join(" · ");
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** The footage range of a case: exact when recorded, else the server's label (alarm locks). */
export function rangeText(c: EvidenceCase, fmt: (ms: number) => string = (ms) => new Date(ms).toLocaleString()): string {
  return c.fromMs && c.toMs ? `${fmt(c.fromMs)} – ${new Date(c.toMs).toLocaleTimeString()}` : c.range;
}
