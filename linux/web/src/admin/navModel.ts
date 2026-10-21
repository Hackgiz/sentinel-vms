// Site navigation tree for Sentinel Admin: a declarative catalog of
// categories and leaf pages, filtered by what the signed-in user may see.
// Pure logic (no React) so it is unit-tested. Routing: #/admin/<category>/<leaf>.

/** What reveals a node: "admin" = Admin only; "any" = any signed-in admin-app user (Supervisor included). */
export type Access = "admin" | "any";

export interface SectionInfo {
  id: string;
  label: string;
  adminOnly: boolean;
}

interface CatalogLeaf {
  /** The section id (the `sections` array in AdminPage.tsx). */
  id: string;
  slug: string;
  label: string;
}
interface CatalogCategory {
  slug: string;
  label: string;
  leaves: CatalogLeaf[];
}

/** The target shape from the wave-6 contract. Leaves whose section id is not
 * registered (streams B/C/D not merged yet) simply don't appear. */
export const catalog: CatalogCategory[] = [
  { slug: "basics", label: "Basics", leaves: [
    { id: "overview", slug: "overview", label: "Overview" },
    { id: "insights", slug: "insights", label: "Insights" }, // WAVE14(C)
    { id: "site", slug: "site-information", label: "Site Information" },
    { id: "cloud", slug: "sentinel-cloud", label: "Sentinel Cloud" }, // WAVE12(C)
  ] },
  { slug: "servers", label: "Servers", leaves: [{ id: "servers", slug: "recording-servers", label: "Recording Servers" }] },
  { slug: "devices", label: "Devices", leaves: [
    { id: "cameras", slug: "cameras", label: "Cameras" },
    { id: "groups", slug: "camera-groups", label: "Camera Groups" },
    { id: "zones", slug: "zones-and-privacy", label: "Zones & Privacy" },
  ] },
  { slug: "client", label: "Client", leaves: [{ id: "floorplans", slug: "floorplans", label: "Floorplans" }] },
  { slug: "rules-and-events", label: "Rules and Events", leaves: [
    { id: "rules", slug: "rules", label: "Rules" },
    { id: "timeprofiles", slug: "time-profiles", label: "Time Profiles" },
    { id: "webhooks", slug: "webhooks", label: "Webhooks" },
    { id: "notifications", slug: "notifications", label: "Notifications" },
  ] },
  { slug: "security", label: "Security", leaves: [
    { id: "roles", slug: "roles", label: "Roles" },
    { id: "users", slug: "users", label: "Users" },
    { id: "people", slug: "people", label: "People" }, // WAVE16(F)
    { id: "plates", slug: "plates", label: "Plates" }, // WAVE16(F)
    { id: "directory", slug: "directory", label: "Directory" },
  ] },
  { slug: "system-dashboard", label: "System Dashboard", leaves: [
    { id: "health", slug: "system-monitor", label: "System Monitor" },
    { id: "tasks", slug: "current-tasks", label: "Current Tasks" },
    { id: "evidence", slug: "evidence-locks", label: "Evidence Locks" },
    { id: "report", slug: "configuration-report", label: "Configuration Report" },
  ] },
  { slug: "logs", label: "Logs", leaves: [
    { id: "logs", slug: "server-logs", label: "Server Logs" },
    { id: "audit", slug: "audit", label: "Audit" },
  ] },
];

/** Sections not placed in the catalog land here until rehomed. */
const OTHER: CatalogCategory = { slug: "other", label: "Other", leaves: [] };

export interface NavLeaf {
  kind: "leaf";
  id: string;
  label: string;
  slug: string;
  category: NavCategory;
  access: Access;
  /** "#/admin/security/roles" */
  hash: string;
}
export interface NavCategory {
  kind: "category";
  slug: string;
  label: string;
  leaves: NavLeaf[];
}
export interface NavTree {
  categories: NavCategory[];
}

export const ADMIN_PREFIX = "#/admin";

export function canSee(role: string | undefined, access: Access): boolean {
  return access === "any" || role === "Admin";
}

const slugify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");

/** Build the permission-filtered tree from the registered sections. */
export function buildTree(role: string | undefined, sections: SectionInfo[]): NavTree {
  const byId = new Map(sections.map((s) => [s.id, s]));
  const placed = new Set<string>();
  const categories: NavCategory[] = [];
  const build = (c: CatalogCategory, extra: CatalogLeaf[] = []) => {
    const cat: NavCategory = { kind: "category", slug: c.slug, label: c.label, leaves: [] };
    for (const l of [...c.leaves, ...extra]) {
      const s = byId.get(l.id);
      if (!s) continue;
      placed.add(l.id);
      const access: Access = s.adminOnly ? "admin" : "any";
      if (!canSee(role, access)) continue;
      cat.leaves.push({ kind: "leaf", id: l.id, label: l.label, slug: l.slug, category: cat, access, hash: `${ADMIN_PREFIX}/${c.slug}/${l.slug}` });
    }
    if (cat.leaves.length) categories.push(cat);
  };
  for (const c of catalog) build(c);
  const stray = sections.filter((s) => !placed.has(s.id)).map((s) => ({ id: s.id, slug: slugify(s.id), label: s.label }));
  if (stray.length) build(OTHER, stray);
  return { categories };
}

