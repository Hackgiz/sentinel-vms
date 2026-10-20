// Sentinel Cloud: the server API calls and the labels behind the Sentinel
// Cloud card (pages/CloudPage.tsx; wave 12's page, rebuilt in wave 13).
// Typed from the generated contract (WAVE12-CLOUD.md section 4); wave 13's
// `signIn` field and `/cloud/refresh` route are decoded tolerantly
// (WAVE13-CLOUD-ONBOARDING.md, "Server API v2 additions").
import type { components } from "../api/v2";
import { onUnauthorized } from "../api";
import { parseServerError } from "../cloudAccount";
import { ApiError, errorFromResponse } from "../shared/api";
import { v2 } from "./client";

type S = components["schemas"];
export type CloudStatus = S["CloudStatus"];
export type CloudSettings = S["CloudSettings"];
export type RemoteState = CloudStatus["remote"]["state"];
export type PushState = CloudStatus["push"]["state"];
/** WAVE13: `GET /api/v2/cloud` also carries `signIn`, the control plane's public config (or null). */
export type CloudStatusWithSignIn = CloudStatus & { signIn?: unknown };

/** Where the owner subscribes, approves link codes and manages billing. */
export const MANAGE_URL = "https://sentvms.com/cloud";

/** A failed wave 13 cloud call; `upgradeURL` comes with `cloud_server_limit`. */
export class CloudError extends ApiError {
  readonly upgradeURL: string;
  constructor(status: number, code: string, message: string, upgradeURL = "") {
    super(status, code, message);
    this.name = "CloudError";
    this.upgradeURL = upgradeURL;
  }
}

/**
 * POST to `/api/v2/cloud/link` (with a JWT) or `/api/v2/cloud/refresh`. Not
 * through v2.call: there any 401 means the Sentinel session is gone and signs
 * the page out, but `401 {code:"cloud_sign_in"}` here only means Sentinel
 * Cloud refused the account sign-in.
 */
async function cloudPost(path: string, body?: unknown): Promise<CloudStatusWithSignIn> {
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      // State-changing cookie requests must be JSON (the server's CSRF rule), even without a body.
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new CloudError(0, "network", "Can't reach the server. Check the connection.");
  }
  const text = await res.text();
  if (res.ok) return JSON.parse(text) as CloudStatusWithSignIn;
  const e = parseServerError(res.status, text);
  if (res.status === 401 && e.code !== "cloud_sign_in") onUnauthorized();
  const base = errorFromResponse(res.status, text);
  throw new CloudError(res.status, e.code || base.code, e.message || base.message, e.upgradeURL);
}

export const cloudApi = {
  get: () => v2.call("get", "/api/v2/cloud") as Promise<CloudStatusWithSignIn>,
  put: (body: CloudSettings) => v2.call("put", "/api/v2/cloud", { body }),
  /** Wave 12's code flow (an empty body); still answered by the server. */
  link: () => v2.call("post", "/api/v2/cloud/link"),
  unlink: () => v2.call("delete", "/api/v2/cloud/link"),
  /** WAVE13: link with the account's Supabase session JWT; the server uses it once and forgets it. */
  linkAccount: (accessToken: string) => cloudPost("/api/v2/cloud/link", { accessToken }),
  /** WAVE13: re-read the plan from Sentinel Cloud now (the server allows one every 10 s). */
  refresh: () => cloudPost("/api/v2/cloud/refresh"),
  /** The outside address (viewLive), for people who can't read the full status. */
  remote: () => v2.call("get", "/api/v2/cloud/remote"),
};

export function planLabel(plan: string | undefined): string {
  switch (plan) {
    case "cloud":
      return "Sentinel Cloud";
    case "business":
      return "Sentinel Cloud Business";
    default:
      return "Free";
  }
}

export function statusLabel(status: string | undefined): string {
  switch (status) {
    case "active":
      return "Active";
    case "grace":
      return "Payment due";
    case "canceled":
      return "Canceled";
    default:
      return "Not active";
  }
}

