// Audio event detection settings of a camera (Wave 8, stream B).

import type { Camera, CameraPatch } from "../shared/api";

export const AUDIO_DEFAULT_DB = 25;
export const AUDIO_MIN_DB = 6;
export const AUDIO_MAX_DB = 60;

/** Clamp a typed threshold into the range the server accepts. */
export function clampAudioDb(v: number): number {
  if (!Number.isFinite(v)) return AUDIO_DEFAULT_DB;
  return Math.min(AUDIO_MAX_DB, Math.max(AUDIO_MIN_DB, Math.round(v)));
}

export interface AudioForm {
  audioDetect: boolean;
  audioThresholdDb: number;
}

/** The form values of a stored camera (older servers omit the fields). */
export function audioFormOf(c: Pick<Camera, "audioDetect" | "audioThresholdDb"> | null): AudioForm {
  return { audioDetect: c?.audioDetect ?? false, audioThresholdDb: c?.audioThresholdDb ?? AUDIO_DEFAULT_DB };
}

/** Only the audio fields that changed. */
export function audioPatch(c: Pick<Camera, "audioDetect" | "audioThresholdDb">, f: AudioForm): CameraPatch {
  const cur = audioFormOf(c);
  const p: CameraPatch = {};
  if (f.audioDetect !== cur.audioDetect) p.audioDetect = f.audioDetect;
  if (f.audioThresholdDb !== cur.audioThresholdDb) p.audioThresholdDb = f.audioThresholdDb;
  return p;
}

// Wave 16 (tracking): "Loitering after", shown with the audio settings.
// A person track present this long raises one Loitering event; 0 = off.

export const LOITER_MAX_S = 3600;

/** Clamp typed seconds into 0–3600 (anything unreadable is off). */
export function clampLoiter(v: number): number {
  if (!Number.isFinite(v) || v <= 0) return 0;
  return Math.min(LOITER_MAX_S, Math.round(v));
}

/** A stored camera's loitering seconds (older servers omit the field). */
export const loiterOf = (c: Pick<Camera, "loiterSeconds"> | null): number => c?.loiterSeconds ?? 0;

/**
 * The camera patch for a changed loitering time. CameraPatch in the contract
 * does not list `loiterSeconds` yet (wave 16 asks for it), so the type is
 * widened here.
 */
export function loiterPatch(c: Pick<Camera, "loiterSeconds">, seconds: number): CameraPatch & { loiterSeconds?: number } {
  return seconds === loiterOf(c) ? {} : { loiterSeconds: seconds };
}

/** One line under the field. */
export function loiterHint(seconds: number): string {
  if (seconds <= 0) return "Off. Set a number of seconds to raise a Loitering event when a person stays that long.";
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  const t = m ? `${m} min${s ? ` ${s} s` : ""}` : `${s} s`;
  return `A person seen for ${t} or more raises one Loitering event (a Warning alarm). Needs object detection on this camera.`;
}
