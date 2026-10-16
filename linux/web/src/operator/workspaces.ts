// The tab model of the operator workspace: fixed workspace tabs (a list any
// stream appends to; the shell shows the ones a renderer is registered for)
// plus closable tabs for saved views. Pure, so it can be tested.

export type FixedId = "live" | "map" | "search" | "exports" | "alarms" | "incidents" | "monitor";

export interface FixedWorkspace {
  id: FixedId;
  label: string;
  /** Ctrl+<key>. Keys 2 and 4 stay Playback and Events inside the Live tab. */
  key: number;
  /** A count badge on the tab. */
  badge?: "alarms";
}

/** Tab order. Append new workspaces here (and register their renderer in Workspace.tsx). */
export const fixedWorkspaces: FixedWorkspace[] = [
  { id: "live", label: "Live", key: 1 },
  { id: "map", label: "Map", key: 6 },
  { id: "search", label: "Search", key: 5 },
  { id: "exports", label: "Exports", key: 7 },
  { id: "alarms", label: "Alarm Manager", key: 3, badge: "alarms" },
  { id: "incidents", label: "Incidents", key: 8 },
  { id: "monitor", label: "System Monitor", key: 9 },
];

/** A fixed workspace's id, or `view:<viewId>` for a saved-view tab. */
export type TabId = string;

export const viewTabId = (viewId: string): TabId => `view:${viewId}`;
export const viewIdOf = (tab: TabId): string | null => (tab.startsWith("view:") ? tab.slice(5) : null);

export interface TabsState {
  active: TabId;
  /** Open saved-view tabs, left to right (view ids). */
  views: string[];
}

export const initialTabs: TabsState = { active: "live", views: [] };

const isFixed = (id: string): id is FixedId => fixedWorkspaces.some((w) => w.id === id);

/** The fixed tabs to show: those with a registered renderer, in model order. */
export function visibleFixed(available: ReadonlySet<FixedId>): FixedWorkspace[] {
  return fixedWorkspaces.filter((w) => w.id === "live" || available.has(w.id));
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
  const ok = vid !== null ? views.includes(vid) : s.active === "live" || (isFixed(s.active) && available.has(s.active));
  if (ok && views.length === s.views.length) return s;
  return { active: ok ? s.active : "live", views };
}

/** The alarm-count badge text: nothing at zero, "9+" past nine. */
export const badgeText = (n: number): string => (n <= 0 ? "" : n > 9 ? "9+" : String(n));

export type ShortcutAction = { tab: TabId; mode?: "live" | "playback"; events?: boolean };

/**
 * What Ctrl+<digit> does. 1 Live, 2 Playback, 4 Events (both inside the
 * Live tab) and 3/5 Alarms/Search keep their old keys; the other fixed tabs
 * are numbered onward. null when nothing is bound (or the tab isn't there).
 */
export function shortcutFor(key: string, available: ReadonlySet<FixedId>): ShortcutAction | null {
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
