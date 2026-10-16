// System Monitor model: turns GET /api/v2/system/health into what the cards
// and the camera table show (meters with state levels, formatted values) and
// finds offline transitions between two readings. Pure, so it can be tested.

import type { CameraHealth, RecordingServer, ServerHealth } from "../shared/api";
import type { StreamMessage } from "../shared/events";
import type { NoticeInput } from "./notices";

/** ok below 80 percent, warn from 80, crit from 95. */
export type Level = "ok" | "warn" | "crit" | "off";
export const warnAt = 80;
export const critAt = 95;
/** A recording camera whose newest file is older than this is stale (a segment is normally a minute or so). */
export const staleWarnSec = 120;
export const staleCritSec = 300;

export function levelOf(pct: number | null): Level {
  if (pct === null || !Number.isFinite(pct)) return "off";
  if (pct >= critAt) return "crit";
  if (pct >= warnAt) return "warn";
  return "ok";
}

export interface Meter {
  /** 0..100, null when the server doesn't know it. */
  pct: number | null;
  level: Level;
  /** "63%", or "n/a". */
  label: string;
  /** Hover detail, e.g. "412 GB of 1.8 TB". */
  detail: string;
}

const pctOf = (used: number, total: number) => (total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : null);

function meter(pct: number | null, detail = ""): Meter {
  return { pct, level: levelOf(pct), label: pct === null ? "n/a" : `${Math.round(pct)}%`, detail };
}

export interface ServerCard {
  id: string;
  name: string;
  online: boolean;
  /** The dot color: green online, red offline, amber when a meter is high. */
  state: Level;
  version: string;
  uptime: string;
  latency: string;
  cpu: Meter;
  mem: Meter;
  disk: Meter;
  db: string;
  cameras: number;
  camerasOnline: number;
}

export function serverCard(s: ServerHealth): ServerCard {
  const cpu = meter(s.online ? s.cpu : null);
  const mem = meter(s.mem ? pctOf(s.mem.used, s.mem.total) : null, s.mem ? `${formatBytes(s.mem.used)} of ${formatBytes(s.mem.total)}` : "");
  const disk = meter(s.disk ? pctOf(s.disk.used, s.disk.total) : null, s.disk ? `${formatBytes(s.disk.used)} of ${formatBytes(s.disk.total)} (${formatBytes(s.disk.free)} free)` : "");
  const worst = [cpu, mem, disk].some((m) => m.level === "crit") ? "crit" : [cpu, mem, disk].some((m) => m.level === "warn") ? "warn" : "ok";
  return {
    id: s.id,
    name: s.name,
    online: s.online,
    state: s.online ? worst : "crit",
    version: s.version,
    uptime: s.online && s.uptimeSec != null ? formatUptime(s.uptimeSec) : "",
    latency: s.latencyMs != null ? `${s.latencyMs} ms` : "",
    cpu,
    mem,
    disk,
    db: s.dbBytes != null ? formatBytes(s.dbBytes) : "",
    cameras: s.cameras.length,
    camerasOnline: s.cameras.filter((c) => c.online).length,
  };
}

export interface CameraRow {
  id: string;
  server: string;
  serverId: string;
  name: string;
  online: boolean;
  recording: boolean;
  /** Overall state of the row: crit offline, warn/crit for a stale recording, ok otherwise. */
  state: Level;
  lastSegment: string;
  lastSegmentLevel: Level;
  kbps: string;
}

export function segmentLevel(c: Pick<CameraHealth, "online" | "recording" | "lastSegmentAgeSec">): Level {
  const age = c.lastSegmentAgeSec;
  if (age == null) return c.recording ? "warn" : "off";
  if (!c.recording) return "off";
  if (age >= staleCritSec) return "crit";
  if (age >= staleWarnSec) return "warn";
  return "ok";
}

