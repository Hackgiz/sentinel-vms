// Pure helpers for the long-export picker and the scheduled-export tab
// (Wave 7, stream C), so they can be tested.

import type { ExportJob, ExportSchedule } from "../shared/api";

/** Longest long export, as the server's playback.MaxLongExport. */
export const maxLongExportMs = 12 * 3600e3;

/** Why a long export can't be started, or "" when it can. */
export function longExportBlocker(cameraId: string, from: number, to: number, now = Date.now()): string {
  if (!cameraId) return "Pick a camera.";
  if (!from || !to) return "Pick a start and an end.";
  if (to <= from) return "The end must be after the start.";
  if (from > now) return "The start is in the future.";
  if (to - from > maxLongExportMs) return `Long exports are limited to ${Math.round(maxLongExportMs / 3600e3)} hours.`;
  return "";
}

/** "5 h 30 min", "45 min": how much footage the range spans. */
export function spanText(ms: number): string {
  const min = Math.max(0, Math.round(ms / 60e3));
  const h = Math.floor(min / 60);
  const m = min % 60;
  if (!h) return `${m} min`;
  return m ? `${h} h ${m} min` : `${h} h`;
}

/** Whether a job is still being worked on (and so should be polled). */
export function jobActive(j: Pick<ExportJob, "state"> | null): boolean {
  return !!j && j.state === "running";
}

/** One line for a job's state. */
export function jobText(j: ExportJob): string {
  switch (j.state) {
    case "running":
      return `Exporting… ${j.progress}%`;
    case "done":
      return j.dest === "evidence" ? `Filed as evidence${j.caseId ? ` ${j.caseId}` : ""}.` : "Ready to download.";
    case "canceled":
      return "Canceled.";
    default:
      return `Failed: ${j.error ?? "unknown error"}`;
  }
}

export const dayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

/** "Every day", "Weekdays", "Mon, Wed, Fri". */
export function daysText(days: number[]): string {
  const set = [...new Set(days)].sort((a, b) => a - b);
  if (set.length === 7) return "Every day";
  if (set.join() === "1,2,3,4,5") return "Weekdays";
  if (set.join() === "0,6") return "Weekends";
  return set.map((d) => dayNames[d] ?? "?").join(", ");
}

/** Minutes after midnight of "HH:MM" ("24:00" allowed), or null. */
export function clockMinutes(v: string, allow24 = false): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(v);
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (min > 59 || h > 24 || (h === 24 && (min !== 0 || !allow24))) return null;
  return h * 60 + min;
}

/** Window length in minutes (an end at or before the start runs overnight), or null if invalid. */
export function windowMinutes(start: string, end: string): number | null {
  const s = clockMinutes(start);
  const e = clockMinutes(end, true);
  if (s === null || e === null || s === e) return null;
  return e > s ? e - s : e + 1440 - s;
}

export interface ScheduleDraft {
  name: string;
  cameraId: string;
  days: number[];
  start: string;
  end: string;
  enabled: boolean;
}

/** Why a schedule can't be saved, or "" when it can (the server checks again). */
export function scheduleBlocker(d: ScheduleDraft): string {
  if (!d.name.trim()) return "Give the schedule a name.";
  if (!d.cameraId) return "Pick a camera.";
  if (!d.days.length) return "Pick at least one day.";
  const len = windowMinutes(d.start, d.end);
  if (len === null) return "Enter a start and an end time that differ.";
  if (len > maxLongExportMs / 60e3) return "A window can cover at most 12 hours.";
  return "";
}

/** "Mon, Wed · 22:00 → 06:00 (next day)". */
export function scheduleText(s: Pick<ExportSchedule, "days" | "start" | "end">): string {
  const a = clockMinutes(s.start);
  const b = clockMinutes(s.end, true);
  const overnight = a !== null && b !== null && b <= a;
  return `${daysText(s.days)} · ${s.start} → ${s.end}${overnight ? " (next day)" : ""}`;
}

/** The last run, in words: "Never", "OK — Oct 9, 06:00", "Failed — nothing was recorded in the window". */
export function lastRunText(s: Pick<ExportSchedule, "lastRunAt" | "lastStatus">, fmt: (ms: number) => string = (ms) => new Date(ms).toLocaleString()): string {
  if (!s.lastRunAt) return "Never";
  const when = fmt(s.lastRunAt);
  if (s.lastStatus === "ok") return `OK — ${when}`;
  if (s.lastStatus === "running") return `Running — ${when}`;
  if (s.lastStatus.startsWith("failed:")) return `Failed — ${s.lastStatus.slice(7).trim() || "see the server log"}`;
  return when;
}

export function blankDraft(cameraId = ""): ScheduleDraft {
  return { name: "", cameraId, days: [0, 1, 2, 3, 4, 5, 6], start: "22:00", end: "06:00", enabled: true };
}

export function draftOf(s: ExportSchedule): ScheduleDraft {
  return { name: s.name, cameraId: s.cameraId, days: [...s.days], start: s.start, end: s.end, enabled: s.enabled };
}
