// The tab model of the operator workspace: fixed workspace tabs (a list any
// stream appends to; the shell shows the ones a renderer is registered for)
// plus closable tabs for saved views. Pure, so it can be tested.

import type { InsightsLink } from "../admin/insights";
import type { Permission } from "../shared/api";

export type FixedId = "home" | "live" | "map" | "search" | "exports" | "alarms" | "incidents" | "monitor" | "reports" | "insights" | "rules" | "people" | "plates";

export interface FixedWorkspace {
  id: FixedId;
  label: string;
  /**
   * Ctrl+<key>: a digit, or a lowercase letter once the digits ran out.
   * Keys 2 and 4 stay Playback and Events inside the Live tab; B is the sidebar.
   */
  key: number | string;
  /** A count badge on the tab. */
  badge?: "alarms";
  /** WAVE20: the rail's shorter label when the full one doesn't fit 84 px. */
  short?: string;
}

/** Tab order. Append new workspaces here (and register their renderer in Workspace.tsx). */
export const fixedWorkspaces: FixedWorkspace[] = [
  { id: "home", label: "Home", key: "h" }, // WAVE20: the dashboard, opened on sign-in
  { id: "live", label: "Live", key: 1 },
  { id: "map", label: "Map", key: 6 },
  { id: "search", label: "Search", key: 5 },
  { id: "exports", label: "Exports", key: 7 },
  { id: "alarms", label: "Alarm Manager", key: 3, badge: "alarms", short: "Alarms" },
  { id: "incidents", label: "Incidents", key: 8 },
  { id: "monitor", label: "System Monitor", key: 9, short: "Monitor" },
  { id: "reports", label: "Reports", key: 0 },
  { id: "insights", label: "Insights", key: "i" }, // WAVE15
  { id: "rules", label: "Rules", key: "e" }, // WAVE15: "r" would collide with the desktop app's Reload
  { id: "people", label: "People", key: "p" }, // WAVE17
  { id: "plates", label: "Plates", key: "l" }, // WAVE17
];

/** The shortcut as the tab's tooltip shows it. */
export const keyLabel = (w: FixedWorkspace): string => `Ctrl+${typeof w.key === "string" ? w.key.toUpperCase() : w.key}`;

/** Letter keys the workspace keeps for itself (Ctrl+B: sidebar). */
export const reservedLetters = ["b"];

/**
 * WAVE17: letters the desktop app's menu already binds (Edit: undo, redo,
 * cut, copy, paste, select all; View: reload; Window: minimize, close; File:
 * quit). A tab key must not take one.
 */
export const desktopMenuLetters = ["z", "y", "x", "c", "v", "a", "r", "m", "w", "q"];

/**
 * WAVE15: the permission a pane needs; a pane not listed here is shown to
 * everyone signed in. The server enforces the same on every call.
 */
export const paneGate: Partial<Record<FixedId, Permission>> = {
  insights: "viewLive",
  rules: "changeSettings",
  // WAVE17: reads need viewLive; the sections hide their changes without changeSettings.
  people: "viewLive",
  plates: "viewLive",
};

export const permitted = (id: FixedId, can: (p: Permission) => boolean): boolean => {
  const p = paneGate[id];
  return !p || can(p);
};

/**
 * WAVE15: what an Insights link does in the Operator. "Use template" opens the
 * Rules tab on the template, "Check storage" System Monitor; there is no camera
 * screen here, so "Open camera" isn't offered (null).
 */
export type OperatorLink = { tab: "rules"; templateId: string } | { tab: "monitor" };

export function operatorLink(t: InsightsLink): OperatorLink | null {
  switch (t.to) {
    case "rules":
      return { tab: "rules", templateId: t.templateId };
    case "storage":
      return { tab: "monitor" };
    default:
      return null;
  }
}

/** A fixed workspace's id, or `view:<viewId>` for a saved-view tab. */
export type TabId = string;

export const viewTabId = (viewId: string): TabId => `view:${viewId}`;
export const viewIdOf = (tab: TabId): string | null => (tab.startsWith("view:") ? tab.slice(5) : null);

export interface TabsState {
  active: TabId;
  /** Open saved-view tabs, left to right (view ids). */
  views: string[];
}

export const initialTabs: TabsState = { active: "home", views: [] };

/** WAVE20: the rail label. */
export const railLabel = (w: FixedWorkspace): string => w.short ?? w.label;