export function cameraRows(servers: ServerHealth[]): CameraRow[] {
  const rows: CameraRow[] = [];
  for (const s of servers) {
    for (const c of s.cameras) {
      const seg = segmentLevel(c);
      rows.push({
        id: c.id,
        server: s.name,
        serverId: s.id,
        name: c.name,
        online: c.online,
        recording: c.recording,
        state: !c.online ? "crit" : seg === "crit" || seg === "warn" ? seg : "ok",
        lastSegment: c.lastSegmentAgeSec == null ? "none" : formatAge(c.lastSegmentAgeSec),
        lastSegmentLevel: seg,
        kbps: c.kbps == null ? "" : formatKbps(c.kbps),
      });
    }
  }
  return rows.sort((a, b) => a.server.localeCompare(b.server) || a.name.localeCompare(b.name));
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function formatUptime(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

/** "12 s ago", "3 min ago", "2 h ago". */
export function formatAge(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  if (s < 90) return `${s} s ago`;
  if (s < 5400) return `${Math.round(s / 60)} min ago`;
  return `${Math.round(s / 3600)} h ago`;
}

export function formatKbps(k: number): string {
  return k >= 1000 ? `${(k / 1000).toFixed(1)} Mbps` : `${Math.round(k)} kbps`;
}

/**
 * Online to offline transitions between two readings (a server or camera
 * that was online and now isn't), as notices. Things not in `prev` are not
 * transitions: the first reading never raises anything.
 */
export function offlineTransitions(prev: ServerHealth[] | null, next: ServerHealth[]): NoticeInput[] {
  if (!prev) return [];
  const out: NoticeInput[] = [];
  const before = new Map(prev.map((s) => [s.id, s]));
  for (const s of next) {
    const was = before.get(s.id);
    if (!was) continue;
    if (was.online && !s.online) {
      out.push({ key: `server-offline:${s.id}`, level: "error", text: `Recording server ${s.name} went offline` });
      continue; // its cameras go with it; one notice
    }
    const wasCam = new Map(was.cameras.map((c) => [c.id, c]));
    for (const c of s.cameras) {
      if (wasCam.get(c.id)?.online && !c.online) out.push({ key: `camera-offline:${c.id}`, level: "warn", text: `Camera ${c.name} went offline` });
    }
  }
  return out;
}

/**
 * The notice for a live stream message: a camera or recording server going
 * offline (warn/error) or coming back (info). Anything else is null.
 */
export function streamNotice(m: StreamMessage, cameras: { id: string; name: string; status: { online: boolean } }[] | null, servers: Pick<RecordingServer, "id" | "name">[]): NoticeInput | null {
  if (m.type === "camera.status") {
    const c = cameras?.find((x) => x.id === m.data.cameraId);
    if (!c) return null;
    if (m.data.online === c.status.online) return null; // not a transition
    return m.data.online
      ? { key: `camera-online:${c.id}`, level: "info", text: `Camera ${c.name} is back online` }
      : { key: `camera-offline:${c.id}`, level: "warn", text: `Camera ${c.name} went offline` };
  }
  if (m.type === "recorder.status") {
    const s = servers.find((x) => x.id === m.data.recorderId);
    const name = s?.name ?? m.data.recorderId;
    return m.data.online
      ? { key: `server-online:${m.data.recorderId}`, level: "info", text: `Recording server ${name} reconnected` }
      : { key: `server-offline:${m.data.recorderId}`, level: "error", text: `Recording server ${name} went offline` };
  }
  return null;
}

// MARK: - Tiles

export type ActivityKind = "person" | "vehicle" | "animal" | "motion" | "other";

/** Which glyph an event kind gets on the tile title bar. */
export function activityKind(kind: string): ActivityKind {
  const k = kind.toLowerCase();
  if (k.includes("person") || k.includes("people") || k.includes("human") || k.includes("face")) return "person";
  if (k.includes("vehicle") || k.includes("car") || k.includes("truck") || k.includes("plate")) return "vehicle";
  if (k.includes("animal") || k.includes("dog") || k.includes("cat") || k.includes("pet")) return "animal";
  if (k.includes("motion")) return "motion";
  return "other";
}

export const activityFadeMs = 5000;

/** Whether a tile is "reconnecting": its camera reports offline. */
export const reconnecting = (c: { status?: { online: boolean } } | undefined) => !!c && c.status?.online === false;
