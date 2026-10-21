// Alarm rules + push notification settings (/api/v1/rules, /api/v1/push),
// plus the wave-14 rule drafts and templates on API v2.
import { request } from "./api";
import { v2 } from "./admin/client";
import type { ApiClient, Schemas } from "./shared/api";

export type Delivery = "dashboard" | "push";

/** Alarm kinds a rule can match (the server's rules.Kinds; API v2 has no endpoint for them yet). */
export const ruleKinds = ["Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering", "Tamper", "Camera Offline", "Storage", "audio", "Watchlist"]; // WAVE16: Watchlist (a known person or plate)

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
  mode: "off" | "relay" | "direct" | "cloud"; // WAVE12(C): cloud = Sentinel Cloud's push relay
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
  /** WAVE12(C): linked to Sentinel Cloud with push in the plan. */
  cloudReady?: boolean;
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

export type RuleDraft = Schemas["RuleDraft"];
export type RuleTemplate = Schemas["RuleTemplate"];

/**
 * Wave 14: a rule drafted from plain English (ChangeSettings). Saves nothing;
 * the draft comes back off. 409 when AI is off, 429 at the daily AI limit.
 */
export function draftRule(text: string, client: ApiClient = v2): Promise<RuleDraft> {
  return client.call("post", "/api/v2/rules/draft", { body: { text: text.trim() } });
}

/**
 * Wave 14: ready-made rules to start from. Nothing is created until one is saved.
 * `client`: the Admin's cookie client by default; the Operator passes its bearer client (WAVE15).
 */
export async function listRuleTemplates(client: ApiClient = v2): Promise<RuleTemplate[]> {
  return (await client.call("get", "/api/v2/rule-templates")).items;
}

export const templateCategories: { id: string; label: string }[] = [
  { id: "security", label: "Security" },
  { id: "health", label: "Health" },
  { id: "operations", label: "Operations" },
];

/** Templates by category, in catalog order; a category the page doesn't know goes last. */
export function groupTemplates(items: RuleTemplate[]): { id: string; label: string; items: RuleTemplate[] }[] {
  const known = templateCategories.map((c) => ({ ...c, items: items.filter((t) => t.category === c.id) }));
  const rest = [...new Set(items.map((t) => t.category as string))]
    .filter((id) => !templateCategories.some((c) => c.id === id))
    .map((id) => ({ id, label: id.charAt(0).toUpperCase() + id.slice(1), items: items.filter((t) => t.category === id) }));
  return [...known, ...rest].filter((g) => g.items.length > 0);
}

export const dayNames = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function minuteToTime(m: number): string {
  return `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
}

export function timeToMinute(t: string): number {
  const [h, m] = t.split(":").map(Number);
  return (h || 0) * 60 + (m || 0);
}
