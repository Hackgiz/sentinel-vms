// Thin typed client for the Sentinel server's /api/v1 endpoints.

export type Role = "Admin" | "Supervisor" | "Operator" | "Viewer";
export const roles: Role[] = ["Admin", "Supervisor", "Operator", "Viewer"];

export interface User {
  id: string;
  name: string;
  role: Role;
  createdAt: number;
}

export interface Camera {
  id: string;
  name: string;
  location: string;
  rtspURL: string;
  subRTSPURL: string;
  username: string;
  hasPassword: boolean;
  recording: boolean;
  retentionDays: number;
  motionLevel: number;
  alertOn: number;
  online: boolean;
  viewers: number;
  liveURL: string;
  status: string;
}

export interface CameraInput {
  name: string;
  location: string;
  rtspURL: string;
  subRTSPURL: string;
  username: string;
  /** undefined = keep the stored password; "" = clear it */
  password?: string;
  recording: boolean;
  retentionDays: number;
  /** Frame rate reported over ONVIF (shown in the iPhone app); omitted = keep. */
  fps?: number;
  /** 0 off, 1 low, 2 medium, 3 high; omitted = keep (new cameras: medium). */
  motionLevel?: number;
  /** 0 auto, 1 any motion, 2 people & vehicles, 3 people only. */
  alertOn?: number;
}

export interface AIStatus {
  enabled: boolean;
  hasKey: boolean;
  dailyLimit: number;
  usedToday: number;
  lastError: string;
  detector: boolean;
  visionModel: string;
  searchModel: string;
}

export interface Evidence {
  id: string;
  caseID: string;
  alertID?: string;
  cameraID: string;
  camera: string;
  title: string;
  range: string;
  size: number;
  sha256: string;
  lockedAt: number;
  lockedBy: string;
}

export interface Alarm {
  id: string;
  cameraID?: string;
  cameraName?: string;
  kind: string;
  title: string;
  detail: string;
  severity: "Critical" | "Warning" | "Info";
  state: string;
  owner: string;
  createdAt: number;
  updatedAt: number;
  lastEventAt: number;
  eventCount: number;
  responseLog: string[];
  hasClip: boolean;
  evidence?: Evidence;
}

export interface DetectionEvent {
  id: string;
  cameraID: string;
  kind: string;
  label?: string;
  score: number;
  createdAt: number;
  hasThumbnail: boolean;
  alarmID?: string;
  description?: string;
  threat?: string;
  anomaly?: boolean;
  reason?: string;
  tags?: string;
}

export interface DiscoveredDevice {
  host: string;
  name: string;
  manufacturer: string;
  model: string;
  serviceURL: string;
  source: "onvif" | "sweep";
  openPorts?: number[];
  added: boolean;
}

export interface DiscoverResult {
  devices: DiscoveredDevice[];
  subnets: string[] | null;
  warnings: string[] | null;
}

export interface OnvifProfile {
  token: string;
  name: string;
  width: number;
  height: number;
  fps: number;
  encoding: string;
  rtspURL: string;
}

export interface ProbeResult {
  details: { manufacturer: string; model: string; firmware: string; serial: string; serviceURL: string; profiles: OnvifProfile[] };
  mainURL?: string;
  subURL?: string;
}

export interface PairingState {
  active: boolean;
  code?: string;
  expiresAt?: number;
  url?: string;
  urls: string[] | null;
  remoteURL?: string;
  payload?: string;
}

export interface PairedDevice {
  id: string;
  name: string;
  pairedBy: string;
  pairedAt: number;
  lastSeenAt: number;
  hasPush: boolean;
}

export interface RemoteStatus {
  available: boolean;
  enabled: boolean;
  state: "off" | "starting" | "connected" | "retrying";
  url?: string;
  error?: string;
}

export interface Segment {
  name: string;
  start: number;
  end: number;
  size: number;
  url: string;
  writing: boolean;
}

export interface AuditEntry {
  id: string;
  time: number;
  user: string;
  area: string;
  action: string;
  detail: string;
}

export interface SystemInfo {
  version: string;
  os: string;
  mediaRunning: boolean;
  mediaError: string;
  recordingsDir: string;
  recordingsBytes: number;
  diskFreeBytes?: number;
  diskTotalBytes?: number;
}

export interface Status {
  setupRequired: boolean;
  version: string;
  user?: User;
}

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly data: Record<string, unknown> | null = null) {
    super(message);
  }
}

