// The saved-view tabs inside the Live workspace (WAVE20: the fixed
// workspaces moved to the rail, Rail.tsx): the Live grid's own tab, closable
// saved-view tabs, a "+" view picker, and on the right the setup-mode pencil.
import { useEffect, useMemo, useRef, useState } from "react";
import type { View } from "../shared/api";
import { viewTabId, type TabId, type TabsState } from "./workspaces";

export interface ViewTabsProps {
  tabs: TabsState;
  /** Views a tab can open (private and shared), with where they live. */
  views: { view: View; where: string }[];
  onSelect: (tab: TabId) => void;
  onCloseView: (viewId: string) => void;
  onOpenView: (viewId: string) => void;
  setup: boolean;
  onSetup: () => void;
}

function ViewPicker({ views, onPick, onClose }: { views: ViewTabsProps["views"]; onPick: (id: string) => void; onClose: () => void }) {
  const [q, setQ] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  const shown = useMemo(() => {
    const n = q.trim().toLowerCase();
    return views.filter((v) => !n || v.view.name.toLowerCase().includes(n) || v.where.toLowerCase().includes(n));
  }, [views, q]);
  useEffect(() => {
    const down = (e: PointerEvent) => !ref.current?.contains(e.target as Node) && onClose();
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("pointerdown", down);
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("pointerdown", down);
      window.removeEventListener("keydown", key);
    };
  }, [onClose]);
  return (
    <div className="op-picker" ref={ref} role="dialog" aria-label="Open a view in a new tab">
      <input type="search" autoFocus placeholder="Find a view" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Find a view" />
      <div className="op-picker-list">
        {shown.map(({ view, where }) => (
          <button key={view.id} onClick={() => onPick(view.id)}>
            <span className="grow">{view.name}</span>
            <small className="muted">{where}</small>
          </button>
        ))}
        {!shown.length && <div className="muted small op-pad">{views.length ? "No views match." : "No saved views yet."}</div>}
      </div>
    </div>
  );
}

export default function ViewTabs(p: ViewTabsProps) {
  const [picking, setPicking] = useState(false);
  const nameOf = (id: string) => p.views.find((v) => v.view.id === id)?.view.name ?? "View";
  return (
    <div className="op-tabbar">
      <div className="op-tabs" role="tablist" aria-label="Live grid and saved views">
        <div className={`op-tab ${p.tabs.active === "live" ? "on" : ""}`}>
          <button role="tab" aria-selected={p.tabs.active === "live"} onClick={() => p.onSelect("live")} title="The Live grid (Ctrl+1)">
            Live grid
          </button>
        </div>
        {p.tabs.views.map((id) => {
          const tab = viewTabId(id);
          return (
            <div key={tab} className={`op-tab view ${p.tabs.active === tab ? "on" : ""}`}>
              <button role="tab" aria-selected={p.tabs.active === tab} onClick={() => p.onSelect(tab)} title={nameOf(id)}>
                <span className="op-view-icon" aria-hidden="true">▦</span> <span className="op-tab-name">{nameOf(id)}</span>
              </button>
              <button className="op-tab-close" aria-label={`Close ${nameOf(id)}`} title="Close tab" onClick={() => p.onCloseView(id)}>
                ✕
              </button>
            </div>
          );
        })}
        <div className="op-tab-plus">
          <button className="btn small" aria-haspopup="dialog" aria-expanded={picking} title="Open a view in a new tab" aria-label="Open a view in a new tab" onClick={() => setPicking((x) => !x)}>
            +
          </button>
          {picking && (
            <ViewPicker
              views={p.views}
              onClose={() => setPicking(false)}
              onPick={(id) => {
                setPicking(false);
                p.onOpenView(id);
              }}
            />
          )}
        </div>
      </div>
      <div className="op-tabbar-right">
        <button className={`btn small op-tab-tool ${p.setup ? "on" : ""}`} aria-pressed={p.setup} title={p.setup ? "Leave setup mode" : "Setup mode: edit the open view"} aria-label="Setup mode" onClick={p.onSetup}>
          ✎ <span className="op-setup-label">Setup</span>
        </button>
      </div>
    </div>
  );
}