export type Tone = "green" | "amber" | "red" | "";

export function statusTone(status: string | undefined): Tone {
  return status === "active" ? "green" : status === "grace" ? "amber" : status === "canceled" ? "red" : "";
}

const remoteLabels: Record<RemoteState, string> = {
  off: "Off",
  connecting: "Connecting…",
  connected: "Connected",
  retrying: "Reconnecting…",
  inactive: "Not active",
};

export const remoteStateLabel = (s: RemoteState | undefined): string => (s && remoteLabels[s]) || "Off";

export function remoteTone(s: RemoteState | undefined): Tone {
  switch (s) {
    case "connected":
      return "green";
    case "connecting":
    case "retrying":
      return "amber";
    case "inactive":
      return "red";
    default:
      return "";
  }
}

export function pushStateLabel(s: PushState | undefined): string {
  switch (s) {
    case "cloud":
      return "Through Sentinel Cloud";
    case "direct":
      return "Direct to Apple with your own key";
    default:
      return "Off";
  }
}

/** Binary units, as the plans are sold (10 GiB, 100 GiB). */
export function formatGiB(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n) || n < 0) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  const digits = i === 0 || n >= 10 || Number.isInteger(n) ? 0 : 1;
  return `${n.toFixed(digits)} ${units[i]}`;
}

/** The backup usage bar: percent (0–100, clamped) and its caption. */
export function usage(bytesUsed: number, quota: number): { pct: number; label: string; tone: Tone } {
  if (!quota || quota <= 0) return { pct: 0, label: `${formatGiB(bytesUsed)} used`, tone: "" };
  const raw = (bytesUsed / quota) * 100;
  const pct = Math.min(100, Math.max(0, raw));
  return { pct, label: `${formatGiB(bytesUsed)} of ${formatGiB(quota)}`, tone: raw >= 95 ? "red" : raw >= 80 ? "amber" : "" };
}

/** "9:41" until the link code expires; "" once it has. */
export function countdown(expiresAt: string | undefined, now: number): string {
  const end = expiresAt ? Date.parse(expiresAt) : NaN;
  if (!Number.isFinite(end)) return "";
  const left = Math.floor((end - now) / 1000);
  if (left <= 0) return "";
  return `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
}

export type Phase = "unlinked" | "pending" | "linked" | "expired";

/** Where the page is. "expired": a code was showing and the server
 * neither linked nor still has it (it expired or was cancelled elsewhere). */
export function phase(s: Pick<CloudStatus, "linked" | "link"> | null | undefined, wasPending: boolean): Phase {
  if (!s) return "unlinked";
  if (s.linked) return "linked";
  if (s.link) return "pending";
  return wasPending ? "expired" : "unlinked";
}

/** How often to re-read the status: every 5 s while a code is pending,
 * every 30 s while linked (relay and backup state), else not at all. */
export function pollInterval(p: Phase): number | null {
  return p === "pending" ? 5_000 : p === "linked" ? 30_000 : null;
}

const day = 24 * 60 * 60 * 1000;

/** The renewal line under the plan. */
export function periodLabel(status: string | undefined, periodEnd: string | null | undefined): string {
  const end = periodEnd ? Date.parse(periodEnd) : NaN;
  if (!Number.isFinite(end)) return "";
  const fmt = (t: number) => new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
  switch (status) {
    case "active":
      return `Renews ${fmt(end)}`;
    case "grace":
      return `Payment is overdue. Features stay on until ${fmt(end + 7 * day)}.`;
    case "canceled":
      return `Ended ${fmt(end)}`;
    default:
      return "";
  }
}

/** "Not yet" or a local date and time. */
export function lastUpload(at: string | null | undefined): string {
  const t = at ? Date.parse(at) : NaN;
  return Number.isFinite(t) ? new Date(t).toLocaleString() : "Not yet";
}

export const hasFeature = (s: Pick<CloudStatus, "features"> | null | undefined, f: "relay" | "push" | "backup"): boolean =>
  !!s?.features?.includes(f);
