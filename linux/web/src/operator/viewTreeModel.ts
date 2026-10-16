// The view tree: two roots, Private (the user's own views) and Shared
// (folders from view_groups, nested by parentId, with shared views in
// them), narrowed by the sidebar's search field. Pure, so it can be tested.

import type { View, ViewGroup } from "../shared/api";

export type TreeNode = { kind: "folder"; id: string; name: string; children: TreeNode[]; viewCount: number } | { kind: "view"; view: View };

export interface TreeRoot {
  id: "private" | "shared";
  label: string;
  nodes: TreeNode[];
  /** Views under the root after the search narrowed it. */
  viewCount: number;
  /** Users without manage rights see the Shared root read-only (lock glyph). */
  readOnly: boolean;
}

const norm = (s: string) => s.trim().toLowerCase();
const has = (text: string | undefined, q: string) => !!text && text.toLowerCase().includes(q);
const byOrderThenName = (a: { sortOrder?: number; name: string }, b: { sortOrder?: number; name: string }) => (a.sortOrder ?? 0) - (b.sortOrder ?? 0) || a.name.localeCompare(b.name, undefined, { sensitivity: "base" });

function prune(nodes: TreeNode[], q: string): TreeNode[] {
  const out: TreeNode[] = [];
  for (const n of nodes) {
    if (n.kind === "view") {
      if (has(n.view.name, q)) out.push(n);
    } else if (has(n.name, q)) {
      out.push(n); // a folder whose own name matches keeps everything in it
    } else {
      const children = prune(n.children, q);
      if (children.length) out.push({ ...n, children, viewCount: countViews(children) });
    }
  }
  return out;
}

const countViews = (nodes: TreeNode[]): number => nodes.reduce((n, x) => n + (x.kind === "view" ? 1 : x.viewCount), 0);

/** The Shared root's nodes: folders nested by parentId (a missing parent counts as top level), then views, each in order. */
export function sharedNodes(groups: ViewGroup[], shared: View[]): TreeNode[] {
  const ids = new Set(groups.map((g) => g.id));
  const build = (parent: string): TreeNode[] => {
    const folders = groups
      .filter((g) => (ids.has(g.parentId) ? g.parentId : "") === parent)
      .sort(byOrderThenName)
      .map((g): TreeNode => {
        const children = build(g.id);
        return { kind: "folder", id: g.id, name: g.name, children, viewCount: countViews(children) };
      });
    const views = shared
      .filter((v) => (v.groupId && ids.has(v.groupId) ? v.groupId : "") === parent)
      .sort(byOrderThenName)
      .map((view): TreeNode => ({ kind: "view", view }));
    return [...folders, ...views];
  };
  return build("");
}

/**
 * Both roots for the sidebar. With a search, only views whose name matches
 * (and the folders leading to them) remain and empty roots drop away;
 * without one the Shared root always shows, so managers can start a folder.
 */
export function buildViewTree(opts: { views: View[]; shared: View[]; groups: ViewGroup[]; query: string; canManage: boolean }): TreeRoot[] {
  const q = norm(opts.query);
  const priv: TreeNode[] = [...opts.views].sort(byOrderThenName).map((view) => ({ kind: "view", view }));
  const sharedAll = sharedNodes(opts.groups, opts.shared);
  const roots: TreeRoot[] = [
    { id: "private", label: "Private", nodes: q ? prune(priv, q) : priv, viewCount: 0, readOnly: false },
    { id: "shared", label: "Shared", nodes: q ? prune(sharedAll, q) : sharedAll, viewCount: 0, readOnly: !opts.canManage },
  ];
  for (const r of roots) r.viewCount = countViews(r.nodes);
  return q ? roots.filter((r) => r.nodes.length) : roots;
}

/** Folder ids in the tree (for expand-all while searching, and move-to pickers). */
export function folderOptions(groups: ViewGroup[]): { id: string; label: string }[] {
  const out: { id: string; label: string }[] = [];
  const walk = (nodes: TreeNode[], prefix: string) => {
    for (const n of nodes) {
      if (n.kind !== "folder") continue;
      out.push({ id: n.id, label: prefix + n.name });
      walk(n.children, `${prefix}${n.name} / `);
    }
  };
  walk(sharedNodes(groups, []), "");
  return out;
}

/** A view by id, private or shared. */
export const findView = (id: string, views: View[], shared: View[]): View | undefined => views.find((v) => v.id === id) ?? shared.find((v) => v.id === id);
