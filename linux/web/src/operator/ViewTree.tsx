// The hierarchical view tree in the sidebar: a Private root (the user's own
// views) and a Shared root (folders from view_groups, shared views). A click
// opens a view in the Live grid, a double-click (or the menu) in its own
// tab. Without manage rights the Shared root is read-only (lock glyph).
import { useEffect, useMemo, useState } from "react";
import type { View, ViewGroup } from "../shared/api";
import { useData } from "./data";
import { useSigned } from "./session";
import type { SidebarState } from "./sidebarModel";
import { buildViewTree, type TreeNode, type TreeRoot } from "./viewTreeModel";

export interface ViewTreeProps {
  query: string;
  state: SidebarState;
  onState: (fn: (s: SidebarState) => SidebarState) => void;
  /** The view open in the Live grid, and whether the grid differs from it. */
  viewId: string | null;
  dirty: boolean;
  onOpenView: (v: View) => void;
  onOpenTab: (v: View) => void;
  onRenameView: (v: View) => void;
  onDeleteView: (v: View) => void;
  onUpdateView: (v: View) => void;
  onMoveView: (v: View) => void;
  onSaveAs: () => void;
  onNewFolder: (parentId: string) => void;
  onRenameFolder: (g: ViewGroup) => void;
  onDeleteFolder: (g: ViewGroup) => void;
}

type Menu = { x: number; y: number; view?: View; folder?: { id: string; name: string }; root?: "shared" };

const Lock = () => (
  <svg className="op-lock" width="11" height="11" viewBox="0 0 16 16" role="img" aria-label="Read-only" fill="currentColor">
    <path d="M4 7V5a4 4 0 118 0v2h1v8H3V7h1zm2 0h4V5a2 2 0 10-4 0v2z" />
  </svg>
);

