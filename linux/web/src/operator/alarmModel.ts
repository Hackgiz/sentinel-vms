// The Alarm Manager's filter bar, sorting and bulk actions, pure so they can
// be tested. States and severities are the server's strings (openapi.yaml).
import type { Alarm, AlarmQuery, AlarmState, Incident } from "../shared/api";

export const allStates: AlarmState[] = ["New", "Acknowledged", "Investigating", "Snoozed", "Resolved", "False Alarm"];
export const openStates: AlarmState[] = ["New", "Acknowledged", "Investigating", "Snoozed"];
export const severities = ["Critical", "Warning", "Info"] as const;
export type Severity = (typeof severities)[number];

export type AlarmRange = "any" | "1h" | "24h" | "7d" | "custom";
export const ranges: { id: AlarmRange; label: string; ms: number }[] = [
  { id: "any", label: "Any time", ms: 0 },
  { id: "1h", label: "Last hour", ms: 3600e3 },
  { id: "24h", label: "24 hours", ms: 24 * 3600e3 },
  { id: "7d", label: "7 days", ms: 7 * 24 * 3600e3 },
];

export interface AlarmFilter {
  states: AlarmState[];
  severities: string[];
  cameras: string[];
  /** "" = anyone, "unassigned", or a user name. */
  owner: string;
  range: AlarmRange;
  /** Used only when range is "custom" (Unix ms; 0 = open). */
  from: number;
  to: number;
  q: string;
}

export const emptyAlarmFilter: AlarmFilter = { states: [...openStates], severities: [], cameras: [], owner: "", range: "any", from: 0, to: 0, q: "" };

const list = (a: string[]) => (a.length ? a.join(",") : undefined);

/** The [from, to) window of a filter in Unix ms (0 = open). */
export function windowOf(f: AlarmFilter, now: number): { from: number; to: number } {
  if (f.range === "custom") return { from: f.from, to: f.to };
  const p = ranges.find((r) => r.id === f.range);
  return p && p.ms ? { from: now - p.ms, to: 0 } : { from: 0, to: 0 };
}

/** Everything but the state chips: the filters the per-state counts honor. */
export function toCountsQuery(f: AlarmFilter, now: number) {
  const { from, to } = windowOf(f, now);
  return { severities: list(f.severities), cameras: list(f.cameras), owner: f.owner || undefined, from: from || undefined, to: to || undefined, q: f.q.trim() || undefined };
}

/** Query parameters for GET /api/v2/alarms (no chips selected = every state). */
export function toAlarmQuery(f: AlarmFilter, now: number): AlarmQuery {
  return { ...toCountsQuery(f, now), states: f.states.length ? list(f.states) : undefined, state: f.states.length ? undefined : "all" };
}

/** How many filters besides the state chips are set. */
export function activeCount(f: AlarmFilter): number {
  return f.severities.length + f.cameras.length + (f.owner ? 1 : 0) + (f.range !== "any" ? 1 : 0) + (f.q.trim() ? 1 : 0);
}

export type SortKey = "severity" | "state" | "camera" | "owner" | "time" | "events";
export type SortDir = "asc" | "desc";
export interface Sort {
  key: SortKey;
  dir: SortDir;
}

const sevRank: Record<string, number> = { Critical: 0, Warning: 1, Info: 2 };
const stateRank = Object.fromEntries(allStates.map((s, i) => [s, i])) as Record<string, number>;

function compare(a: Alarm, b: Alarm, key: SortKey): number {
  switch (key) {
    case "severity":
      return (sevRank[a.severity] ?? 3) - (sevRank[b.severity] ?? 3);
    case "state":
      return (stateRank[a.state] ?? 9) - (stateRank[b.state] ?? 9);
    case "camera":
      return (a.cameraName ?? "").localeCompare(b.cameraName ?? "");
    case "owner":
      return a.owner.localeCompare(b.owner);
    case "events":
      return a.eventCount - b.eventCount;
    case "time":
      return a.createdAt - b.createdAt;
  }
}

/** Sorted copy; ties fall back to newest first so the order is stable. */
export function sortAlarms(alarms: Alarm[], s: Sort): Alarm[] {
  const sign = s.dir === "asc" ? 1 : -1;
  return [...alarms].sort((a, b) => sign * compare(a, b, s.key) || b.createdAt - a.createdAt || a.id.localeCompare(b.id));
}

/** Clicking a header: the same key flips the direction; a new key starts descending for time/events and ascending otherwise. */
export function nextSort(cur: Sort, key: SortKey): Sort {
  if (cur.key === key) return { key, dir: cur.dir === "asc" ? "desc" : "asc" };
  return { key, dir: key === "time" || key === "events" ? "desc" : "asc" };
}

export type BulkAction = "acknowledge" | "close";

/** The selected alarms a bulk action applies to, and the state it sets. */
export function bulkTargets(action: BulkAction, alarms: Alarm[], selected: Set<string>): { ids: string[]; state: AlarmState } {
  const eligible = (a: Alarm) => (action === "acknowledge" ? a.state === "New" || a.state === "Snoozed" : openStates.includes(a.state));
  return { ids: alarms.filter((a) => selected.has(a.id) && eligible(a)).map((a) => a.id), state: action === "acknowledge" ? "Acknowledged" : "Resolved" };
}

/** Toggle one id, or (shift-click) the range between the anchor and it, in table order. */
export function toggleSelection(selected: Set<string>, order: string[], id: string, anchor: string | null, range: boolean): Set<string> {
  const next = new Set(selected);
  if (range && anchor && anchor !== id) {
    const a = order.indexOf(anchor);
    const b = order.indexOf(id);
    if (a >= 0 && b >= 0) {
      const on = !selected.has(id);
      for (const x of order.slice(Math.min(a, b), Math.max(a, b) + 1)) {
        if (on) next.add(x);
        else next.delete(x);
      }
      return next;
    }
  }
  if (next.has(id)) next.delete(id);
  else next.add(id);
  return next;
}

/** Incidents an alarm can still be added to: not closed. */
export const pickableIncidents = (incidents: Incident[]) => incidents.filter((i) => i.status !== "closed");

export const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };
export const stateClass: Record<string, string> = { New: "red", Acknowledged: "amber", Investigating: "amber", Snoozed: "blue", Resolved: "green", "False Alarm": "blue" };
export const incidentSevClass: Record<string, string> = { critical: "red", high: "red", medium: "amber", low: "blue" };
export const incidentStatusClass: Record<string, string> = { open: "red", investigating: "amber", closed: "green" };
