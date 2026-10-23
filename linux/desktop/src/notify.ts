// PARITY-342: a native notification when a new alarm arrives. The operator
// page decides which stream messages deserve one (alarmNotify.ts in
// linux/web) and asks through the bridge (notifications.alarm); clicking
// the notification brings its window forward and tells the page to open
// that alarm ("notify:open-alarm").
import { BrowserWindow, Notification } from "electron";
import type { AlarmNotice } from "../../web/src/operator/bridge";

/** Notifications on screen; held so they are not collected before a click. */
const live = new Set<Notification>();

const text = (v: unknown, max: number) => (typeof v === "string" ? v.slice(0, max) : "");

/** A notice from the renderer, checked and trimmed; null when it isn't one. */
export function parseNotice(n: unknown): AlarmNotice | null {
  const o = n as Partial<AlarmNotice> | null;
  if (!o || typeof o !== "object" || typeof o.alarmId !== "string" || !o.alarmId || o.alarmId.length > 200) return null;
  return { alarmId: o.alarmId, title: text(o.title, 120) || "New alarm", body: text(o.body, 240), critical: o.critical === true };
}

/** Bring a window forward (restored, shown, focused). */
export function focusWindow(win: BrowserWindow) {
  if (win.isDestroyed()) return;
  if (win.isMinimized()) win.restore();
  win.show();
  win.focus();
}

/** Show the notice for `win`, unless that window is already in front of the user. */
export function notifyAlarm(win: BrowserWindow | null, notice: unknown, icon?: string) {
  const n = parseNotice(notice);
  if (!n || !win || win.isDestroyed() || !Notification.isSupported()) return;
  if (win.isFocused() && win.isVisible() && !win.isMinimized()) return; // the in-app banner and queue already show it
  const note = new Notification({ title: n.title, body: n.body, icon, urgency: n.critical ? "critical" : "normal", timeoutType: n.critical ? "never" : "default" });
  const done = () => live.delete(note);
  note.on("click", () => {
    done();
    if (win.isDestroyed()) return;
    focusWindow(win);
    win.webContents.send("notify:open-alarm", n.alarmId);
  });
  note.on("close", done);
  live.add(note);
  note.show();
}
