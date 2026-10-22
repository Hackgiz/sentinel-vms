// The workspace sidebar: a search field over two sections. Views is the
// view tree (Private / Shared, ViewTree.tsx); Cameras is a tree by recording
// server (or by group) whose rows drag onto grid tiles. Resizable, and
// collapsible (chevron by the search field, or Ctrl+B) to a 28 px strip with
// a vertical label; the width animates and the grid takes the freed space.
// Revoked recording servers are not listed (sidebarModel.activeServers). The workspaces are on the rail (Rail.tsx).
import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { Camera } from "../shared/api";
import { cameraMime } from "./CameraGrid";
import { useData } from "./data";
import { cameraSections, clampWidth, hasGroups, sidebarWidth, toggleCollapsed, type Grouping } from "./sidebarModel";
import ViewTree, { type ViewTreeProps } from "./ViewTree";

export interface SidebarProps extends Omit<ViewTreeProps, "query"> {
  /** Cameras on the grid now. */
  shown: Set<string>;
  onPickCamera: (c: Camera) => void;
  onOpenAsGrid: (ids: string[]) => void;
}

export default function Sidebar(p: SidebarProps) {
  const data = useData();
  const { state } = p;
  const [query, setQuery] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const anchor = useRef<string | null>(null);
  const [dragging, setDragging] = useState(false);

  const sections = useMemo(() => cameraSections(data.cameras ?? [], data.servers, data.groups, query, state.grouping), [data.cameras, data.servers, data.groups, query, state.grouping]);
  const order = useMemo(() => sections.flatMap((s) => s.cameras.map((c) => c.id)), [sections]);
  const searching = query.trim() !== "";
  const folded = new Set(state.folded);
  const isOpen = (id: string) => searching || !folded.has(id);
  const toggleFold = (id: string) => p.onState((s) => ({ ...s, folded: s.folded.includes(id) ? s.folded.filter((x) => x !== id) : [...s.folded, id] }));
  const online = (data.cameras ?? []).filter((c) => c.status.online).length;

  // Anything that is no longer listed leaves the multi-selection.
  useEffect(() => setPicked((s) => (s.every((id) => order.includes(id)) ? s : s.filter((id) => order.includes(id)))), [order]);

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
    setDragging(true);
    const x0 = e.clientX;
    const w0 = state.width;
    const move = (ev: PointerEvent) => p.onState((s) => ({ ...s, width: clampWidth(w0 + ev.clientX - x0) }));
    const up = () => {
      setDragging(false);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  };

  const setGrouping = (g: Grouping) => p.onState((s) => ({ ...s, grouping: g }));

  const toggle = () => p.onState(toggleCollapsed);
  const cls = `op-side${state.collapsed ? " collapsed" : ""}${dragging ? " dragging" : ""}`;

  if (state.collapsed) {
    return (
      <aside className={cls} style={{ width: sidebarWidth(state) }} aria-label="Views and cameras">
        <button className="op-side-strip" onClick={toggle} title="Show views and cameras (Ctrl+B)" aria-label="Show views and cameras" aria-expanded={false}>
          <span className="op-side-chev" aria-hidden="true">›</span>
          <span className="op-side-vlabel">Views &amp; Cameras</span>
        </button>
      </aside>
    );
  }

  return (
    <aside className={cls} style={{ width: sidebarWidth(state) }} aria-label="Views and cameras">
      <div className="op-side-head">
        <input type="search" placeholder="Search views and cameras" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search views and cameras" />
        <button className="op-side-collapse" onClick={toggle} title="Minimize the sidebar (Ctrl+B)" aria-label="Minimize the sidebar" aria-expanded={true}>
          <span aria-hidden="true">‹</span>
        </button>
      </div>

      <div className="op-side-body">
        <div className="op-views-title">Views</div>
        <ViewTree {...p} query={query} />

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

      <div className="op-resizer" role="separator" aria-orientation="vertical" aria-label="Resize the sidebar" onPointerDown={resize} onDoubleClick={() => p.onState((s) => ({ ...s, width: 260 }))} />

    </aside>
  );
}