/** Workspaces every signed-in user has (no renderer to register, no permission). */
const always: FixedId[] = ["home", "live"];

/**
 * WAVE20: a sign-in opens Home; the saved-view tabs stay as they were.
 * (Switching back to a site that is still signed in keeps its last tab.)
 */
export const onSignIn = (s: TabsState): TabsState => activate(s, "home");

const isFixed = (id: string): id is FixedId => fixedWorkspaces.some((w) => w.id === id);

/** The fixed tabs to show: those with a registered renderer, in model order. */
export function visibleFixed(available: ReadonlySet<FixedId>): FixedWorkspace[] {
  return fixedWorkspaces.filter((w) => always.includes(w.id) || available.has(w.id));
}

export const activate = (s: TabsState, tab: TabId): TabsState => (s.active === tab ? s : { ...s, active: tab });

/** Open a saved view in its own tab (or switch to it when open). */
export function openViewTab(s: TabsState, viewId: string): TabsState {
  return { active: viewTabId(viewId), views: s.views.includes(viewId) ? s.views : [...s.views, viewId] };
}

/** Close a view tab; when it was active, the tab to its left (or Live) takes over. */
export function closeViewTab(s: TabsState, viewId: string): TabsState {
  const i = s.views.indexOf(viewId);
  if (i < 0) return s;
  const views = s.views.filter((v) => v !== viewId);
  if (s.active !== viewTabId(viewId)) return { ...s, views };
  return { active: i > 0 ? viewTabId(views[i - 1]) : views.length ? viewTabId(views[0]) : "live", views };
}

/** Drop view tabs whose view is gone, and an active tab that no longer exists. */
export function reconcile(s: TabsState, available: ReadonlySet<FixedId>, viewExists: (id: string) => boolean): TabsState {
  const views = s.views.filter(viewExists);
  const vid = viewIdOf(s.active);
  const ok = vid !== null ? views.includes(vid) : (isFixed(s.active) && (always.includes(s.active) || available.has(s.active)));
  if (ok && views.length === s.views.length) return s;
  return { active: ok ? s.active : "live", views };
}

/** The alarm-count badge text: nothing at zero, "9+" past nine. */
export const badgeText = (n: number): string => (n <= 0 ? "" : n > 9 ? "9+" : String(n));

export type ShortcutAction = { tab: TabId; mode?: "live" | "playback"; events?: boolean };

/**
 * What Ctrl+<digit> does. 1 Live, 2 Playback, 4 Events (both inside the
 * Live tab) and 3/5 Alarms/Search keep their old keys; the other fixed tabs
 * are numbered onward, then lettered. null when nothing is bound (or the tab
 * isn't there).
 */
export function shortcutFor(key: string, available: ReadonlySet<FixedId>): ShortcutAction | null {
  // Lowercase only: Ctrl+Shift+<letter> stays the app's (Ctrl+Shift+I opens dev tools in debug builds of the desktop app).
  if (/^[a-z]$/.test(key)) {
    const w = fixedWorkspaces.find((x) => x.key === key);
    return w && available.has(w.id) ? { tab: w.id } : null;
  }
  if (!/^[1-9]$/.test(key)) return null;
  const n = Number(key);
  if (n === 1) return { tab: "live", mode: "live" };
  if (n === 2) return { tab: "live", mode: "playback" };
  if (n === 4) return { tab: "live", events: true };
  const w = fixedWorkspaces.find((x) => x.key === n);
  return w && available.has(w.id) ? { tab: w.id } : null;
}

/** Read persisted tab state leniently. */
export function parseTabs(raw: string | null): TabsState {
  try {
    const v = JSON.parse(raw ?? "null") as Partial<TabsState> | null;
    if (!v || typeof v !== "object") return initialTabs;
    const views = Array.isArray(v.views) ? [...new Set(v.views.filter((x): x is string => typeof x === "string" && x !== ""))] : [];
    const active = typeof v.active === "string" ? v.active : "live";
    return { active, views };
  } catch {
    return initialTabs;
  }
}

export const serializeTabs = (s: TabsState): string => JSON.stringify(s);

/** Milliseconds until the next whole minute (the wall clock ticks then). */
export const msToNextMinute = (nowMs: number): number => 60_000 - (nowMs % 60_000) || 60_000;