/** Set by the app so any 401 drops back to the sign-in screen. */
export let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) {
    if (res.status === 401 && path !== "/login" && path !== "/setup") onUnauthorized();
    throw new ApiError(data?.error ?? `Request failed (${res.status})`, res.status, data);
  }
  return data as T;
}

export const api = {
  status: () => request<Status>("GET", "/status"),
  setup: (name: string, password: string) => request<{ user: User }>("POST", "/setup", { name, password }),
  login: (name: string, password: string) => request<{ user: User }>("POST", "/login", { name, password }),
  logout: () => request<{ ok: boolean }>("POST", "/logout"),

  cameras: () => request<Camera[]>("GET", "/cameras"),
  addCamera: (c: CameraInput) => request<Camera>("POST", "/cameras", c),
  updateCamera: (id: string, c: CameraInput) => request<Camera>("PUT", `/cameras/${id}`, c),
  deleteCamera: (id: string) => request<{ ok: boolean }>("DELETE", `/cameras/${id}`),
  discover: (sweep: boolean) => request<DiscoverResult>("POST", "/discover", { sweep }),
  probeCamera: (host: string, serviceURL: string, username: string, password: string) =>
    request<ProbeResult>("POST", "/discover/probe", { host, serviceURL, username, password }),
  recordings: (id: string) => request<Segment[]>("GET", `/cameras/${id}/recordings`),

  alarms: (all: boolean) => request<Alarm[]>("GET", `/alarms${all ? "?state=all" : ""}`),
  setAlarmState: (id: string, state: string, note = "") => request<Alarm>("POST", `/alarms/${id}/state`, { state, note }),
  lockEvidence: (id: string) => request<Evidence>("POST", `/alarms/${id}/evidence`),
  events: (camera = "", limit = 200) => request<DetectionEvent[]>("GET", `/events?limit=${limit}${camera ? `&camera=${camera}` : ""}`),
  evidence: () => request<Evidence[]>("GET", "/evidence"),

  sendFeedback: (kind: string, message: string, contactEmail: string, includeDiagnostics: boolean) =>
    request<{ ok: boolean }>("POST", "/feedback", { kind, message, contactEmail, includeDiagnostics }),
  ai: () => request<AIStatus>("GET", "/ai"),
  saveAI: (patch: { enabled?: boolean; apiKey?: string; dailyLimit?: number }) => request<AIStatus>("PUT", "/ai", patch),
  aiSearch: (query: string, days = 7) => request<{ answer: string; events: DetectionEvent[]; searched: number }>("POST", "/ai/search", { query, days }),
  aiDigest: (period: "today" | "24h" | "yesterday") => request<{ digest: string; events: number; label: string }>("POST", "/ai/digest", { period }),

  pairing: () => request<PairingState>("GET", "/pairing"),
  startPairing: (url: string) => request<PairingState>("POST", "/pairing", { url }),
  cancelPairing: () => request<PairingState>("DELETE", "/pairing"),
  devices: () => request<PairedDevice[]>("GET", "/devices"),
  revokeDevice: (id: string) => request<{ ok: boolean }>("DELETE", `/devices/${id}`),
  remote: () => request<RemoteStatus>("GET", "/remote"),
  setRemote: (enabled: boolean) => request<RemoteStatus>("PUT", "/remote", { enabled }),

  users: () => request<User[]>("GET", "/users"),
  addUser: (name: string, role: Role, password: string) => request<User>("POST", "/users", { name, role, password }),
  deleteUser: (id: string) => request<{ ok: boolean }>("DELETE", `/users/${id}`),

  settings: () => request<{ defaultRetentionDays: number }>("GET", "/settings"),
  saveSettings: (defaultRetentionDays: number) =>
    request<{ defaultRetentionDays: number }>("PUT", "/settings", { defaultRetentionDays }),
  system: () => request<SystemInfo>("GET", "/system"),
  audit: () => request<AuditEntry[]>("GET", "/audit"),
  verifyAudit: () => request<{ checked: number; intact: boolean; brokenEntryID: string }>("GET", "/audit/verify"),
};

export const can = {
  acknowledge: (u: User) => u.role !== "Viewer",
  manageCameras: (u: User) => u.role === "Admin",
  manageUsers: (u: User) => u.role === "Admin",
  changeSettings: (u: User) => u.role === "Admin",
  viewAudit: (u: User) => u.role === "Admin" || u.role === "Supervisor",
};

export function formatBytes(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}
