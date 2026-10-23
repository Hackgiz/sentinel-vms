// PARITY-342: the desktop app's new-alarm notification (DesktopBridge
// notifications.alarm). What it says, and which stream messages raise one.
// Pure, so it is tested.

import type { Alarm } from "../shared/api";
import type { StreamMessage } from "../shared/events";
import type { AlarmNotice } from "./bridge";

/** The notification for a new alarm: the title, then camera · severity · detail. */
export function alarmNotice(a: Pick<Alarm, "id" | "title" | "kind" | "severity" | "cameraName" | "detail">, siteName?: string): AlarmNotice {
  const where = [a.cameraName, siteName].filter((s) => s?.trim()).join(" · ");
  const body = [where, a.severity, a.detail?.trim()].filter(Boolean).join(" · ");
  return {
    alarmId: a.id,
    title: (a.title || a.kind || "New alarm").slice(0, 120),
    body: body.slice(0, 240),
    critical: a.severity === "Critical" || a.kind === "Watchlist",
  };
}

/**
 * The alarm a stream message should notify about: only `alarm.new` in the
 * New state, once per alarm id (a reconnect may replay it).
 */
export function noticeFor(m: StreamMessage, seen: Set<string>): Alarm | null {
  if (m.type !== "alarm.new" || m.data.state !== "New" || seen.has(m.data.id)) return null;
  seen.add(m.data.id);
  if (seen.size > 500) seen.delete(seen.values().next().value as string);
  return m.data;
}
