// Wave 23: iPhone cameras (the phone pushes its camera to Sentinel) and
// videos uploaded from the iPhone into the evidence vault. The generated
// api/v2.d.ts does not carry these fields yet (stream H adds them), so the
// shapes live here and are joined onto the generated types with
// intersections at the API boundary, like recordingModes.ts.
// TODO(wave23 merge): swap for the generated types once v2.d.ts is regenerated.

import type { ApiClient, Camera, Evidence, EvidenceCase } from "./api";

// ---------------------------------------------------------------- cameras ---

export type CameraSource = "rtsp" | "phone";

/** The camera fields wave 23 adds (absent on older servers: then the camera is RTSP). */
export interface PhoneCameraFields {
  source?: CameraSource;
  /** Phone cameras: the device streaming now, or the last one that did ("EJ's iPhone"). */
  phoneDevice?: string;
  /** Phone cameras: when the last segment arrived (Unix ms; RFC 3339 and Unix s tolerated). Absent = never. */
  lastSeenAt?: number | string | null;
  /** Phone cameras: false once the push key is revoked. Absent = has a key. */
  hasPushKey?: boolean;
  /** What the server actually sends for phone cameras (wins over the flat fields above). */
  phone?: { streaming: boolean; device?: string; lastSeenAt?: number; hasPushKey: boolean };
}
export type SourcedCamera = Camera & PhoneCameraFields;

/** POST /api/v2/phone-cameras (the iPhone calls it; the web never does). The key is shown once. */
export interface PhoneCameraCreated {
  camera: SourcedCamera;
  pushKey: string;
}

/** POST /api/v2/cameras/{id}/push-key: a new key; the old one stops at once. */
export interface PushKeyRotated {
  pushKey: string;
}

export const isPhone = (c: object | null | undefined): boolean => (c as { source?: string } | null | undefined)?.source === "phone";

const enc = encodeURIComponent;

export const phoneCameraApi = {
  create: (c: ApiClient, body: { name: string; recorderId?: string }) => c.request<PhoneCameraCreated>("post", "/api/v2/phone-cameras", body),
  rotateKey: (c: ApiClient, cameraId: string) => c.request<PushKeyRotated>("post", `/api/v2/cameras/${enc(cameraId)}/push-key`),
  revokeKey: (c: ApiClient, cameraId: string) => c.request<void>("delete", `/api/v2/cameras/${enc(cameraId)}/push-key`),
};

/** Status texts the server sends for phone cameras (health and camera status). */
export const PHONE_NOT_STREAMING = "Phone not streaming";
export const streamingFrom = (device: string) => (device ? `Streaming from ${device}` : "Streaming");
/** Addendum 2: the site's Cloud subscription ended, so the phone can't get a push token. */
export const NEEDS_CLOUD = "Needs Sentinel Cloud";
export const CLOUD_URL = "https://sentvms.com/cloud";
export const needsCloud = (c: { status?: { online?: boolean; message?: string } }): boolean =>
  !c.status?.online && (c.status?.message ?? "").trim().toLowerCase() === NEEDS_CLOUD.toLowerCase();

/** Addendum 2: GET /api/v2/cloud gains these (absent on older servers: unknown). */
export interface CloudAvailability {
  phoneCamerasAvailable?: boolean;
  uploadsAvailable?: boolean;
}

