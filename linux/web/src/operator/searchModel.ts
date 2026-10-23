// The forensic search filter bar's state and how it maps to the API.
import type { EventSearchFilter, EventSearchQuery } from "../shared/api";
import { normalizePlate } from "../admin/platesModel";

export type RangePreset = "1h" | "24h" | "7d" | "30d" | "all" | "custom";

export const presets: { id: RangePreset; label: string; ms: number }[] = [
  { id: "1h", label: "Last hour", ms: 3600e3 },
  { id: "24h", label: "24 hours", ms: 24 * 3600e3 },
  { id: "7d", label: "7 days", ms: 7 * 24 * 3600e3 },
  { id: "30d", label: "30 days", ms: 30 * 24 * 3600e3 },
  { id: "all", label: "Any time", ms: 0 },
];

export interface SearchState {
  range: RangePreset;
  /** Used only when range is "custom" (Unix ms; 0 = open). */
  from: number;
  to: number;
  cameras: string[];
  kinds: string[];
  labels: string[];
  q: string;
  /** "Elevated or high only". */
  threatOnly: boolean;
  /** Set by a natural-language question; shown as removable chips. */
  threats: string[];
  minScore: number;
  anomaly: boolean | undefined;
  /** Wave 16: plate text (normalized; a prefix matches). */
  plate: string;
  /** Wave 16: a personId (set from a person chip). */
  person: string;
  /** Wave 16: a trackId ("same object"). */
  track: string;
}

export const emptySearch: SearchState = { range: "24h", from: 0, to: 0, cameras: [], kinds: [], labels: [], q: "", threatOnly: false, threats: [], minScore: 0, anomaly: undefined, plate: "", person: "", track: "" };

/** The [from, to) window of a state, in Unix ms (0 = open). */
export function windowOf(s: SearchState, now: number): { from: number; to: number } {
  if (s.range === "custom") return { from: s.from, to: s.to };
  const p = presets.find((x) => x.id === s.range);
  return p && p.ms ? { from: now - p.ms, to: 0 } : { from: 0, to: 0 };
}

const list = (a: string[]) => (a.length ? a.join(",") : undefined);

/** Query parameters for GET /api/v2/events/search (and, minus paging, /facets). */
export function toQuery(s: SearchState, now: number): EventSearchQuery {
  const { from, to } = windowOf(s, now);
  const threats = s.threatOnly ? ["elevated", "high"] : s.threats;
  return {
    cameras: list(s.cameras),
    kinds: list(s.kinds),
    labels: list(s.labels),
    from: from || undefined,
    to: to || undefined,
    minScore: s.minScore > 0 ? s.minScore : undefined,
    threats: list(threats),
    anomaly: s.anomaly,
    q: s.q.trim() || undefined,
    plate: normalizePlate(s.plate) || undefined,
    person: s.person || undefined,
    track: s.track || undefined,
  };
}

/** The filter bar state for the filter the server understood a question as. */
export function fromFilter(f: EventSearchFilter): SearchState {
  const open = !f.from && !f.to;
  const elevated = f.threats?.length === 2 && f.threats.includes("elevated") && f.threats.includes("high");
  return {
    range: open ? "all" : "custom",
    from: f.from ?? 0,
    to: f.to ?? 0,
    cameras: f.cameras ?? [],
    kinds: f.kinds ?? [],
    labels: f.labels ?? [],
    q: f.q ?? "",
    threatOnly: elevated,
    threats: elevated ? [] : (f.threats ?? []),
    minScore: f.minScore ?? 0,
    anomaly: f.anomaly,
    plate: f.plate ?? "",
    person: f.person ?? "",
    track: f.track ?? "",
  };
}

/** How many filters (besides the time range) are set. */
export function activeCount(s: SearchState): number {
  return s.cameras.length + s.kinds.length + s.labels.length + (s.q.trim() ? 1 : 0) + (s.threatOnly ? 1 : 0) + s.threats.length + (s.minScore > 0 ? 1 : 0) + (s.anomaly === undefined ? 0 : 1) + (normalizePlate(s.plate) ? 1 : 0) + (s.person ? 1 : 0) + (s.track ? 1 : 0);
}
