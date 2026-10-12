// The live grid's layout, and how it maps to saved views (/api/v2/views:
// `columns × rows` cells and tiles at x,y spanning w×h). A layout always
// covers every cell: tiles with a camera, and empty 1×1 tiles in the rest.

import type { ViewInput } from "../shared/api";

export interface GridTile {
  x: number;
  y: number;
  w: number;
  h: number;
  cameraId: string | null;
}

export interface GridLayout {
  columns: number;
  rows: number;
  /** Reading order (by y, then x). */
  tiles: GridTile[];
}

/** The quick layouts in the toolbar. */
export const presets: { id: string; label: string; columns: number; rows: number }[] = [
  { id: "1", label: "1", columns: 1, rows: 1 },
  { id: "2x2", label: "2×2", columns: 2, rows: 2 },
  { id: "3x3", label: "3×3", columns: 3, rows: 3 },
  { id: "4x4", label: "4×4", columns: 4, rows: 4 },
];

/** A view as stored, read leniently (w/h default to 1; x/y may be absent). */
export interface ViewShape {
  columns: number;
  rows: number;
  tiles: { cameraId: string; x?: number; y?: number; w?: number; h?: number }[];
}

/** The view schema's limits. */
export const maxSide = 8;

const clampInt = (n: unknown, lo: number, hi: number, dflt: number) => {
  const v = typeof n === "number" && Number.isFinite(n) ? Math.floor(n) : dflt;
  return Math.min(hi, Math.max(lo, v));
};

function sortTiles(tiles: GridTile[]): GridTile[] {
  return [...tiles].sort((a, b) => a.y - b.y || a.x - b.x);
}

/** Fill every cell no tile covers with an empty 1×1 tile. */
function fill(columns: number, rows: number, tiles: GridTile[]): GridLayout {
  const used = occupancy(columns, rows, tiles);
  const out = [...tiles];
  for (let y = 0; y < rows; y++) for (let x = 0; x < columns; x++) if (!used[y][x]) out.push({ x, y, w: 1, h: 1, cameraId: null });
  return { columns, rows, tiles: sortTiles(out) };
}

function occupancy(columns: number, rows: number, tiles: GridTile[]): boolean[][] {
  const used = Array.from({ length: rows }, () => Array<boolean>(columns).fill(false));
  for (const t of tiles) for (let y = t.y; y < t.y + t.h; y++) for (let x = t.x; x < t.x + t.w; x++) used[y][x] = true;
  return used;
}

export function emptyLayout(columns: number, rows: number): GridLayout {
  return fill(clampInt(columns, 1, maxSide, 1), clampInt(rows, 1, maxSide, 1), []);
}

/** Cameras in reading order (empty tiles skipped). */
export function camerasOf(layout: GridLayout): string[] {
  return layout.tiles.flatMap((t) => (t.cameraId ? [t.cameraId] : []));
}

/**
 * A grid of the given size holding the given cameras in reading order
 * (cameras that don't fit are left out).
 */
export function gridWith(columns: number, rows: number, cameraIds: string[]): GridLayout {
  const l = emptyLayout(columns, rows);
  return { ...l, tiles: l.tiles.map((t, i) => ({ ...t, cameraId: cameraIds[i] ?? null })) };
}

/** Switch to another grid size, keeping the cameras in reading order. */
export function resize(layout: GridLayout, columns: number, rows: number): GridLayout {
  return gridWith(columns, rows, camerasOf(layout));
}

/**
 * A saved view as a layout. Tiles that fall outside the grid are clipped,
 * tiles that would overlap one already placed (or have no position) go to
 * the first free cell, and a camera listed twice is shown once.
 */
export function layoutFromView(view: ViewShape): GridLayout {
  const columns = clampInt(view.columns, 1, maxSide, 1);
  const rows = clampInt(view.rows, 1, maxSide, 1);
  const placed: GridTile[] = [];
  const later: string[] = [];
  const seen = new Set<string>();
  for (const vt of view.tiles ?? []) {
    if (!vt?.cameraId || seen.has(vt.cameraId)) continue;
    seen.add(vt.cameraId);
    if (vt.x === undefined || vt.y === undefined || vt.x >= columns || vt.y >= rows || vt.x < 0 || vt.y < 0) {
      later.push(vt.cameraId);
      continue;
    }
    const x = Math.floor(vt.x);
    const y = Math.floor(vt.y);
    const t: GridTile = { x, y, w: clampInt(vt.w, 1, columns - x, 1), h: clampInt(vt.h, 1, rows - y, 1), cameraId: vt.cameraId };
    const used = occupancy(columns, rows, placed);
    let free = true;
    for (let yy = t.y; yy < t.y + t.h && free; yy++) for (let xx = t.x; xx < t.x + t.w; xx++) if (used[yy][xx]) free = false;
    if (free) placed.push(t);
    else later.push(vt.cameraId);
  }
  const layout = fill(columns, rows, placed);
  // Unplaced cameras take the empty cells in reading order.
  let i = 0;
  const tiles = layout.tiles.map((t) => (t.cameraId === null && i < later.length ? { ...t, cameraId: later[i++] } : t));
  return { ...layout, tiles };
}

