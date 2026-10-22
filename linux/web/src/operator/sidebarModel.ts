// What the sidebar shows: saved views and cameras (by recording server or by
// group), narrowed by the search field. Pure, so it can be tested.

import type { Camera, CameraGroup, RecordingServer, View } from "../shared/api";

export type Grouping = "server" | "group";

export interface CameraSection {
  /** Server or group id; "" for cameras without a group. */
  id: string;
  name: string;
  /** Servers only: whether the recording server is online. */
  online?: boolean;
  cameras: Camera[];
  /** Cameras in the section before the search narrowed it. */
  total: number;
}

const norm = (s: string) => s.trim().toLowerCase();
const has = (text: string | undefined, q: string) => !!text && text.toLowerCase().includes(q);

export function filterViews(views: View[], query: string): View[] {
  const q = norm(query);
  return views.filter((v) => !q || has(v.name, q));
}

/**
 * Cameras grouped for the tree. A search matches camera names and
 * locations; a match on the server's or group's own name keeps all its
 * cameras. Sections left empty by a search are dropped; with no search,
 * servers and groups with no cameras still show (a server row is useful
 * even when empty).
 */
export function cameraSections(cameras: Camera[], servers: RecordingServer[], groups: CameraGroup[], query: string, grouping: Grouping): CameraSection[] {
  const q = norm(query);
  const matchCam = (c: Camera) => has(c.name, q) || has(c.location, q);
  const narrow = (name: string, cams: Camera[]) => (!q || has(name, q) ? cams : cams.filter(matchCam));
  const out: CameraSection[] = [];
  if (grouping === "server") {
    const listed = activeServers(servers);
    const known = new Set(listed.map((s) => s.id));
    for (const s of [...listed].sort((a, b) => a.name.localeCompare(b.name))) {
      const all = cameras.filter((c) => c.recorderId === s.id);
      out.push({ id: s.id, name: s.name, online: s.online, cameras: narrow(s.name, all), total: all.length });
    }
    const stray = cameras.filter((c) => !known.has(c.recorderId));
    if (stray.length) out.push({ id: "", name: "Other cameras", cameras: narrow("Other cameras", stray), total: stray.length });
  } else {
    const grouped = new Set<string>();
    for (const g of [...groups].sort((a, b) => a.name.localeCompare(b.name))) {
      const all = cameras.filter((c) => g.cameraIds.includes(c.id) || c.groupIds?.includes(g.id));
      all.forEach((c) => grouped.add(c.id));
      out.push({ id: g.id, name: g.name, cameras: narrow(g.name, all), total: all.length });
    }
    const rest = cameras.filter((c) => !grouped.has(c.id));
    if (rest.length) out.push({ id: "", name: out.length ? "Other cameras" : "Cameras", cameras: narrow("Other cameras", rest), total: rest.length });
  }
  return q ? out.filter((s) => s.cameras.length) : out;
}

/**
 * Recording servers the tree lists: revoked ones (removed recorders) are
 * hidden. Their cameras, if any are still assigned, fall to "Other cameras".
 * The Admin page lists every server, revoked included.
 */
export const activeServers = (servers: RecordingServer[]) => servers.filter((s) => !s.revoked);

/** The Exports workspace's entry in the sidebar footer (wave 4). */
export const exportsNav = { panel: "exports", label: "Exports", title: "Exports and evidence locks" } as const;

/** Whether the user's scope has any camera groups (the by-group toggle only shows then). */
export const hasGroups = (groups: CameraGroup[]) => groups.length > 0;

/** Sidebar state remembered per site. */
export interface SidebarState {
  width: number;
  collapsed: boolean;
  grouping: Grouping;
  /** Section ids folded in the tree, prefixed "s:" (server/group) or "v" (the Views section). */
  folded: string[];
}

export const sidebarDefaults: SidebarState = { width: 260, collapsed: false, grouping: "server", folded: [] };
export const sidebarMinWidth = 200;
export const sidebarMaxWidth = 520;

/** Width of the collapsed strip (a chevron and a vertical label). */
export const sidebarCollapsedWidth = 28;

/** localStorage key of the sidebar state, per site. */
export const sidebarStorageKey = (siteId: string) => `operator.sidebar.${siteId}`;

/** The sidebar's rendered width. */
export const sidebarWidth = (s: Pick<SidebarState, "width" | "collapsed">) => (s.collapsed ? sidebarCollapsedWidth : clampWidth(s.width));

/** Ctrl+B and the chevrons: flip collapsed, keep everything else (width included). */
export const toggleCollapsed = (s: SidebarState): SidebarState => ({ ...s, collapsed: !s.collapsed });

export const clampWidth = (w: number) => Math.min(sidebarMaxWidth, Math.max(sidebarMinWidth, Math.round(w)));

/** Read saved sidebar state leniently. */
export function parseSidebarState(raw: string | null): SidebarState {
  try {
    const v = JSON.parse(raw ?? "null") as Partial<SidebarState> | null;
    if (!v || typeof v !== "object") return sidebarDefaults;
    return {
      width: typeof v.width === "number" && Number.isFinite(v.width) ? clampWidth(v.width) : sidebarDefaults.width,
      collapsed: v.collapsed === true,
      grouping: v.grouping === "group" ? "group" : "server",
      folded: Array.isArray(v.folded) ? v.folded.filter((x): x is string => typeof x === "string") : [],
    };
  } catch {
    return sidebarDefaults;
  }
}
