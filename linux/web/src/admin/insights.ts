// The arithmetic behind Sentinel Admin's Insights page (WAVE14(C)), kept pure
// so it is tested without a server: the range query, the daily bars stacked
// by alarm kind, share formatting, and what each recommendation's button does
// for a user with a given set of permissions (from /api/v2/me).

import { ApiError, errorMessage, type AlertOn, type ApiClient, type Permission, type Schemas } from "../shared/api";
import { ruleKinds } from "../rulesApi";
import { v2 } from "./client";
import { sectionHash } from "./navModel";

export type Insights = Schemas["Insights"];
export type InsightsDay = Insights["daily"][number];
export type Recommendation = Schemas["Recommendation"];
export type RecommendationAction = Schemas["RecommendationAction"];

const DAY = 24 * 3600_000;

/** The ranges the page offers. Seven days is the default, as on the server. */
export const RANGES = [
  { id: "24h", label: "24 h", ms: DAY },
  { id: "7d", label: "7 days", ms: 7 * DAY },
  { id: "30d", label: "30 days", ms: 30 * DAY },
] as const;
export type RangeId = (typeof RANGES)[number]["id"];
export const DEFAULT_RANGE: RangeId = "7d";

/** GET /api/v2/insights query for a range ending now (Unix ms). */
export function rangeQuery(id: RangeId, now: number): { from: number; to: number } {
  const r = RANGES.find((x) => x.id === id) ?? RANGES.find((x) => x.id === DEFAULT_RANGE)!;
  return { from: now - r.ms, to: now };
}

/**
 * GET /api/v2/insights for a range ending `now`. The Admin's cookie client by
 * default; the Operator passes its bearer client (WAVE15).
 */
export function loadInsights(id: RangeId, now: number, client: ApiClient = v2): Promise<Insights> {
  return client.call("get", "/api/v2/insights", { query: rangeQuery(id, now) });
}

/** Nothing to show yet: no alarms and no events in the range. */
export const isEmpty = (ins: Insights): boolean => ins.totals.alarms === 0 && ins.totals.events === 0;

// ------------------------------------------------------------ daily bars ---

/** Kinds that get their own colour in the daily bars; the rest are summed as Other. */
export const TOP_KINDS = 4;
export const OTHER = "Other";

export interface Segment {
  kind: string;
  count: number;
}
export interface DayBar {
  /** Local server date, YYYY-MM-DD. */
  day: string;
  events: number;
  /** Height of the bar: the day's alarms. */
  total: number;
  /** Bottom to top, in `kinds` order; only kinds with alarms that day. */
  segments: Segment[];
}

// Rule-kind order, so a kind keeps its slot (and colour) when the range changes.
const kindRank = (k: string) => {
  const i = ruleKinds.indexOf(k);
  return i < 0 ? ruleKinds.length : i;
};
const byKindOrder = (a: string, b: string) => kindRank(a) - kindRank(b) || a.localeCompare(b);

/**
 * The daily bars: the range's `top` busiest alarm kinds each get a segment and
 * everything else is Other. A day's alarms that byKind doesn't account for
 * count as Other, so a bar is never shorter than the day's alarm count.
 */
export function stackDaily(daily: InsightsDay[], top = TOP_KINDS): { kinds: string[]; bars: DayBar[] } {
  const totals = new Map<string, number>();
  for (const d of daily) for (const [k, n] of Object.entries(d.byKind ?? {})) if (k && n > 0) totals.set(k, (totals.get(k) ?? 0) + n);
  const picked = [...totals.entries()]
    .sort((a, b) => b[1] - a[1] || byKindOrder(a[0], b[0]))
    .slice(0, top)
    .map(([k]) => k)
    .sort(byKindOrder);
  const counts = daily.map((d) => {
    const m = new Map<string, number>();
    let sum = 0;
    for (const [k, n] of Object.entries(d.byKind ?? {})) {
      if (!(n > 0)) continue;
      const key = picked.includes(k) ? k : OTHER;
      m.set(key, (m.get(key) ?? 0) + n);
      sum += n;
    }
    if (d.alarms > sum) m.set(OTHER, (m.get(OTHER) ?? 0) + d.alarms - sum);
    return m;
  });
  const kinds = counts.some((m) => m.has(OTHER)) ? [...picked, OTHER] : picked;
  const bars = daily.map((d, i) => {
    const segments = kinds.filter((k) => counts[i].has(k)).map((kind) => ({ kind, count: counts[i].get(kind)! }));
    return { day: d.day, events: d.events, total: segments.reduce((s, x) => s + x.count, 0), segments };
  });
  return { kinds, bars };
}

// ------------------------------------------------------------ formatting ---

/** A kind's share of the alarms as a whole percent: "<1%" for a sliver, ">99%" for nearly all. */
export function formatShare(share: number): string {
  if (!(share > 0)) return "0%";
  if (share >= 1) return "100%";
  if (share < 0.01) return "<1%";
  if (share > 0.99) return ">99%";
  return `${Math.round(share * 100)}%`;
}

