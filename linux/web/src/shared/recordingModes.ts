// Wave 22: recording modes (continuous / events / dual / off), per camera or
// as a server default. The generated api/v2.d.ts does not carry these fields
// yet (streams A and B add them), so the shapes live here and are joined onto
// the generated types with intersections at the API boundary.
// TODO(wave22 merge): swap for the generated types once v2.d.ts is regenerated.

import type { ApiClient, Camera, CameraHealth, CameraInput, CameraPatch } from "./api";

/** What the recorder actually does. */
export type RecordingMode = "continuous" | "events" | "dual" | "off";
/** A camera's setting: `default` follows the server. */
export type CameraRecordingMode = "default" | RecordingMode;
export type RecordTrigger = "motion" | "objects";
export type CameraRecordTrigger = "default" | RecordTrigger;
/** Span.quality: absent = full. */
export type SpanQuality = "full" | "low";

/**
 * The camera fields wave 22 adds (absent on older servers). WAVE24(D1): the
 * roll seconds are nullable; null or absent = the server default, a number
 * (0 included) is that many seconds.
 */
export interface CameraRecordingFields {
  recordingMode?: CameraRecordingMode;
  /** Read-only. */
  effectiveRecordingMode?: RecordingMode;
  recordTrigger?: CameraRecordTrigger;
  /** null = server default; a number (0 included) is literal (D1). */
  preRollSeconds?: number | null;
  postRollSeconds?: number | null;
  lowResRetentionDays?: number;
}
export type RecCamera = Omit<Camera, "preRollSeconds" | "postRollSeconds"> & CameraRecordingFields;
// The generated types now carry nullable rolls (wave 24 N); the Omit keeps this file the one source of the shape.
type RollKeys = "preRollSeconds" | "postRollSeconds";
export type RecCameraInput = Omit<CameraInput, RollKeys> & Omit<CameraRecordingFields, "effectiveRecordingMode">;
export type RecCameraPatch = Omit<CameraPatch, RollKeys> & Omit<CameraRecordingFields, "effectiveRecordingMode">;

/** GET/PATCH /api/v2/settings/recording. */
export interface RecordingSettings {
  defaultMode: RecordingMode;
  defaultTrigger: RecordTrigger;
  /** null/absent from a newer or older server reads as the built-in default. */
  defaultPreRollSeconds: number | null;
  defaultPostRollSeconds: number | null;
  defaultRetentionDays: number;
  defaultLowResRetentionDays: number;
  /** 0 = off, else 5–50. */
  minFreePercent: number;
}

export const recordingDefaults: RecordingSettings = {
  defaultMode: "continuous",
  defaultTrigger: "motion",
  defaultPreRollSeconds: 5,
  defaultPostRollSeconds: 15,
  defaultRetentionDays: 7,
  defaultLowResRetentionDays: 14,
  minFreePercent: 0,
};

/** Health camera row additions. The server sends `lastKeptClipAt` as RFC 3339; Unix ms/s are tolerated. */
export type RecCameraHealth = Omit<CameraHealth, "lastKeptClipAt"> & { recordingMode?: CameraRecordingMode; lastKeptClipAt?: string | number | null };

export const recordingApi = {
  get: (c: ApiClient) => c.request<RecordingSettings>("get", "/api/v2/settings/recording"),
  patch: (c: ApiClient, body: Partial<RecordingSettings>) => c.request<RecordingSettings>("patch", "/api/v2/settings/recording", body),
};

// ------------------------------------------------------------- the modes ---

export interface ModeChoice {
  value: RecordingMode;
  label: string;
  line: string;
}

export const modeChoices: ModeChoice[] = [
  { value: "continuous", label: "Continuous", line: "Records the main stream around the clock." },
  { value: "events", label: "Events only", line: "Records the main stream around motion and detections, with a few seconds before and after." },
  { value: "dual", label: "Low-res 24/7 + full-res on events", line: "Records the sub-stream around the clock, and the main stream around events." },
  { value: "off", label: "Off (live only)", line: "Live view only. Nothing is recorded." },
];

export const modeLabel = (m: RecordingMode): string => modeChoices.find((c) => c.value === m)?.label ?? m;

/** "Recommended" goes on dual when the camera has a sub-stream, on continuous otherwise. */
export const recommendedMode = (hasSub: boolean): RecordingMode => (hasSub ? "dual" : "continuous");