/** Unix ms of a time the server may send as ms, s or RFC 3339; 0 when absent or unreadable. */
export function toMs(at: number | string | null | undefined): number {
  if (at == null || at === "") return 0;
  if (typeof at === "string") {
    const t = Date.parse(at);
    return Number.isFinite(t) ? t : 0;
  }
  if (!Number.isFinite(at) || at <= 0) return 0;
  return at < 1e12 ? at * 1000 : at;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** "14:02" today, "Oct 21 14:02" before. */
export function shortTime(ms: number, now = Date.now()): string {
  const d = new Date(ms);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return new Date(now).toDateString() === d.toDateString() ? hm : `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${hm}`;
}

export interface PhoneStatus {
  streaming: boolean;
  /** "Streaming" / "Not streaming" / "Needs Sentinel Cloud" / "Key revoked". */
  state: string;
  /** "last seen 14:02" / "never connected" / the device; "" when nothing to add. */
  detail: string;
}

/** What the camera list and the editor say about a phone camera. */
export function phoneStatus(c: Pick<SourcedCamera, "status" | "phoneDevice" | "lastSeenAt" | "hasPushKey" | "phone">, now = Date.now()): PhoneStatus {
  const streaming = c.phone?.streaming ?? !!c.status?.online;
  const device = c.phone?.device ?? c.phoneDevice;
  if ((c.phone?.hasPushKey ?? c.hasPushKey) === false) return { streaming: false, state: "Key revoked", detail: "set it up again on the iPhone" };
  if (streaming) return { streaming, state: "Streaming", detail: device ? `from ${device}` : "" };
  const seen = toMs(c.lastSeenAt ?? c.phone?.lastSeenAt);
  if (needsCloud(c)) return { streaming, state: NEEDS_CLOUD, detail: seen ? `last seen ${shortTime(seen, now)}` : "" };
  return { streaming, state: "Not streaming", detail: seen ? `last seen ${shortTime(seen, now)}` : "never connected" };
}

/** One line: "iPhone camera · Not streaming · last seen 14:02". */
export function phoneStatusLine(c: Parameters<typeof phoneStatus>[0], now = Date.now()): string {
  const s = phoneStatus(c, now);
  return ["iPhone camera", s.state, s.detail].filter(Boolean).join(" · ");
}

/** A live tile's offline text: phone cameras say so plainly. */
export function offlineText(c: { status?: { message?: string } }): string {
  if (isPhone(c)) return needsCloud(c) ? NEEDS_CLOUD : PHONE_NOT_STREAMING;
  return c.status?.message || "Offline";
}

/** Copy shared by the Add camera dialog. */
export const phoneSetupSteps = ["Open Sentinel on the iPhone.", "Go to Settings → Use this iPhone as a camera.", "Pick a name and tap Start."];

// --------------------------------------------------------------- evidence ---

export type EvidenceSource = "export" | "alarm" | "upload";

/** The evidence fields wave 23 adds (absent on older servers: an export). */
export interface EvidenceUploadFields {
  source?: EvidenceSource;
  /** Uploads: the user who sent it. */
  uploadedBy?: string;
  /** Uploads: the device it came from ("iPhone 16 Pro Max"). */
  device?: string;
  /** Uploads: when it was filmed (Unix ms; RFC 3339 and Unix s tolerated); absent = unknown. */
  recordedAt?: number | string | null;
  /** Uploads: the original file name. */
  fileName?: string;
}
export type SourcedEvidence = Evidence & EvidenceUploadFields;
export type SourcedEvidenceCase = EvidenceCase & EvidenceUploadFields;

export const isUpload = (e: object | null | undefined): boolean => (e as { source?: string } | null | undefined)?.source === "upload";

/** "Uploaded from iPhone" (or iPad, by the device name). */
export function uploadedFromLabel(device?: string): string {
  return /ipad/i.test(device ?? "") ? "Uploaded from iPad" : "Uploaded from iPhone";
}

/** The lines an uploaded video shows under its label: who, which device, when filmed. */
export function uploadDetails(e: EvidenceUploadFields, fmt: (ms: number) => string = (ms) => new Date(ms).toLocaleString()): string[] {
  const out: string[] = [];
  if (e.uploadedBy) out.push(`by ${e.uploadedBy}`);
  if (e.device) out.push(e.device);
  const at = toMs(e.recordedAt);
  out.push(at ? `recorded ${fmt(at)}` : "recording time unknown");
  return out;
}

/** What an uploaded video stands for in the camera column: its camera when attached, else its file. */
export function uploadSubject(e: Pick<SourcedEvidence, "cameraName" | "title" | "fileName">): string {
  return e.cameraName || e.title || e.fileName || "Video";
}

// ---------------------------------------------------------------- playing ---

/** Largest file the Operator loads into memory to play in place; bigger ones are downloaded. */
export const PLAY_IN_PLACE_MAX = 1024 * 1024 * 1024;
export const canPlayInPlace = (size: number) => size > 0 && size <= PLAY_IN_PLACE_MAX;
