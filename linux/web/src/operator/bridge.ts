// What the desktop app (linux/desktop, preload.ts) exposes to the operator
// UI as `window.sentinelDesktop`, and the plain-browser fallbacks. Keep the
// interface in step with linux/desktop/src/preload.ts.

import type { TokenPair } from "../shared/api";
import { parseSites, type Site } from "./sites";

/** A site certificate the desktop app refused, for the trust prompt. */
export interface PendingCertificate {
  host: string;
  /** SHA-256 of the certificate, uppercase hex pairs joined by ":". */
  fingerprint: string;
  subject: string;
  issuer: string;
  validFrom: number;
  validTo: number;
  /** Chromium's reason, e.g. "net::ERR_CERT_AUTHORITY_INVALID". */
  error: string;
}

export interface DisplayInfo {
  id: number;
  label: string;
  primary: boolean;
  bounds: { x: number; y: number; width: number; height: number };
}

/** A new-alarm desktop notification (PARITY-342). */
export interface AlarmNotice {
  alarmId: string;
  title: string;
  body: string;
  /** Critical urgency (Critical severity, or a Watchlist alarm). */
  critical: boolean;
}

/** What a pop-out window shows. */
export type PopoutTarget = { kind: "camera"; siteId: string; cameraId: string; title?: string } | { kind: "view"; siteId: string; viewId: string; title?: string };

export interface DesktopBridge {
  version: string;
  platform: string;
  sites: {
    list(): Promise<unknown>;
    save(sites: Site[]): Promise<void>;
  };
  /** Token pairs per site, encrypted at rest by the OS keyring (safeStorage). */
  tokens: {
    load(siteId: string): Promise<TokenPair | null>;
    save(siteId: string, tokens: TokenPair | null): Promise<void>;
    /** Another window changed a site's tokens. */
    onChange(fn: (siteId: string, tokens: TokenPair | null) => void): () => void;
    /** Whether tokens are encrypted on disk (false: kept for this run only). */
    encrypted(): Promise<boolean>;
  };
  /** The site the window works with: the only origin it may reach besides the app. */
  setSite(address: string | null): Promise<void>;
  certificates: {
    /** The certificate last refused for this site's host, if any. */
    pending(address: string): Promise<PendingCertificate | null>;
    /** Pin this certificate for the site's host. */
    trust(address: string, fingerprint: string): Promise<void>;
    forget(address: string): Promise<void>;
    pinned(address: string): Promise<string | null>;
  };
  /** Native single sign-on: opens the system browser, waits for the loopback redirect. */
  oidcSignIn(address: string): Promise<{ code: string }>;
  oidcCancel(): Promise<void>;
  windows: {
    open(target: PopoutTarget): Promise<void>;
    displays(): Promise<DisplayInfo[]>;
    /** Full screen on a display (null: leave full screen). */
    fullscreen(displayId: number | null): Promise<void>;
  };
  /** PARITY-342: native notifications for new alarms. */
  notifications: {
    /** Show one (the main process skips it while the calling window has focus). */
    alarm(notice: AlarmNotice): Promise<void>;
    /** The user clicked an alarm notification: the window is focused; open that alarm. */
    onOpenAlarm(fn: (alarmId: string) => void): () => void;
  };
}

declare global {
  interface Window {
    sentinelDesktop?: DesktopBridge;
  }
}

export const desktop: DesktopBridge | null = typeof window !== "undefined" ? (window.sentinelDesktop ?? null) : null;

const sitesKey = "operator.sites";

/** Saved sites from the desktop app, else this browser's localStorage. */
export async function loadSites(): Promise<Site[]> {
  if (desktop) return parseSites(await desktop.sites.list());
  try {
    return parseSites(JSON.parse(localStorage.getItem(sitesKey) ?? "[]"));
  } catch {
    return [];
  }
}

export async function saveSites(sites: Site[]): Promise<void> {
  if (desktop) return desktop.sites.save(sites);
  try {
    localStorage.setItem(sitesKey, JSON.stringify(sites));
  } catch {
    /* storage blocked: the list lasts for this page */
  }
}