/** The camera's setting; older servers only know `recording`. */
export function modeOf(c: Pick<RecCamera, "recording" | "recordingMode"> | null): CameraRecordingMode {
  if (!c) return "default";
  return c.recordingMode ?? (c.recording ? "default" : "off");
}

/** What a setting resolves to: the server default, and dual falls back to events without a sub-stream. */
export function resolveMode(mode: CameraRecordingMode, serverDefault: RecordingMode | undefined, hasSub: boolean): RecordingMode {
  const m = mode === "default" ? (serverDefault ?? "continuous") : mode;
  return m === "dual" && !hasSub ? "events" : m;
}

/** What the camera records now: the server's word when it sends one. */
export function effectiveModeOf(c: Pick<RecCamera, "recording" | "recordingMode" | "effectiveRecordingMode" | "subRtspUrl">, serverDefault?: RecordingMode): RecordingMode {
  if (c.effectiveRecordingMode) return c.effectiveRecordingMode;
  if (c.recordingMode === undefined) return c.recording ? "continuous" : "off";
  return resolveMode(c.recordingMode, serverDefault, !!c.subRtspUrl);
}

/** The camera list badge. */
export function modeBadge(m: RecordingMode): { text: string; tone: "red" | "amber" | "blue" } {
  switch (m) {
    case "continuous":
      return { text: "24/7", tone: "red" };
    case "events":
      return { text: "Events", tone: "amber" };
    case "dual":
      return { text: "24/7 low + events", tone: "red" };
    default:
      return { text: "Live only", tone: "blue" };
  }
}

export const usesEvents = (m: RecordingMode) => m === "events" || m === "dual";

export const triggerChoices: { value: RecordTrigger; label: string }[] = [
  { value: "motion", label: "Any motion" },
  { value: "objects", label: "People and vehicles only" },
];
export const triggerLabel = (t: RecordTrigger) => triggerChoices.find((c) => c.value === t)?.label ?? t;

/** Events and dual need something to start a clip: motion detection or camera analytics. */
export function noTriggerWarning(mode: RecordingMode, motionLevel: number, cameraAnalytics: boolean): string {
  return usesEvents(mode) && motionLevel === 0 && !cameraAnalytics ? "Nothing will start a recording. Turn on motion detection or camera analytics." : "";
}

// --------------------------------------------------------------- numbers ---

/** Choices for the roll pickers. A camera's picker puts "Server default" (null) before these; 0 is a literal 0 s. */
export const preRollChoices = [0, 1, 2, 3, 5, 10, 15, 20, 30];
export const postRollChoices = [0, 1, 2, 3, 5, 10, 15, 20, 30, 60, 90, 120];
export const keepDayChoices = [1, 3, 7, 14, 30, 60, 90, 180, 365];
export const PRE_ROLL_MAX = 30;
export const POST_ROLL_MIN = 0;
export const POST_ROLL_MAX = 120;
export const KEEP_DAYS_MAX = 365;

const clampInt = (v: number, lo: number, hi: number) => (Number.isFinite(v) ? Math.min(hi, Math.max(lo, Math.round(v))) : lo);
export const clampPreRoll = (v: number) => clampInt(v, 0, PRE_ROLL_MAX);
/** 0–120 s. */
export const clampPostRoll = (v: number) => clampInt(v, POST_ROLL_MIN, POST_ROLL_MAX);

/** A server-default roll, reading null/absent as the built-in value. */
export const rollOrDefault = (v: number | null | undefined, fallback: number): number => (typeof v === "number" && Number.isFinite(v) ? v : fallback);
export const clampKeepDays = (v: number) => clampInt(v, 0, KEEP_DAYS_MAX);
/** minFreePercent: 0 = off, else 5–50. */
export const clampMinFree = (v: number) => (!Number.isFinite(v) || v <= 0 ? 0 : clampInt(v, 5, 50));
export const minFreeChoices = [5, 10, 15, 20, 25, 30, 40, 50];

/** The low-res keep days a camera ends up with: its own, else the server's, else 2× full-res. */
export function lowResDaysOf(own: number, server: RecordingSettings | null, fullDays: number): number {
  if (own > 0) return own;
  if (server?.defaultLowResRetentionDays) return server.defaultLowResRetentionDays;
  return Math.min(KEEP_DAYS_MAX, 2 * fullDays);
}

// -------------------------------------------------------- the editor form ---

