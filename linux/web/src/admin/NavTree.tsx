// The site-navigation tree: collapsible categories, filter box, keyboard
// up/down/left/right/enter. Presentation only; logic lives in navModel.ts.
import { useEffect, useMemo, useRef, useState } from "react";
import { categoryKey, filterTree, loadExpanded, moveFocus, rootKey, saveExpanded, visibleRows, type NavTree as Tree, type Route, type Row } from "./navModel";

export default function NavTree({ tree, route, title, onNavigate }: { tree: Tree; route: Route; title: string; onNavigate?: () => void }) {
  const [filter, setFilter] = useState("");
  const [expanded, setExpanded] = useState(loadExpanded);
  const filtering = filter.trim() !== "";
  const shown = useMemo(() => filterTree(tree, filter), [tree, filter]);
  const rows = useMemo(() => visibleRows(shown, expanded, filtering), [shown, expanded, filtering]);
  const activeKey = route.kind === "leaf" ? `leaf:${route.leaf.id}` : rootKey;
  const [focusKey, setFocusKey] = useState(activeKey);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => setFocusKey(activeKey), [activeKey]);
  const focusRow = (key: string) => {
    setFocusKey(key);
    requestAnimationFrame(() => box.current?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus());
  };
  const toggle = (slug: string, open?: boolean) =>
    setExpanded((e) => {
      const next = { ...e, [slug]: open ?? e[slug] === false };
      saveExpanded(next);
      return next;
    });

  const activate = (r: Row) => {
    if (r.type === "category") toggle(r.category.slug);
    else {
      window.location.hash = r.type === "root" ? "#/admin" : r.leaf.hash;
      onNavigate?.();
    }
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    const row = rows.find((r) => r.key === focusKey) ?? rows[0];
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      focusRow(moveFocus(rows, row.key, e.key === "ArrowDown" ? 1 : -1));
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      activate(row);
    } else if (e.key === "ArrowRight" && row.type === "category" && !row.expanded) {
      e.preventDefault();
      toggle(row.category.slug, true);
    } else if (e.key === "ArrowLeft") {
      if (row.type === "category" && row.expanded && !filtering) {
        e.preventDefault();
        toggle(row.category.slug, false);
      } else if (row.type === "leaf") {
        e.preventDefault();
        focusRow(categoryKey(row.leaf.category));
      }
    } else if (e.key === "Home") {
      e.preventDefault();
      focusRow(rows[0].key);
    } else if (e.key === "End") {
      e.preventDefault();
      focusRow(rows[rows.length - 1].key);
    }
  };

  return (
    <div className="nav-tree">
      <input
        className="nav-filter"
        type="search"
        placeholder="Filter pages…"
        aria-label="Filter navigation"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            focusRow(rows[0].key);
          } else if (e.key === "Escape") setFilter("");
        }}
      />
      <div ref={box} role="tree" aria-label="Sentinel Admin sections" onKeyDown={onKeyDown}>
        {rows.map((r) => {
          const common = { "data-key": r.key, tabIndex: focusKey === r.key ? 0 : -1, onFocus: () => setFocusKey(r.key) };
          if (r.type === "root")
            return (
              <a key={r.key} role="treeitem" aria-level={1} aria-selected={route.kind === "root"} aria-current={route.kind === "root" ? "page" : undefined} href="#/admin" className={`nav-row nav-root ${route.kind === "root" ? "on" : ""}`} onClick={onNavigate} {...common}>
                <img className="nav-glyph" src="/favicon.svg" alt="" aria-hidden="true" />
                {title}
              </a>
            );
          if (r.type === "category")
            return (
              <div key={r.key} role="treeitem" aria-level={2} aria-expanded={r.expanded} className="nav-row nav-cat" onClick={() => toggle(r.category.slug)} {...common}>
                <span className={`nav-chev ${r.expanded ? "open" : ""}`} aria-hidden="true">▸</span>
                {r.category.label}
              </div>
            );
          const on = route.kind === "leaf" && route.leaf.id === r.leaf.id;
          return (
            <a key={r.key} role="treeitem" aria-level={3} aria-selected={on} aria-current={on ? "page" : undefined} href={r.leaf.hash} className={`nav-row nav-leaf ${on ? "on" : ""}`} onClick={onNavigate} {...common}>
              {r.leaf.label}
            </a>
          );
        })}
        {filtering && shown.categories.length === 0 && <div className="nav-none">No matching pages</div>}
      </div>
    </div>
  );
}
