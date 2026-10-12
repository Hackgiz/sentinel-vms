// The workspace sidebar: a search field over two sections. Views are the
// user's saved views; Cameras is a tree by recording server (or by group)
// whose rows drag onto grid tiles. Resizable, and collapsible to a rail.
import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { Camera, View } from "../shared/api";
import { cameraMime } from "./CameraGrid";
import { useData } from "./data";
import { cameraSections, clampWidth, filterViews, hasGroups, type Grouping, type SidebarState } from "./sidebarModel";

export interface SidebarProps {
  state: SidebarState;
  onState: (fn: (s: SidebarState) => SidebarState) => void;
  /** Cameras on the grid now. */
  shown: Set<string>;
  viewId: string | null;
  dirty: boolean;
  onOpenView: (v: View) => void;
  onRenameView: (v: View) => void;
  onDeleteView: (v: View) => void;
  onUpdateView: (v: View) => void;
  onSaveAs: () => void;
  onPickCamera: (c: Camera) => void;
  onOpenAsGrid: (ids: string[]) => void;
  newAlarms: number;
  onAlarms: () => void;
  onEvents: () => void;
  panel: "alarms" | "events" | null;
}

export default function Sidebar(p: SidebarProps) {
  const data = useData();
  const { state } = p;
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [menu, setMenu] = useState<{ view: View; x: number; y: number } | null>(null);
  const anchor = useRef<string | null>(null);

  const views = useMemo(() => filterViews(data.views, query), [data.views, query]);
  const sections = useMemo(() => cameraSections(data.cameras ?? [], data.servers, data.groups, query, state.grouping), [data.cameras, data.servers, data.groups, query, state.grouping]);
  const order = useMemo(() => sections.flatMap((s) => s.cameras.map((c) => c.id)), [sections]);
  const searching = query.trim() !== "";
  const folded = new Set(state.folded);
  const isOpen = (id: string) => searching || !folded.has(id);
  const toggleFold = (id: string) => p.onState((s) => ({ ...s, folded: s.folded.includes(id) ? s.folded.filter((x) => x !== id) : [...s.folded, id] }));
  const online = (data.cameras ?? []).filter((c) => c.status.online).length;

  // Anything that is no longer listed leaves the multi-selection.
  useEffect(() => setPicked((s) => (s.every((id) => order.includes(id)) ? s : s.filter((id) => order.includes(id)))), [order]);
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

  const clickCamera = (e: React.MouseEvent, id: string) => {
    if (e.shiftKey && anchor.current) {
      const a = order.indexOf(anchor.current);
      const b = order.indexOf(id);
      if (a >= 0 && b >= 0) return setPicked(order.slice(Math.min(a, b), Math.max(a, b) + 1));
    }
    anchor.current = id;
    if (e.metaKey || e.ctrlKey) setPicked((s) => (s.includes(id) ? s.filter((x) => x !== id) : [...s, id]));
    else setPicked([id]);
  };

  const resize = (e: ReactPointerEvent) => {
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    const x0 = e.clientX;
    const w0 = state.width;
    const move = (ev: PointerEvent) => p.onState((s) => ({ ...s, width: clampWidth(w0 + ev.clientX - x0) }));
    const up = () => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  };

  const setGrouping = (g: Grouping) => p.onState((s) => ({ ...s, grouping: g }));

  if (state.collapsed) {
    return (
      <aside className="op-side rail" aria-label="Sidebar">
        <img src="/favicon.svg" alt="" className="op-rail-logo" />
        <button className="btn small" onClick={() => p.onState((s) => ({ ...s, collapsed: false }))} title="Show the sidebar" aria-label="Show the sidebar">
          »
        </button>
      </aside>
    );
  }

  return (
    <aside className="op-side" style={{ width: state.width }} aria-label="Sidebar">
      <div className="op-side-head">
        <input type="search" placeholder="Search views and cameras" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search views and cameras" />
        <button className="btn small" onClick={() => p.onState((s) => ({ ...s, collapsed: true }))} title="Hide the sidebar" aria-label="Hide the sidebar">
          «
        </button>
      </div>

      <div className="op-side-body">
        <button className="op-group" aria-expanded={isOpen("views")} onClick={() => toggleFold("views")}>
          <span>{isOpen("views") ? "▾" : "▸"}</span> Views<span className="muted">{data.views.length}</span>
        </button>
        {isOpen("views") && (
          <div className="op-views">
            {views.map((v) => (
              <div
                key={v.id}
                className={`op-view ${p.viewId === v.id ? "on" : ""}`}
                onContextMenu={(e) => {
                  e.preventDefault();
                  setMenu({ view: v, x: e.clientX, y: e.clientY });
                }}
              >
                <button className="op-view-main" onClick={() => p.onOpenView(v)} title={`Open ${v.name} (${v.columns}×${v.rows})`}>
                  <span className="op-view-icon" aria-hidden="true">▦</span>
                  <span className="grow">{v.name}</span>
                  {p.viewId === v.id && p.dirty && <span className="badge amber">edited</span>}
                </button>
                <button
                  className="btn small op-view-more"
                  aria-label={`Actions for ${v.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                    setMenu({ view: v, x: r.left, y: r.bottom });
                  }}
                  onPointerDown={(e) => e.stopPropagation()}
                >
                  ⋯
                </button>
              </div>
            ))}
            {data.viewsLoaded && !data.views.length && <div className="muted small op-pad">No saved views yet.</div>}
            {searching && data.views.length > 0 && !views.length && <div className="muted small op-pad">No views match.</div>}
            <button className="op-view-save" onClick={p.onSaveAs}>
              + Save current as view…
            </button>
          </div>
        )}

        <div className="op-cams-title">
          <button className="op-group grow" aria-expanded={isOpen("cameras")} onClick={() => toggleFold("cameras")}>
            <span>{isOpen("cameras") ? "▾" : "▸"}</span> Cameras
            <span className="muted">
              {online}/{data.cameras?.length ?? 0}
            </span>
          </button>
          {hasGroups(data.groups) && (
            <div className="seg op-grouping" role="group" aria-label="Group cameras">
              <button className={state.grouping === "server" ? "on" : ""} onClick={() => setGrouping("server")}>
                Server
              </button>
              <button className={state.grouping === "group" ? "on" : ""} onClick={() => setGrouping("group")}>
                Group
              </button>
            </div>
          )}
        </div>
        {isOpen("cameras") && (
          <>
            {picked.length > 1 && (
              <div className="op-open-grid">
                <button className="btn small primary" onClick={() => p.onOpenAsGrid(picked)}>
                  Open {picked.length} as grid
                </button>
                <button className="btn small" onClick={() => setPicked([])}>
                  Clear
                </button>
              </div>
            )}
            {data.cameras === null && <div className="muted small op-pad">Loading…</div>}
            {data.cameras?.length === 0 && <div className="muted small op-pad">No cameras you may view.</div>}
            {searching && data.cameras && data.cameras.length > 0 && !sections.length && <div className="muted small op-pad">No cameras match.</div>}
            {sections.map((sec) => {
              const key = `s:${sec.id || "rest"}`;
              const open = isOpen(key);
              return (
                <section key={key}>
                  <button className="op-server" aria-expanded={open} onClick={() => toggleFold(key)}>
                    <span className="op-caret">{open ? "▾" : "▸"}</span>
                    {sec.online !== undefined && <span className={`op-status ${sec.online ? "on" : "off"}`} role="img" aria-label={sec.online ? "Server online" : "Server offline"} />}
                    <span className="grow">{sec.name}</span>
                    <span className="muted">{searching && sec.cameras.length !== sec.total ? `${sec.cameras.length}/${sec.total}` : sec.total}</span>
                  </button>
                  {open &&
                    sec.cameras.map((c) => (
                      <div
                        key={c.id}
                        className={`op-cam ${p.shown.has(c.id) ? "shown" : ""} ${picked.includes(c.id) ? "picked" : ""}`}
                        draggable
                        onClick={(e) => clickCamera(e, c.id)}
                        onDragStart={(e) => {
                          e.dataTransfer.setData(cameraMime, c.id);
                          e.dataTransfer.effectAllowed = "copyMove";
                        }}
                        onDoubleClick={() => p.onPickCamera(c)}
                        title={`${c.status.message || (c.status.online ? "Online" : "Offline")} — drag onto a tile, or double-click`}
                      >
                        <span className={`op-status ${c.status.online ? "on" : "idle"}`} role="img" aria-label={c.status.online ? "Online" : "Offline"} />
                        <span className="grow">
                          {c.name}
                          {c.location && <small>{c.location}</small>}
                        </span>
                      </div>
                    ))}
                </section>
              );
            })}
          </>
        )}
      </div>

      <div className="op-side-foot">
        <button className={`op-foot ${p.panel === "alarms" ? "on" : ""}`} onClick={p.onAlarms}>
          Alarms {p.newAlarms > 0 && <span className="nav-count">{p.newAlarms}</span>}
        </button>
        <button className={`op-foot ${p.panel === "events" ? "on" : ""}`} onClick={p.onEvents}>
          Events
        </button>
      </div>
      <div className="op-resizer" role="separator" aria-orientation="vertical" aria-label="Resize the sidebar" onPointerDown={resize} onDoubleClick={() => p.onState((s) => ({ ...s, width: 260 }))} />

      {menu && (
        <div className="op-menu" style={{ left: Math.min(menu.x, window.innerWidth - 220), top: Math.min(menu.y, window.innerHeight - 140) }} role="menu" onPointerDown={(e) => e.stopPropagation()}>
          <button role="menuitem" onClick={() => (setMenu(null), p.onRenameView(menu.view))}>
            Rename…
          </button>
          <button role="menuitem" onClick={() => (setMenu(null), p.onUpdateView(menu.view))}>
            Update with current grid
          </button>
          <button role="menuitem" className="danger" onClick={() => (setMenu(null), p.onDeleteView(menu.view))}>
            Delete…
          </button>
        </div>
      )}
    </aside>
  );
}