export interface RecordingForm {
  recordingMode: CameraRecordingMode;
  recordTrigger: CameraRecordTrigger;
  /** null = server default. */
  preRollSeconds: number | null;
  postRollSeconds: number | null;
  retentionDays: number;
  lowResRetentionDays: number;
}

export function recordingFormOf(c: RecCamera | null): RecordingForm {
  return {
    recordingMode: modeOf(c),
    recordTrigger: c?.recordTrigger ?? "default",
    preRollSeconds: c?.preRollSeconds ?? null,
    postRollSeconds: c?.postRollSeconds ?? null,
    retentionDays: c?.retentionDays ?? 0,
    lowResRetentionDays: c?.lowResRetentionDays ?? 0,
  };
}

/**
 * Only the recording fields that changed. A server that predates wave 22
 * (no `recordingMode` on the camera) gets the old `recording` flag instead.
 */
export function recordingPatch(c: RecCamera, f: RecordingForm): RecCameraPatch {
  const cur = recordingFormOf(c);
  const p: RecCameraPatch = {};
  if (f.retentionDays !== cur.retentionDays) p.retentionDays = f.retentionDays;
  if (c.recordingMode === undefined) {
    const rec = f.recordingMode !== "off";
    if (rec !== c.recording) p.recording = rec;
    return p;
  }
  if (f.recordingMode !== cur.recordingMode) p.recordingMode = f.recordingMode;
  if (f.recordTrigger !== cur.recordTrigger) p.recordTrigger = f.recordTrigger;
  if (f.preRollSeconds !== cur.preRollSeconds) p.preRollSeconds = f.preRollSeconds;
  if (f.postRollSeconds !== cur.postRollSeconds) p.postRollSeconds = f.postRollSeconds;
  if (f.lowResRetentionDays !== cur.lowResRetentionDays) p.lowResRetentionDays = f.lowResRetentionDays;
  return p;
}

/** The recording part of a new camera (with `recording` too, which CameraInput requires). */
export function recordingInput(f: RecordingForm): Pick<RecCameraInput, "recording" | "recordingMode" | "recordTrigger" | "preRollSeconds" | "postRollSeconds" | "retentionDays" | "lowResRetentionDays"> {
  return {
    recording: f.recordingMode !== "off",
    recordingMode: f.recordingMode,
    recordTrigger: f.recordTrigger,
    preRollSeconds: f.preRollSeconds,
    postRollSeconds: f.postRollSeconds,
    retentionDays: f.retentionDays,
    lowResRetentionDays: f.lowResRetentionDays,
  };
}

// ------------------------------------------------------------- estimates ---

/** GB a day at an ingest rate in kbps (decimal GB, like the disk meters). */
export const gbPerDay = (kbps: number) => (kbps * 1000 * 86400) / 8 / 1e9;

const fmtGB = (gb: number) => (gb >= 10 ? `${Math.round(gb)} GB` : gb >= 1 ? `${gb.toFixed(1)} GB` : `${Math.max(0.1, Math.round(gb * 10) / 10).toFixed(1)} GB`);

/**
 * One line with the disk use per day, from the main stream's live ingest
 * rate (System Monitor's kbps). Empty when the rate isn't known or nothing
 * is recorded: the page shows nothing rather than a guess.
 */
export function storageEstimate(mode: RecordingMode, kbps: number | null | undefined): string {
  if (kbps == null || !Number.isFinite(kbps) || kbps <= 0 || mode === "off") return "";
  const gb = fmtGB(gbPerDay(kbps));
  const rate = kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${Math.round(kbps)} kbps`;
  switch (mode) {
    case "continuous":
      return `About ${gb} a day at the camera's current ${rate}.`;
    case "events":
      return `Up to ${gb} a day at ${rate} if there is activity all day; usually much less.`;
    default:
      return `Full-res: up to ${gb} a day at ${rate} on events, plus the low-res stream (usually a small fraction of that).`;
  }
}

// ---------------------------------------------------------------- health ---

const pad = (n: number) => String(n).padStart(2, "0");

/** "Events only: last kept clip 14:02" / "Events only: no clips yet". */
export function keptClipLabel(at: string | number | null | undefined, now = Date.now()): string {
  const ms = typeof at === "string" ? Date.parse(at) : at && at < 1e12 ? at * 1000 : at;
  if (!ms) return "Events only: no clips yet";
  const d = new Date(ms);
  const sameDay = new Date(now).toDateString() === d.toDateString();
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return `Events only: last kept clip ${sameDay ? hm : `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${hm}`}`;
}
