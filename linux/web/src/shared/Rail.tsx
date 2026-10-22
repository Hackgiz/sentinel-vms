// WAVE21: the navigation rail shared by the operator (operator/Rail.tsx, wave
// 20) and the server dashboard (App.tsx), after the iPhone's SidebarShell:
// 84 px, the site and its online dot on top, one item per page (icon over
// label, the selected one on a tinted rounded square, an optional count
// badge), and whatever the app pins at the bottom (`foot`). Styles: look.css.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Icon } from "./Icons";

export interface RailItem {
  id: string;
  /** Under the icon. */
  label: string;
  icon: string;
  /** The tooltip (defaults to the label). */
  title?: string;
  /** The count badge's text ("" or absent: none). */
  badge?: string;
  /** The accessible name (defaults to the label). */
  ariaLabel?: string;
}

export interface RailProps {
  items: RailItem[];
  active: string | null;
  siteName: string;
  online: boolean;
  /** The connection state, for the dot's tooltip. */
  status: string;
  onSelect: (id: string) => void;
  /** Pinned at the bottom (the clock, Settings…). */
  foot?: ReactNode;
  label?: string;
  className?: string;
}

/** The rail's Settings-style item: a button that opens a menu of role="menuitem" buttons; a pick closes it. */
export function RailMenu({ label, icon = "settings", text = "Settings", children }: { label: string; icon?: string; text?: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const key = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("pointerdown", down);
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("pointerdown", down);
      window.removeEventListener("keydown", key);
    };
  }, [open]);
  return (
    <div className="op-rail-settings" ref={ref}>
      <button className={`op-rail-item ${open ? "on" : ""}`} aria-haspopup="menu" aria-expanded={open} title={label} onClick={() => setOpen((o) => !o)}>
        <Icon name={icon} size={20} />
        <span className="op-rail-label">{text}</span>
      </button>
      {open && (
        // A pick anywhere in the menu closes it (the item's own handler runs first).
        <div className="op-menu op-rail-menu" role="menu" onClick={(e) => (e.target as HTMLElement).closest("button, a") && setOpen(false)}>
          {children}
        </div>
      )}
    </div>
  );
}

export default function Rail(p: RailProps) {
  return (
    <nav className={`op-rail ${p.className ?? ""}`} aria-label={p.label ?? "Workspaces"}>
      <div className="op-rail-site" role="status" aria-label={`${p.siteName}, ${p.status}`} title={`${p.siteName} · ${p.status}`}>
        <span className="op-rail-badge">
          <Icon name="shield" size={20} />
          <span className={`op-rail-dot ${p.online ? "on" : ""}`} />
        </span>
        <span className="op-rail-site-name">{p.siteName}</span>
      </div>
      <div className="op-rail-items" role="tablist" aria-orientation="vertical">
        {p.items.map((w) => {
          const on = p.active === w.id;
          return (
            <button
              key={w.id}
              role="tab"
              aria-selected={on}
              className={`op-rail-item ${on ? "on" : ""}`}
              onClick={() => p.onSelect(w.id)}
              title={w.title ?? w.label}
              aria-label={w.ariaLabel ?? w.label}
            >
              <span className="op-rail-icon">
                <Icon name={w.icon} size={20} />
                {w.badge && <span className="op-rail-count">{w.badge}</span>}
              </span>
              <span className="op-rail-label">{w.label}</span>
            </button>
          );
        })}
      </div>
      {p.foot && <div className="op-rail-foot">{p.foot}</div>}
    </nav>
  );
}