export const allLeaves = (t: NavTree): NavLeaf[] => t.categories.flatMap((c) => c.leaves);

/** Narrow to matching leaves; a category-name match keeps all its leaves. */
export function filterTree(tree: NavTree, query: string): NavTree {
  const q = query.trim().toLowerCase();
  if (!q) return tree;
  const categories: NavCategory[] = [];
  for (const c of tree.categories) {
    const all = c.label.toLowerCase().includes(q);
    const leaves = c.leaves.filter((l) => all || l.label.toLowerCase().includes(q));
    if (leaves.length) categories.push({ ...c, leaves });
  }
  return { categories };
}

export type Route = { kind: "root" } | { kind: "leaf"; leaf: NavLeaf };

/** "#/admin/security/roles" -> route. Legacy "#/admin/<sectionId>" links
 * resolve too, and a "?…" deep link after either is ignored here (see
 * hashParams). Unknown or hidden -> root welcome. */
export function parseHash(hash: string, tree: NavTree): Route {
  const parts = hash.split("?")[0].replace(/^#\/?/, "").split("/").filter(Boolean);
  if (parts[0] !== "admin") return { kind: "root" };
  const leaves = allLeaves(tree);
  if (parts.length === 2) {
    const legacy = leaves.find((l) => l.id === parts[1]);
    if (legacy) return { kind: "leaf", leaf: legacy };
  }
  const leaf = parts.length >= 3 ? leaves.find((l) => l.category.slug === parts[1] && l.slug === parts[2]) : undefined;
  return leaf ? { kind: "leaf", leaf } : { kind: "root" };
}

export const routeHash = (r: Route): string => (r.kind === "leaf" ? r.leaf.hash : ADMIN_PREFIX);

/** A section's deep-link parameters: "#/admin/rules-and-events/rules?template=tamper" -> template=tamper. */
export function hashParams(hash: string): URLSearchParams {
  const i = hash.indexOf("?");
  return new URLSearchParams(i < 0 ? "" : hash.slice(i + 1));
}

/** Link to a section by id, with optional deep-link parameters. Sections the catalog doesn't place use the legacy form. */
export function sectionHash(id: string, params: Record<string, string> = {}): string {
  const cat = catalog.find((c) => c.leaves.some((l) => l.id === id));
  const leaf = cat?.leaves.find((l) => l.id === id);
  const base = cat && leaf ? `${ADMIN_PREFIX}/${cat.slug}/${leaf.slug}` : `${ADMIN_PREFIX}/${id}`;
  const q = new URLSearchParams(params).toString();
  return q ? `${base}?${q}` : base;
}

export const breadcrumb = (leaf: NavLeaf): string[] => [leaf.category.label, leaf.label];

/** A row of the flattened, keyboard-navigable tree. */
export type Row =
  | { key: string; type: "root" }
  | { key: string; type: "category"; category: NavCategory; expanded: boolean }
  | { key: string; type: "leaf"; leaf: NavLeaf };

export const rootKey = "root";
export const categoryKey = (c: NavCategory) => `cat:${c.slug}`;
export const leafKey = (l: NavLeaf) => `leaf:${l.id}`;

/** Rows in display order. While filtering, every category is expanded. */
export function visibleRows(tree: NavTree, expanded: Record<string, boolean>, filtering: boolean): Row[] {
  const rows: Row[] = [{ key: rootKey, type: "root" }];
  for (const c of tree.categories) {
    const open = filtering || expanded[c.slug] !== false;
    rows.push({ key: categoryKey(c), type: "category", category: c, expanded: open });
    if (open) for (const l of c.leaves) rows.push({ key: leafKey(l), type: "leaf", leaf: l });
  }
  return rows;
}

/** Arrow-key movement, clamped. */
export function moveFocus(rows: Row[], key: string, delta: 1 | -1): string {
  const i = rows.findIndex((r) => r.key === key);
  const n = Math.min(rows.length - 1, Math.max(0, (i < 0 ? 0 : i) + delta));
  return rows[n].key;
}

const STORE = "sentinel.admin.nav.expanded";
export function loadExpanded(): Record<string, boolean> {
  try {
    const v = JSON.parse(localStorage.getItem(STORE) ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}
export function saveExpanded(e: Record<string, boolean>) {
  try {
    localStorage.setItem(STORE, JSON.stringify(e));
  } catch {
    /* storage unavailable: state is per-session */
  }
}

/** Site name from GET /api/v2/site, with the graceful fallback. */
export const siteTitle = (name: unknown): string => (typeof name === "string" && name.trim() ? name.trim() : "Sentinel");
