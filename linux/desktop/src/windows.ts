// Window layouts per display setup. Operators move pop-outs onto other
// monitors (often one view per monitor, full screen); the app remembers,
// for each set of connected displays, where every window was and which
// pop-outs were open, and restores that the next time the same displays
// are connected. A window whose display is gone opens on the primary one.

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface DisplayLike {
  id: number;
  bounds: Rect;
  workArea: Rect;
}

export interface WindowState {
  bounds: Rect;
  displayId: number;
  fullscreen: boolean;
  maximized: boolean;
}

/** What a pop-out shows (mirrors PopoutTarget in the web build). */
export type PopoutTarget = { kind: "camera"; siteId: string; cameraId: string; title?: string } | { kind: "view"; siteId: string; viewId: string; title?: string };

export interface SetupLayout {
  /** Window key ("main", "camera:<site>:<id>", "view:<site>:<id>") → where it was. */
  windows: Record<string, WindowState>;
  /** Pop-outs open when the app quit, reopened at start. */
  popouts: PopoutTarget[];
}

export type Layouts = Record<string, SetupLayout>;

/** Identifies a display setup: the connected displays and their sizes, in a stable order. */
export function setupKey(displays: DisplayLike[]): string {
  return displays
    .map((d) => `${d.id}@${d.bounds.x},${d.bounds.y},${d.bounds.width}x${d.bounds.height}`)
    .sort()
    .join("|");
}

export function windowKey(t: PopoutTarget | null): string {
  if (!t) return "main";
  return t.kind === "camera" ? `camera:${t.siteId}:${t.cameraId}` : `view:${t.siteId}:${t.viewId}`;
}

/** Popout hash route for the renderer (see parseRoute in the web build). */
export function popoutHash(t: PopoutTarget): string {
  const id = t.kind === "camera" ? t.cameraId : t.viewId;
  return `#/popout/${t.kind}/${encodeURIComponent(t.siteId)}/${encodeURIComponent(id)}`;
}

export function isPopoutTarget(v: unknown): v is PopoutTarget {
  if (!v || typeof v !== "object") return false;
  const o = v as Record<string, unknown>;
  if (typeof o.siteId !== "string" || !o.siteId) return false;
  if (o.kind === "camera") return typeof o.cameraId === "string" && !!o.cameraId;
  if (o.kind === "view") return typeof o.viewId === "string" && !!o.viewId;
  return false;
}

function overlaps(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.width && a.x + a.width > b.x && a.y < b.y + b.height && a.y + a.height > b.y;
}

/**
 * Where to open a window: its saved place when that display is still there
 * and the bounds are on it; else centered on the primary display's work
 * area at the default size.
 */
export function placeWindow(saved: WindowState | undefined, displays: DisplayLike[], primary: DisplayLike, size: { width: number; height: number }): { bounds: Rect; fullscreen: boolean; maximized: boolean; displayId: number } {
  const d = saved ? displays.find((x) => x.id === saved.displayId) : undefined;
  if (saved && d && overlaps(saved.bounds, d.workArea)) {
    return { bounds: saved.bounds, fullscreen: saved.fullscreen, maximized: saved.maximized, displayId: d.id };
  }
  const wa = primary.workArea;
  const width = Math.min(size.width, wa.width);
  const height = Math.min(size.height, wa.height);
  return {
    bounds: { x: wa.x + Math.round((wa.width - width) / 2), y: wa.y + Math.round((wa.height - height) / 2), width, height },
    fullscreen: false,
    maximized: false,
    displayId: primary.id,
  };
}

/** The display a rectangle is mostly on. */
export function displayFor(r: Rect, displays: DisplayLike[]): DisplayLike | undefined {
  let best: DisplayLike | undefined;
  let area = -1;
  for (const d of displays) {
    const w = Math.max(0, Math.min(r.x + r.width, d.bounds.x + d.bounds.width) - Math.max(r.x, d.bounds.x));
    const h = Math.max(0, Math.min(r.y + r.height, d.bounds.y + d.bounds.height) - Math.max(r.y, d.bounds.y));
    if (w * h > area) {
      area = w * h;
      best = d;
    }
  }
  return best;
}
