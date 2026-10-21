// The top tab bar of the workspace: fixed workspace tabs (from the model in
// workspaces.ts, so a new workspace needs no change here), closable
// saved-view tabs, a "+" view picker, and on the right the setup-mode
// pencil, the wall clock and the fullscreen toggle.
import { useEffect, useMemo, useRef, useState } from "react";
import type { View } from "../shared/api";
import { badgeText, keyLabel, msToNextMinute, viewTabId, type FixedWorkspace, type TabId, type TabsState } from "./workspaces";

export interface TabBarProps {
  tabs: TabsState;
  fixed: FixedWorkspace[];
  /** Views a tab can open (private and shared), with where they live. */
  views: { view: View; where: string }[];
  /** Open alarms, for the Alarm Manager badge. */
  alarmCount: number;
  onSelect: (tab: TabId) => void;
  onCloseView: (viewId: string) => void;
  onOpenView: (viewId: string) => void;
  setup: boolean;
  onSetup: () => void;
}

/** The wall clock; ticks on the minute. */
export function WallClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setTimeout(() => setNow(new Date()), msToNextMinute(now.getTime()));
    return () => clearTimeout(t);
  }, [now]);
  return (
    <time className="op-clock" dateTime={now.toISOString()} title={now.toLocaleDateString(undefined, { dateStyle: "full" })}>
      {now.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
    </time>
  );
}

function FullscreenToggle() {
  const [on, setOn] = useState(() => typeof document !== "undefined" && !!document.fullscreenElement);
  useEffect(() => {
    const f = () => setOn(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", f);
    return () => document.removeEventListener("fullscreenchange", f);
  }, []);
  if (typeof document === "undefined" || !document.fullscreenEnabled) return null;
  return (
    <button className={`btn small op-tab-tool ${on ? "on" : ""}`} aria-pressed={on} title={on ? "Leave full screen" : "Full screen"} aria-label={on ? "Leave full screen" : "Full screen"} onClick={() => void (on ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(() => {})}>
      {on ? "⤡" : "⤢"}
    </button>
  );
}

function ViewPicker({ views, onPick, onClose }: { views: TabBarProps["views"]; onPick: (id: string) => void; onClose: () => void }) {
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

export default function TabBar(p: TabBarProps) {
  const [picking, setPicking] = useState(false);
  const nameOf = (id: string) => p.views.find((v) => v.view.id === id)?.view.name ?? "View";
  return (
    <div className="op-tabbar">
      <div className="op-tabs" role="tablist" aria-label="Workspaces">
        {p.fixed.map((w) => {
          const badge = w.badge === "alarms" ? badgeText(p.alarmCount) : "";
          return (
            <div key={w.id} className={`op-tab ${p.tabs.active === w.id ? "on" : ""}`}>
              <button role="tab" aria-selected={p.tabs.active === w.id} onClick={() => p.onSelect(w.id)} title={`${w.label} (${keyLabel(w)})`}>
                {w.label}
                {badge && (
                  <span className="nav-count" aria-label={`${p.alarmCount} open alarms`}>
                    {badge}
                  </span>
                )}
              </button>
            </div>
          );
        })}
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
        <FullscreenToggle />
        <WallClock />
      </div>
    </div>
  );
}
