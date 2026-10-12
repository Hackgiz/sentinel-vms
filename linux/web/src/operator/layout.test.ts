import { describe, expect, it } from "vitest";
import { assignCamera, camerasOf, clearTile, emptyLayout, firstEmpty, gridWith, layoutFromView, presetOf, pruneCameras, resize, sameAsView, swapTiles, viewFromLayout } from "./layout";

describe("grid layouts", () => {
  it("covers every cell", () => {
    const l = emptyLayout(3, 3);
    expect(l.tiles).toHaveLength(9);
    expect(l.tiles[4]).toEqual({ x: 1, y: 1, w: 1, h: 1, cameraId: null });
    expect(presetOf(l)).toBe("3x3");
    expect(emptyLayout(20, 0).columns).toBe(8);
  });

  it("assigns by drop, moving a camera already shown (swap)", () => {
    let l = gridWith(2, 2, ["a", "b"]);
    l = assignCamera(l, 3, "c");
    expect(camerasOf(l)).toEqual(["a", "b", "c"]);
    l = assignCamera(l, 0, "c"); // c moves to 0, a takes c's old place
    expect(l.tiles.map((t) => t.cameraId)).toEqual(["c", "b", null, "a"]);
    l = swapTiles(l, 0, 1);
    expect(l.tiles.map((t) => t.cameraId)).toEqual(["b", "c", null, "a"]);
    l = clearTile(l, 1);
    expect(firstEmpty(l)).toBe(1);
    expect(assignCamera(l, 9, "x")).toBe(l);
  });

  it("keeps cameras in reading order across sizes", () => {
    const l = resize(gridWith(3, 3, ["a", "b", "c", "d", "e"]), 2, 2);
    expect(camerasOf(l)).toEqual(["a", "b", "c", "d"]);
    expect(camerasOf(resize(l, 4, 4))).toEqual(["a", "b", "c", "d"]);
  });

  it("drops cameras no longer visible", () => {
    const l = gridWith(2, 1, ["a", "b"]);
    expect(pruneCameras(l, new Set(["a", "b"]))).toBe(l);
    expect(camerasOf(pruneCameras(l, new Set(["b"])))).toEqual(["b"]);
  });
});

describe("views ↔ layouts", () => {
  it("round-trips a view with a large tile", () => {
    const view = { name: "Lobby", columns: 3, rows: 3, tiles: [{ cameraId: "big", x: 0, y: 0, w: 2, h: 2 }, { cameraId: "r", x: 2, y: 0 }, { cameraId: "b", x: 0, y: 2, w: 1, h: 1 }] };
    const l = layoutFromView(view);
    // The 2×2 tile, two single ones and the three cells left empty.
    expect(l.tiles).toHaveLength(6);
    expect(l.tiles.filter((t) => t.cameraId === null).map((t) => [t.x, t.y])).toEqual([[2, 1], [1, 2], [2, 2]]);
    expect(presetOf(l)).toBeNull();
    const back = viewFromLayout(" Lobby ", l);
    expect(back).toEqual({ name: "Lobby", columns: 3, rows: 3, tiles: [{ cameraId: "big", x: 0, y: 0, w: 2, h: 2 }, { cameraId: "r", x: 2, y: 0, w: 1, h: 1 }, { cameraId: "b", x: 0, y: 2, w: 1, h: 1 }] });
    expect(sameAsView(l, view)).toBe(true);
    expect(sameAsView(swapTiles(l, 0, 1), view)).toBe(false);
  });

  it("repairs views that don't fit: clips spans, moves overlaps and unplaced tiles, drops duplicates", () => {
    const l = layoutFromView({
      columns: 2,
      rows: 2,
      tiles: [
        { cameraId: "a", x: 1, y: 1, w: 5, h: 5 }, // clipped to 1×1
        { cameraId: "b", x: 1, y: 1 }, // overlaps a → first free cell
        { cameraId: "c" }, // no position → next free cell
        { cameraId: "a", x: 0, y: 0 }, // duplicate
        { cameraId: "d", x: 7, y: 7 }, // outside → free cell
        { cameraId: "e" }, // no room left
      ],
    });
    expect(l.tiles.map((t) => [t.x, t.y, t.w, t.h, t.cameraId])).toEqual([
      [0, 0, 1, 1, "b"],
      [1, 0, 1, 1, "c"],
      [0, 1, 1, 1, "d"],
      [1, 1, 1, 1, "a"],
    ]);
  });
});
