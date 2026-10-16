// Pure helpers for the stream-settings dialog (StreamSettingsDialog.tsx).
import type { components } from "../api/v2";

export type Stream = components["schemas"]["Stream"];
export type StreamRange = components["schemas"]["StreamRange"];
export type StreamChange = components["schemas"]["StreamSettingsChange"];

/** What an edit form holds for one stream. */
export interface StreamForm {
  resolution: string; // "1920x1080"
  fps: number;
  bitrate: number;
  govLength: number;
  quality: number;
}

export const resKey = (w: number, h: number) => `${w}x${h}`;

export function formOfStream(s: Stream): StreamForm {
  return {
    resolution: resKey(s.current.width, s.current.height),
    fps: s.current.fps,
    bitrate: s.current.bitrate,
    govLength: s.current.govLength,
    quality: s.current.quality,
  };
}

/** The resolutions to offer: the camera's list, with the current one always present. */
export function resolutionChoices(s: Stream): { key: string; width: number; height: number }[] {
  const out = s.options.resolutions.map((r) => ({ key: resKey(r.width, r.height), width: r.width, height: r.height }));
  if (!out.some((r) => r.key === resKey(s.current.width, s.current.height))) {
    out.unshift({ key: resKey(s.current.width, s.current.height), width: s.current.width, height: s.current.height });
  }
  return out;
}

/** A range the camera did not report (max 0) is free-form input. */
export const bounded = (r: StreamRange) => r.max > 0;

/** Sliders only where the range is tight enough to be usable. */
export const useSlider = (r: StreamRange) => bounded(r) && r.max - r.min <= 100;

/** Is v inside the range (an unbounded range accepts anything positive)? */
export function inRange(v: number, r: StreamRange): boolean {
  if (!Number.isInteger(v) || v <= 0) return false;
  return !bounded(r) || (v >= r.min && v <= r.max);
}

/**
 * The PUT body for a form: only what differs from the camera's current
 * values (zero fields keep them on the server). null when nothing changed.
 */
export function changeOf(s: Stream, f: StreamForm): StreamChange | null {
  const c: StreamChange = {};
  if (f.resolution !== resKey(s.current.width, s.current.height)) {
    const [w, h] = f.resolution.split("x").map(Number);
    c.width = w;
    c.height = h;
  }
  if (f.fps !== s.current.fps) c.fps = f.fps;
  if (f.bitrate !== s.current.bitrate) c.bitrate = f.bitrate;
  if (f.govLength !== s.current.govLength) c.govLength = f.govLength;
  if (f.quality !== s.current.quality) c.quality = f.quality;
  return Object.keys(c).length ? c : null;
}

/** Changing the picture size or frame rate makes the camera restart its stream. */
export const reconnects = (c: StreamChange | null): boolean => !!c && (c.width !== undefined || c.fps !== undefined);

/** First problem with the changed fields of a form against the camera's ranges, or "". */
export function problemWith(s: Stream, f: StreamForm): string {
  const o = s.options;
  const c = s.current;
  if (f.fps !== c.fps && !inRange(f.fps, o.fps)) return rangeText("Frame rate", o.fps);
  if (f.bitrate !== c.bitrate && !inRange(f.bitrate, o.bitrate)) return rangeText("Bitrate", o.bitrate);
  if (f.govLength !== c.govLength && !inRange(f.govLength, o.gov)) return rangeText("Keyframe interval", o.gov);
  if (f.quality !== c.quality && !inRange(f.quality, o.quality)) return rangeText("Quality", o.quality);
  return "";
}

const rangeText = (name: string, r: StreamRange) => (bounded(r) ? `${name} must be a whole number from ${r.min} to ${r.max}.` : `${name} must be a positive whole number.`);

export function streamTitle(s: Stream): string {
  return s.inUse === "main" ? "Main stream" : s.inUse === "sub" ? "Sub stream" : s.name || s.profileToken;
}