/** A layout as a view to save. Empty cells aren't stored. */
export function viewFromLayout(name: string, layout: GridLayout): ViewInput {
  return {
    name: name.trim(),
    columns: layout.columns,
    rows: layout.rows,
    tiles: layout.tiles.filter((t) => t.cameraId).map((t) => ({ cameraId: t.cameraId!, x: t.x, y: t.y, w: t.w, h: t.h })),
  };
}

/** Whether a layout still matches the view it was loaded from (for "unsaved changes"). */
export function sameAsView(layout: GridLayout, view: ViewShape & { name: string }): boolean {
  const a = viewFromLayout(view.name, layout);
  const b = viewFromLayout(view.name, layoutFromView(view));
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * Put a camera in tile `index`. A camera already shown elsewhere moves
 * there, and the tile's previous camera takes its old place (a swap).
 */
export function assignCamera(layout: GridLayout, index: number, cameraId: string): GridLayout {
  if (index < 0 || index >= layout.tiles.length) return layout;
  const from = layout.tiles.findIndex((t) => t.cameraId === cameraId);
  const displaced = layout.tiles[index].cameraId;
  const tiles = layout.tiles.map((t, i) => {
    if (i === index) return { ...t, cameraId };
    if (i === from) return { ...t, cameraId: displaced };
    return t;
  });
  return { ...layout, tiles };
}

/** Swap the cameras of two tiles (dragging one tile onto another). */
export function swapTiles(layout: GridLayout, a: number, b: number): GridLayout {
  if (a === b || !layout.tiles[a] || !layout.tiles[b]) return layout;
  const ca = layout.tiles[a].cameraId;
  const cb = layout.tiles[b].cameraId;
  return { ...layout, tiles: layout.tiles.map((t, i) => (i === a ? { ...t, cameraId: cb } : i === b ? { ...t, cameraId: ca } : t)) };
}

export function clearTile(layout: GridLayout, index: number): GridLayout {
  return { ...layout, tiles: layout.tiles.map((t, i) => (i === index ? { ...t, cameraId: null } : t)) };
}

/** Drop cameras the user can no longer see (deleted, or out of scope). */
export function pruneCameras(layout: GridLayout, known: Set<string>): GridLayout {
  if (layout.tiles.every((t) => !t.cameraId || known.has(t.cameraId))) return layout;
  return { ...layout, tiles: layout.tiles.map((t) => (t.cameraId && !known.has(t.cameraId) ? { ...t, cameraId: null } : t)) };
}

/** Index of the first empty tile, or -1. */
export function firstEmpty(layout: GridLayout): number {
  return layout.tiles.findIndex((t) => !t.cameraId);
}

/** The preset a layout's size matches, if any. */
export function presetOf(layout: GridLayout): string | null {
  const p = presets.find((p) => p.columns === layout.columns && p.rows === layout.rows);
  const allSingle = layout.tiles.every((t) => t.w === 1 && t.h === 1);
  return p && allSingle ? p.id : null;
}

/** CSS grid placement for a tile. */
export function tileStyle(t: GridTile): { gridColumn: string; gridRow: string } {
  return { gridColumn: `${t.x + 1} / span ${t.w}`, gridRow: `${t.y + 1} / span ${t.h}` };
}

/** The smallest square-ish grid that holds n cameras ("open as grid"). */
export function gridForCount(n: number): { columns: number; rows: number } {
  const count = Math.max(1, Math.min(n, maxSide * maxSide));
  const columns = Math.min(maxSide, Math.ceil(Math.sqrt(count)));
  return { columns, rows: Math.min(maxSide, Math.ceil(count / columns)) };
}

/** A grid filled with the given cameras in reading order, sized to fit them. */
export function openAsGrid(cameraIds: string[]): GridLayout {
  const { columns, rows } = gridForCount(cameraIds.length);
  return gridWith(columns, rows, cameraIds);
}