/** An alarm kind as shown ("audio" -> "Audio"); an empty kind is Other. */
export const kindLabel = (kind: string): string => (kind ? kind.charAt(0).toUpperCase() + kind.slice(1) : OTHER);

export const alertOnLabel: Record<AlertOn, string> = {
  auto: "Automatic",
  motion: "Any motion",
  peopleAndVehicles: "People & vehicles",
  people: "People only",
};

/** "2026-10-14" -> "Wed, Oct 14". The day is already the server's local date, so no time zone applies. */
export function dayLabel(day: string, locale?: string, opts: Intl.DateTimeFormatOptions = { weekday: "short", month: "short", day: "numeric" }): string {
  const [y, m, d] = day.split("-").map(Number);
  if (!y || !m || !d) return day;
  return new Date(y, m - 1, d).toLocaleDateString(locale, opts);
}

const count = (n: number, word: string, locale?: string) => `${n.toLocaleString(locale)} ${word}${n === 1 ? "" : "s"}`;

/** What a screen reader and the hover title say for one day's bar. */
export function barLabel(b: DayBar, locale?: string): string {
  const parts = b.segments.map((s) => `${kindLabel(s.kind)} ${s.count.toLocaleString(locale)}`).join(", ");
  return `${dayLabel(b.day, locale)}: ${count(b.total, "alarm", locale)}${parts ? ` (${parts})` : ""}, ${count(b.events, "event", locale)}`;
}

// ------------------------------------------------------- recommendations ---

/** The permission each action needs: the server's guard on what it changes or opens. */
export const actionPermission: Record<RecommendationAction["type"], Permission> = {
  setAlertOn: "manageCameras", // PATCH /api/v2/cameras/{cameraId}
  openTemplate: "changeSettings", // the rule editor saves with POST /api/v2/rules
  openCamera: "manageCameras", // the camera's settings in Cameras
  openStorage: "viewLive", // System Monitor's storage per recording server
};

/** What a recommendation's button does: change the camera here, or open another section. */
export type ActionPlan =
  | { kind: "setAlertOn"; label: string; cameraId: string; alertOn: AlertOn }
  | { kind: "link"; label: string; href: string };

/**
 * The button for a recommendation, or null when there is none to show: no
 * action, one this page doesn't know, a missing id, or a user without the
 * permission it needs.
 */
export function actionFor(rec: Recommendation, can: (p: Permission) => boolean): ActionPlan | null {
  const a = rec.action;
  const permission = a ? actionPermission[a.type] : undefined;
  if (!a || !permission || !can(permission)) return null;
  const cameraId = a.cameraId || rec.cameraId || "";
  switch (a.type) {
    case "setAlertOn": {
      const to = a.alertOn ? alertOnLabel[a.alertOn] : undefined;
      return cameraId && a.alertOn && to ? { kind: "setAlertOn", label: `Switch to ${to.toLowerCase()}`, cameraId, alertOn: a.alertOn } : null;
    }
    case "openTemplate":
    case "openCamera":
    case "openStorage": {
      const target = linkTarget(rec);
      return target ? { kind: "link", label: linkLabel[target.to], href: adminHref(target) } : null;
    }
    default:
      return null;
  }
}

// ------------------------------------------------------- links (WAVE15) ---

/**
 * Where a link button goes, whatever page hosts Insights: the Admin follows
 * its hash (adminHref), the Operator switches tabs.
 */
export type InsightsLink = { to: "rules"; templateId: string } | { to: "camera"; cameraId: string } | { to: "storage" };

const linkLabel: Record<InsightsLink["to"], string> = { rules: "Use template", camera: "Open camera", storage: "Check storage" };

/** The link a recommendation's action points at, or null (not a link, or a missing id). Permissions are actionFor's job. */
export function linkTarget(rec: Recommendation): InsightsLink | null {
  const a = rec.action;
  switch (a?.type) {
    case "openTemplate":
      return a.templateId ? { to: "rules", templateId: a.templateId } : null;
    case "openCamera": {
      const cameraId = a.cameraId || rec.cameraId || "";
      return cameraId ? { to: "camera", cameraId } : null;
    }
    case "openStorage":
      return { to: "storage" };
    default:
      return null;
  }
}

/** The Admin section a link opens. */
export function adminHref(t: InsightsLink): string {
  switch (t.to) {
    case "rules":
      return sectionHash("rules", { template: t.templateId });
    case "camera":
      return sectionHash("cameras", { camera: t.cameraId });
    case "storage":
      return sectionHash("health");
  }
}

/** The message for a failed alert change; a 403 says which permission is missing. */
export function alertChangeError(e: unknown): string {
  return e instanceof ApiError && e.status === 403 ? "You need permission to change cameras." : errorMessage(e);
}
