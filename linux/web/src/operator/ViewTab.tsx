// A saved view in its own tab: the view's live grid. Read-only until setup
// mode is on; in setup mode the grid takes cameras dragged from the tree,
// swaps tiles, clears them, resizes to a preset, and saves (update, or
// Save as… with a name and target folder).
import { useState } from "react";
import type { View } from "../shared/api";
import CameraGrid from "./CameraGrid";
import { clearTile, presetOf, presets, resize, swapTiles, assignCamera, type GridLayout } from "./layout";

export default function ViewTab({
  view,
  layout,
  onLayout,
  setup,
  canUpdate,
  dirty,
  busy,
  onSave,
  onSaveAs,
  onRevert,
  onSetup,
}: {
  view: View;
  layout: GridLayout;
  onLayout: (fn: (l: GridLayout) => GridLayout) => void;
  setup: boolean;
  /** The viewer may overwrite this view (not a shared one without manage rights). */
  canUpdate: boolean;
  dirty: boolean;
  busy: boolean;
  onSave: () => void;
  onSaveAs: () => void;
  onRevert: () => void;
  onSetup: () => void;
}) {
  const [selected, setSelected] = useState<number | null>(null);
  const preset = presetOf(layout);
  return (
    <>
      <div className="op-toolbar op-viewbar">
        <span className="op-viewname" title={view.name}>
          {view.name}
        </span>
        {view.shared && <span className="badge">Shared</span>}
        {setup ? (
          <>
            <div className="seg" role="group" aria-label="Layout">
              {presets.map((p) => (
                <button key={p.id} className={preset === p.id ? "on" : ""} onClick={() => onLayout((l) => resize(l, p.columns, p.rows))} title={`${p.label} grid`}>
                  {p.label}
                </button>
              ))}
            </div>
            <span className="muted small">Drag cameras from the sidebar onto tiles; drag tiles to swap.</span>
            <div className="grow" />
            {dirty && <span className="badge amber">Unsaved changes</span>}
            {dirty && (
              <button className="btn small" onClick={onRevert} disabled={busy}>
                Revert
              </button>
            )}
            {canUpdate && (
              <button className="btn small primary" onClick={onSave} disabled={busy || !dirty}>
                Save
              </button>
            )}
            <button className="btn small" onClick={onSaveAs} disabled={busy}>
              Save as…
            </button>
            <button className="btn small" onClick={onSetup}>
              Done
            </button>
          </>
        ) : (
          <>
            <span className="muted small">
              {view.columns}×{view.rows}
            </span>
            <div className="grow" />
            <button className="btn small" onClick={onSetup} title="Setup mode: edit this view">
              ✎ Edit layout
            </button>
          </>
        )}
      </div>
        <CameraGrid
          layout={layout}
          selected={selected}
          onSelect={setSelected}
          onAssign={setup ? (i, id) => onLayout((l) => assignCamera(l, i, id)) : undefined}
          onSwap={setup ? (a, b) => onLayout((l) => swapTiles(l, a, b)) : undefined}
          onClear={setup ? (i) => onLayout((l) => clearTile(l, i)) : undefined}
          emptyHint="Drag a camera here"
        />
    </>
  );
}
