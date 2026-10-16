// Shared colouring for the System Monitor: one place turns a percentage and
// the site's thresholds into a level and a colour.
import type { MonitorThresholds } from "./systemApi";

export type Threshold = { amber: number; red: number };
export type Level = "ok" | "amber" | "red";

export const DEFAULT_THRESHOLDS: MonitorThresholds = {
  cpu: { amber: 80, red: 95 },
  mem: { amber: 80, red: 95 },
  disk: { amber: 80, red: 95 },
};

/** ok below amber; amber from the amber mark; red from the red mark. */
export function levelFor(pct: number | null | undefined, t: Threshold): Level {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return "ok";
  if (pct >= t.red) return "red";
  if (pct >= t.amber) return "amber";
  return "ok";
}

/** The CSS colour for a level; undefined keeps the meter's default accent. */
export function levelColor(level: Level): string | undefined {
  return level === "red" ? "var(--red)" : level === "amber" ? "var(--amber)" : undefined;
}

/** Amber must sit below red, both within 1-100 (what the server accepts). */
export function thresholdError(t: MonitorThresholds): string | null {
  for (const [name, v] of Object.entries(t) as [string, Threshold][]) {
    if (!Number.isInteger(v.amber) || !Number.isInteger(v.red) || v.amber < 1 || v.red > 100 || v.amber >= v.red) {
      return `${name.toUpperCase()}: amber and red must be whole percentages from 1 to 100, with amber below red.`;
    }
  }
  return null;
}
