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
