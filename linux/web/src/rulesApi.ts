// Alarm rules + push notification settings (/api/v1/rules, /api/v1/push).
import { request } from "./api";

export type Delivery = "dashboard" | "push";

export interface AlarmRule {
  id: string;
  name: string;
  enabled: boolean;
  /** empty = every camera */
  cameraIDs: string[];
  /** empty = any alarm */
  kinds: string[];
  minSeverity: "Critical" | "Warning" | "Info";
  schedule: boolean;
  /** minutes after local midnight; toMinute < fromMinute runs overnight */
  fromMinute: number;
  toMinute: number;
  /** 0 = Sunday … 6 = Saturday; empty = every day */
  days: number[];
  delivery: Delivery;
  instructions: string;
  createdAt?: number;
  updatedAt?: number;
}

export interface RulesResponse {
  rules: AlarmRule[];
  kinds: string[];
  severities: string[];
  timeZone: string;
  zoneAbbrev: string;
}

export interface PushStatus {
  mode: "off" | "relay" | "direct";
  relayURL: string;
  relayRegistered: boolean;
  apnsKeyID: string;
  apnsTeamID: string;
  apnsTopic: string;
  apnsHasKey: boolean;
  ready: boolean;
  quietEnabled: boolean;
  quietFrom: number;
  quietTo: number;
  devices: number;
  pushDevices: number;
  sent: number;
  failed: number;
  dropped: number;
  lastSentAt?: number;
  lastError?: string;
}

export interface PushPatch {
  mode?: PushStatus["mode"];
  relayURL?: string;
  apnsKey?: string;
  apnsKeyID?: string;
  apnsTeamID?: string;
  apnsTopic?: string;
  quietEnabled?: boolean;
  quietFrom?: number;
  quietTo?: number;
}

export const rulesApi = {
  list: () => request<RulesResponse>("GET", "/rules"),
  create: (r: Omit<AlarmRule, "id">) => request<AlarmRule>("POST", "/rules", r),
  update: (r: AlarmRule) => request<AlarmRule>("PUT", `/rules/${r.id}`, r),
  remove: (id: string) => request<{ ok: boolean }>("DELETE", `/rules/${id}`),
  addSuggested: () => request<AlarmRule[]>("POST", "/rules/suggested"),
  push: () => request<PushStatus>("GET", "/push"),
  savePush: (p: PushPatch) => request<PushStatus>("PUT", "/push", p),
  testPush: () => request<{ sent: number; failed: string[] }>("POST", "/push/test"),
};

export const dayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function minuteToTime(m: number): string {
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

export function timeToMinute(t: string): number {
  const [h, m] = t.split(":").map(Number);
  return (h || 0) * 60 + (m || 0);
}
