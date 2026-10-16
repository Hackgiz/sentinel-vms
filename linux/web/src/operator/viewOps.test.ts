import { describe, expect, it } from "vitest";
import type { View } from "../shared/api";
import { gridWith } from "./layout";
import { canEditView, newViewInput, viewInputFromLayout, viewInputOf } from "./viewOps";

const priv = { id: "p", name: "Mine", columns: 2, rows: 2, tiles: [{ cameraId: "a", x: 0, y: 0 }], createdAt: 0, updatedAt: 0 } as View;
const shared = { ...priv, id: "s", name: "Wall", shared: true, groupId: "g1", sortOrder: 3 } as View;

describe("view requests", () => {
  it("a shared view keeps its flag, folder and order when renamed or re-saved", () => {
    expect(viewInputOf(shared, { name: "Wall 2" })).toMatchObject({ name: "Wall 2", shared: true, groupId: "g1", sortOrder: 3 });
    const l = gridWith(2, 2, ["a", "b"]);
    expect(viewInputFromLayout(shared, "Wall", l)).toMatchObject({ shared: true, groupId: "g1", sortOrder: 3, columns: 2, rows: 2 });
    expect(viewInputFromLayout(shared, "Wall", l).tiles).toHaveLength(2);
  });
  it("a private view stays private", () => {
    const i = viewInputOf(priv, { name: "New" });
    expect(i.shared).toBeUndefined();
    expect(i.groupId).toBeUndefined();
  });
  it("new views are private unless shared, with the folder only when given", () => {
    const l = gridWith(1, 1, ["a"]);
    expect(newViewInput("A", l, { shared: false, groupId: "g" }).shared).toBeUndefined();
    expect(newViewInput("A", l, { shared: false, groupId: "g" }).groupId).toBeUndefined();
    expect(newViewInput("A", l, { shared: true, groupId: "g" })).toMatchObject({ shared: true, groupId: "g" });
    expect(newViewInput("A", l, { shared: true, groupId: "" }).groupId).toBeUndefined();
  });
  it("shared views need manage rights to edit", () => {
    expect([canEditView(priv, false), canEditView(shared, false), canEditView(shared, true)]).toEqual([true, false, true]);
  });
});
