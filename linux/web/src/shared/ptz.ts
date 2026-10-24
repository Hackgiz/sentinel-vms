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

/**
 * Whether PTZ controls show for a camera: the server's /ptz probe says it is
 * supported, the camera is online, and the user has the `controlPtz` right.
 */
export function ptzVisible(o: { supported: boolean; online: boolean; canControl: boolean }): boolean {
  return o.supported && o.online && o.canControl;
}

/** The two PTZ calls the hold model makes (a subset of ApiClient["ptz"]). */
export interface PtzMover {
  move: (cameraId: string, pan: number, tilt: number, zoom: number) => Promise<unknown>;
  stop: (cameraId: string) => Promise<unknown>;
}

/**
 * Press-and-hold PTZ for one camera. Each change of the held set sends one
 * move, or a stop once nothing is held. Commands go out one after another so
 * a stop can never overtake a move. `releaseAll` (window blur, page hidden,
 * unmount, camera gone offline) sends a stop whenever the camera may still
 * be moving.
 */
export class PtzHold {
  private held = new Set<PtzDir>();
  private moving = false;
  private queue: Promise<unknown> = Promise.resolve();
  private readonly mover: PtzMover;
  private readonly cameraId: string;
  private readonly onError: (e: unknown) => void;
  private readonly onLit: (d: PtzDir | null) => void;

  constructor(mover: PtzMover, cameraId: string, onError: (e: unknown) => void = () => {}, onLit: (d: PtzDir | null) => void = () => {}) {
    this.mover = mover;
    this.cameraId = cameraId;
    this.onError = onError;
    this.onLit = onLit;
  }

  /** Queue a command behind the ones already sent (presets use this too). */
  send(fn: () => Promise<unknown>): void {
    this.queue = this.queue.then(fn).catch(this.onError);
  }

  /** Resolves once every queued command has settled. */
  idle(): Promise<unknown> {
    return this.queue;
  }

  press(d: PtzDir): void {
    if (this.held.has(d)) return;
    this.held.add(d);
    this.apply();
  }

  release(d: PtzDir): void {
    if (!this.held.delete(d)) return;
    this.apply();
  }

  releaseAll(): void {
    this.held.clear();
    if (this.moving) this.apply();
  }

  private apply(): void {
    const v = velocityOf(this.held);
    this.onLit([...this.held].at(-1) ?? null);
    const id = this.cameraId;
    if (isStill(v)) {
      this.moving = false;
      this.send(() => this.mover.stop(id));
    } else {
      this.moving = true;
      this.send(() => this.mover.move(id, v.pan, v.tilt, v.zoom));
    }
  }
}
