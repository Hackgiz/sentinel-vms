// PTZ input model: which controls are held and what velocity that means.
// Kept free of React so it can be tested.

export type PtzDir = "up" | "down" | "left" | "right" | "zoomIn" | "zoomOut";

export interface PtzVelocity {
  pan: number;
  tilt: number;
  zoom: number;
}

/** Pan/tilt and zoom speed as a fraction of the camera's maximum. */
export const PTZ_SPEED = 0.5;
export const PTZ_ZOOM_SPEED = 0.4;

/** The combined velocity of every held control; opposite controls cancel. */
export function velocityOf(held: Iterable<PtzDir>): PtzVelocity {
  const h = new Set(held);
  const axis = (pos: PtzDir, neg: PtzDir, speed: number) => ((h.has(pos) ? 1 : 0) - (h.has(neg) ? 1 : 0)) * speed;
  return {
    pan: axis("right", "left", PTZ_SPEED),
    tilt: axis("up", "down", PTZ_SPEED),
    zoom: axis("zoomIn", "zoomOut", PTZ_ZOOM_SPEED),
  };
}

export function isStill(v: PtzVelocity): boolean {
  return v.pan === 0 && v.tilt === 0 && v.zoom === 0;
}

/** The control a key press stands for: arrow keys pan/tilt, + and - zoom. */
export function keyDir(key: string): PtzDir | null {
  switch (key) {
    case "ArrowUp":
      return "up";
    case "ArrowDown":
      return "down";
    case "ArrowLeft":
      return "left";
    case "ArrowRight":
      return "right";
    case "+":
    case "=":
      return "zoomIn";
    case "-":
    case "_":
      return "zoomOut";
    default:
      return null;
  }
}

/** Typing in a field must not steer the camera. */
export function isTypingTarget(t: EventTarget | null): boolean {
  const el = t as { tagName?: string; isContentEditable?: boolean } | null;
  if (!el?.tagName) return false;
  return el.isContentEditable === true || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
}

/** A trimmed preset name, or null when it is empty or too long for the server (64). */
export function cleanPresetName(name: string): string | null {
  const n = name.trim();
  return n && n.length <= 64 ? n : null;
}
