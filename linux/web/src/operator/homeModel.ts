// WAVE20: the Home dashboard's arithmetic (HomeView.tsx), after the iPhone's
// HomeDashboard: the header line, the four stat tiles, the alarms that need
// attention, recent activity and the camera tiles. Pure, so it is tested;
// everything comes from data the workspace already reads.

import { tones } from "../shared/alarmKind";
import { dayBounds } from "../shared/time";

export interface HomeCamera {
  id: string;
  name: string;
  location: string;
  recording: boolean;
  status: { online: boolean; width?: number; height?: number };
}

export interface HomeEvent {
  id: string;
  cameraId: string;
  kind: string;
  createdAt: number;
  hasThumbnail: boolean;
}

export interface HomeAlarm {
  id: string;
  state: string;
  severity: string;
  lastEventAt: number;
}

/** The content column's cap and the alarm/camera limits, as on the iPhone. */
export const contentMax = 1100;
export const alarmLimit = 3;
export const activityLimit = 6;
export const cameraLimit = 8;

export type StreamState = "connecting" | "open" | "reconnecting" | "stopped" | "forbidden";

const privateV4 = /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)/i;
const privateV6 = /^\[(::1|f[cd][0-9a-f]{0,2}:|fe80:)/i;

/** Whether a site address is on the local network (private, link-local, or a .local or bare host name). */
export function isLocalAddress(address: string): boolean {
  let host: string;
  try {
    host = new URL(address).hostname;
  } catch {
    return false;
  }
  if (host.startsWith("[")) return privateV6.test(host);
  return privateV4.test(host) || host.toLowerCase().endsWith(".local") || !host.includes(".");
}

/** The header's subtitle and dot: where the site is and whether the stream is live. */
export function connectionLine(stream: StreamState, address: string): { text: string; tone: string } {
  const where = isLocalAddress(address) ? "Local network" : "Remote";
  switch (stream) {
    case "open":
      return { text: `${where} · live`, tone: tones.green };
    case "connecting":
      return { text: `${where} · connecting…`, tone: tones.amber };
    case "reconnecting":
      return { text: `${where} · reconnecting…`, tone: tones.amber };
    case "forbidden":
      return { text: `${where} · live updates not allowed`, tone: tones.red };
    default:
      return { text: `${where} · not connected`, tone: tones.red };
  }
}

export interface CameraCounts {
  total: number;
  online: number;
  offline: number;
  /** Online and recording. */
  recording: number;
}

export function cameraCounts(cameras: HomeCamera[]): CameraCounts {
  const online = cameras.filter((c) => c.status.online).length;
  return { total: cameras.length, online, offline: cameras.length - online, recording: cameras.filter((c) => c.status.online && c.recording).length };
}

/** Events created today (local time): the facet total when the server gave one, else what is loaded. */
export function eventsToday(events: HomeEvent[], now: number, facetTotal?: number | null): number {
  const [start, end] = dayBounds(now);
  const loaded = events.filter((e) => e.createdAt >= start && e.createdAt < end).length;
  return facetTotal == null ? loaded : Math.max(facetTotal, loaded);
}

export type TileTarget = "live" | "events" | "alarms";

export interface StatTileModel {
  id: "cameras" | "recording" | "events" | "alarms";
  title: string;
  icon: string;
  value: string;
  note: string;
  tone: string;
  target: TileTarget;
}

/** The four tiles: cameras online, recording, events today, open alarms. */
export function statTiles(cams: CameraCounts, events: number, alarms: HomeAlarm[]): StatTileModel[] {
  const open = alarms.length;
  const fresh = alarms.filter((a) => a.state === "New").length;
  return [
    {
      id: "cameras",
      title: "Cameras online",
      icon: "video",
      value: `${cams.online}/${cams.total}`,
      note: cams.offline > 0 ? `${cams.offline} offline` : cams.total === 0 ? "None yet" : "All online",
      tone: cams.offline > 0 ? tones.red : tones.green,
      target: "live",
    },
    {
      id: "recording",
      title: "Recording",
      icon: "record",
      value: String(cams.recording),
      note: cams.total > 0 && cams.recording === cams.online ? "Every online camera" : `of ${cams.total} camera${cams.total === 1 ? "" : "s"}`,
      tone: tones.green,
      target: "live",
    },
    { id: "events", title: "Events today", icon: "sparkles", value: String(events), note: "AI detections", tone: tones.accent, target: "events" },
    {
      id: "alarms",
      title: "Open alarms",
      icon: "bell",
      value: String(open),
      note: fresh > 0 ? `${fresh} new` : open > 0 ? "Being handled" : "Nothing open",
      tone: fresh > 0 ? tones.red : open > 0 ? tones.amber : tones.green,
      target: "alarms",
    },
  ];
}

const sevRank: Record<string, number> = { Critical: 0, Warning: 1, Info: 2 };

/** New alarms, most severe then newest first, the first `limit`, and how many more there are. */
export function needsAttention<A extends HomeAlarm>(alarms: A[], limit = alarmLimit): { shown: A[]; more: number } {
  const all = alarms.filter((a) => a.state === "New").sort((a, b) => (sevRank[a.severity] ?? 3) - (sevRank[b.severity] ?? 3) || b.lastEventAt - a.lastEventAt);
  return { shown: all.slice(0, limit), more: Math.max(0, all.length - limit) };
}

/** Newest first, at most `limit`. */
export const recentActivity = <E extends HomeEvent>(events: E[], limit = activityLimit): E[] => [...events].sort((a, b) => b.createdAt - a.createdAt).slice(0, limit);

/** A new or updated event into the newest-first list, capped. */
export function mergeEvent<E extends HomeEvent>(events: E[], e: E, cap = 200): E[] {
  const rest = events.filter((x) => x.id !== e.id);
  return [e, ...rest].sort((a, b) => b.createdAt - a.createdAt).slice(0, cap);
}

/** The newest event on a camera with a saved frame: the camera tile's still. */
export function latestStill<E extends HomeEvent>(cameraId: string, events: E[]): E | null {
  let best: E | null = null;
  for (const e of events) if (e.cameraId === cameraId && e.hasThumbnail && (!best || e.createdAt > best.createdAt)) best = e;
  return best;
}

export interface CameraTileModel<C extends HomeCamera, E extends HomeEvent> {
  camera: C;
  still: E | null;
  online: boolean;
  rec: boolean;
  /** The location, else the resolution, else nothing. */
  place: string;
}

/** The camera tiles: offline ones first (they need a look), then in the site's order; capped. */
export function cameraTiles<C extends HomeCamera, E extends HomeEvent>(cameras: C[], events: E[], limit = cameraLimit): { tiles: CameraTileModel<C, E>[]; more: number } {
  const ordered = [...cameras.filter((c) => !c.status.online), ...cameras.filter((c) => c.status.online)];
  const tiles = ordered.slice(0, limit).map((camera) => ({
    camera,
    still: latestStill(camera.id, events),
    online: camera.status.online,
    rec: camera.status.online && camera.recording,
    place: camera.location.trim() || (camera.status.width && camera.status.height ? `${camera.status.width}×${camera.status.height}` : ""),
  }));
  return { tiles, more: Math.max(0, cameras.length - limit) };
}

/** The Insights card's line: the last seven days, or what the page is for. */
export function insightsLine(totals: { alarms: number; events: number } | null): string {
  if (!totals) return "Trends and recommendations";
  if (totals.alarms === 0 && totals.events === 0) return "Nothing in the last 7 days";
  const n = (v: number, one: string) => `${v.toLocaleString("en-US")} ${one}${v === 1 ? "" : "s"}`;
  return `${n(totals.alarms, "alarm")} · ${n(totals.events, "event")} in 7 days`;
}

/** The People card's line: how many are enrolled and who was seen last. */
export function peopleLine(people: { name: string; lastSeenAt?: number }[] | null): string {
  if (!people) return "Who was recognized, and when";
  if (!people.length) return "No one enrolled yet";
  const seen = people.filter((p) => p.lastSeenAt).sort((a, b) => (b.lastSeenAt ?? 0) - (a.lastSeenAt ?? 0))[0];
  const count = `${people.length} ${people.length === 1 ? "person" : "people"}`;
  return seen ? `${count} · ${seen.name} seen last` : count;
}