export default function ViewTree(p: ViewTreeProps) {
  const data = useData();
  const { can } = useSigned();
  const canManage = can("manageViews");
  const [menu, setMenu] = useState<Menu | null>(null);
  const searching = p.query.trim() !== "";
  const roots = useMemo(() => buildViewTree({ views: data.views, shared: data.sharedViews, groups: data.viewGroups, query: p.query, canManage }), [data.views, data.sharedViews, data.viewGroups, p.query, canManage]);
  const folded = new Set(p.state.folded);
  const isOpen = (key: string) => searching || !folded.has(key);
  const toggle = (key: string) => p.onState((s) => ({ ...s, folded: s.folded.includes(key) ? s.folded.filter((x) => x !== key) : [...s.folded, key] }));
  const groupById = useMemo(() => new Map(data.viewGroups.map((g) => [g.id, g])), [data.viewGroups]);

  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    window.addEventListener("pointerdown", close);
    window.addEventListener("keydown", close);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("pointerdown", close);
      window.removeEventListener("keydown", close);
      window.removeEventListener("blur", close);
    };
  }, [menu]);

  const canEdit = (v: View) => !v.shared || canManage;
  const openMenu = (e: React.MouseEvent, m: Omit<Menu, "x" | "y">) => {
    e.preventDefault();
    e.stopPropagation();
    setMenu({ ...m, x: e.clientX, y: e.clientY });
  };
  const moreAt = (e: React.MouseEvent, m: Omit<Menu, "x" | "y">) => {
    e.stopPropagation();
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    setMenu({ ...m, x: r.left, y: r.bottom });
  };

  const renderNode = (n: TreeNode, depth: number, root: TreeRoot): React.ReactNode => {
    const pad = { paddingLeft: 12 + depth * 14 };
    if (n.kind === "folder") {
      const key = `f:${n.id}`;
      const open = isOpen(key);
      return (
        <div key={key}>
          <div className="op-view op-folder" onContextMenu={(e) => canManage && openMenu(e, { folder: { id: n.id, name: n.name } })}>
            <button className="op-view-main" style={pad} aria-expanded={open} onClick={() => toggle(key)}>
              <span className="op-caret">{open ? "▾" : "▸"}</span>
              <span className="grow">{n.name}</span>
              <span className="muted small">{n.viewCount}</span>
            </button>
            {canManage && (
              <button className="btn small op-view-more" aria-label={`Actions for folder ${n.name}`} onClick={(e) => moreAt(e, { folder: { id: n.id, name: n.name } })} onPointerDown={(e) => e.stopPropagation()}>
                ⋯
              </button>
            )}
          </div>
          {open && n.children.map((c) => renderNode(c, depth + 1, root))}
        </div>
      );
    }
    const v = n.view;
    return (
      <div key={v.id} className={`op-view ${p.viewId === v.id ? "on" : ""}`} onContextMenu={(e) => openMenu(e, { view: v })}>
        <button className="op-view-main" style={pad} onClick={() => p.onOpenView(v)} onDoubleClick={() => p.onOpenTab(v)} title={`Open ${v.name} (${v.columns}×${v.rows}); double-click for a new tab`}>
          <span className="op-view-icon" aria-hidden="true">▦</span>
          <span className="grow">{v.name}</span>
          {p.viewId === v.id && p.dirty && <span className="badge amber">edited</span>}
        </button>
        <button className="btn small op-view-more" aria-label={`Actions for ${v.name}`} onClick={(e) => moreAt(e, { view: v })} onPointerDown={(e) => e.stopPropagation()}>
          ⋯
        </button>
      </div>
    );
  };

  return (
    <div className="op-viewtree">
      {roots.map((r) => {
        const key = `r:${r.id}`;
        const open = isOpen(key);
        return (
          <section key={r.id}>
            <div className="op-root">
              <button className="op-group grow" aria-expanded={open} onClick={() => toggle(key)}>
                <span>{open ? "▾" : "▸"}</span> {r.label}
                {r.readOnly && <Lock />}
                <span className="muted">{r.viewCount}</span>
              </button>
              {r.id === "shared" && canManage && (
                <button className="btn small op-view-more show" aria-label="New folder" title="New folder" onClick={() => p.onNewFolder("")}>
                  +
                </button>
              )}
            </div>
            {open && (
              <div className="op-views">
                {r.nodes.map((n) => renderNode(n, 0, r))}
                {!r.nodes.length && !searching && r.id === "private" && data.viewsLoaded && <div className="muted small op-pad">No saved views yet.</div>}
                {!r.nodes.length && !searching && r.id === "shared" && data.viewsLoaded && <div className="muted small op-pad">{canManage ? "Nothing shared yet. Save a view as shared." : "Nothing is shared with you."}</div>}
              </div>
            )}
          </section>
        );
      })}
      {searching && !roots.length && <div className="muted small op-pad">No views match.</div>}
      <button className="op-view-save" onClick={p.onSaveAs}>
        + Save current as view…
      </button>

      {menu && (
        <div className="op-menu" style={{ left: Math.min(menu.x, window.innerWidth - 220), top: Math.min(menu.y, window.innerHeight - 200) }} role="menu" onPointerDown={(e) => e.stopPropagation()}>
          {menu.view && (
            <>
              <button role="menuitem" onClick={() => (setMenu(null), p.onOpenView(menu.view!))}>
                Open in Live
              </button>
              <button role="menuitem" onClick={() => (setMenu(null), p.onOpenTab(menu.view!))}>
                Open in new tab
              </button>
              {canEdit(menu.view) ? (
                <>
                  <button role="menuitem" onClick={() => (setMenu(null), p.onRenameView(menu.view!))}>
                    Rename…
                  </button>
                  <button role="menuitem" onClick={() => (setMenu(null), p.onUpdateView(menu.view!))}>
                    Update with current grid
                  </button>
                  {menu.view.shared && (
                    <button role="menuitem" onClick={() => (setMenu(null), p.onMoveView(menu.view!))}>
                      Move to folder…
                    </button>
                  )}
                  <button role="menuitem" className="danger" onClick={() => (setMenu(null), p.onDeleteView(menu.view!))}>
                    Delete…
                  </button>
                </>
              ) : (
                <div className="op-menu-label">Shared views are read-only for your role.</div>
              )}
            </>
          )}
          {menu.folder && (
            <>
              <button role="menuitem" onClick={() => (setMenu(null), p.onNewFolder(menu.folder!.id))}>
                New folder inside…
              </button>
              <button role="menuitem" onClick={() => (setMenu(null), groupById.has(menu.folder!.id) && p.onRenameFolder(groupById.get(menu.folder!.id)!))}>
                Rename…
              </button>
              <button role="menuitem" className="danger" onClick={() => (setMenu(null), groupById.has(menu.folder!.id) && p.onDeleteFolder(groupById.get(menu.folder!.id)!))}>
                Delete…
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
