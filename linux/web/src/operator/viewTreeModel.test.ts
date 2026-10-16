import { describe, expect, it } from "vitest";
import type { View, ViewGroup } from "../shared/api";
import { buildViewTree, findView, folderOptions, sharedNodes, type TreeNode } from "./viewTreeModel";

const view = (id: string, name: string, extra: Partial<View> = {}) => ({ id, name, columns: 2, rows: 2, tiles: [], createdAt: 0, updatedAt: 0, ...extra }) as View;
const grp = (id: string, name: string, parentId = "", sortOrder = 0): ViewGroup => ({ id, name, parentId, sortOrder });

const groups = [grp("g2", "Warehouse", "", 2), grp("g1", "Lobby", "", 1), grp("g3", "Floor 1", "g1")];
const shared = [view("s1", "Entrances", { shared: true, groupId: "g3", sortOrder: 2 }), view("s2", "Dock", { shared: true, groupId: "g2" }), view("s3", "Overview", { shared: true }), view("s4", "Atrium", { shared: true, groupId: "g3", sortOrder: 1 })];
const mine = [view("p2", "zebra"), view("p1", "Alpha")];

const label = (nodes: TreeNode[]): unknown[] => nodes.map((n) => (n.kind === "view" ? n.view.name : [n.name, label(n.children)]));

describe("shared tree", () => {
  it("nests folders by parent, folders first by sort order then name, views by sort order then name", () => {
    expect(label(sharedNodes(groups, shared))).toEqual([["Lobby", [["Floor 1", ["Atrium", "Entrances"]]]], ["Warehouse", ["Dock"]], "Overview"]);
  });
  it("puts orphans (missing parent or folder) at the top level", () => {
    const n = sharedNodes([grp("a", "A", "gone")], [view("v", "V", { shared: true, groupId: "nope" })]);
    expect(label(n)).toEqual([["A", []], "V"]);
  });
  it("counts views through nested folders", () => {
    const lobby = sharedNodes(groups, shared)[0];
    expect(lobby.kind === "folder" && lobby.viewCount).toBe(2);
  });
});

describe("roots", () => {
  const opts = { views: mine, shared, groups, query: "", canManage: false };
  it("Private (by name) and Shared; shared is read-only without manage rights", () => {
    const r = buildViewTree(opts);
    expect(r.map((x) => [x.id, x.readOnly, x.viewCount])).toEqual([
      ["private", false, 2],
      ["shared", true, 4],
    ]);
    expect(label(r[0].nodes)).toEqual(["Alpha", "zebra"]);
    expect(buildViewTree({ ...opts, canManage: true })[1].readOnly).toBe(false);
  });
  it("keeps an empty Shared root without a search (managers add folders there)", () => {
    expect(buildViewTree({ ...opts, views: [], shared: [], groups: [], canManage: true }).map((r) => r.id)).toEqual(["private", "shared"]);
  });
});

describe("search", () => {
  const opts = { views: mine, shared, groups, canManage: true };
  it("keeps matching views and the folders that lead to them, drops empty roots", () => {
    const r = buildViewTree({ ...opts, query: "entr" });
    expect(r.map((x) => x.id)).toEqual(["shared"]);
    expect(label(r[0].nodes)).toEqual([["Lobby", [["Floor 1", ["Entrances"]]]]]);
    expect(r[0].viewCount).toBe(1);
  });
  it("a folder name match keeps everything in it", () => {
    const r = buildViewTree({ ...opts, query: "WAREHOUSE" });
    expect(label(r[0].nodes)).toEqual([["Warehouse", ["Dock"]]]);
  });
  it("finds private views, and nothing leaves no roots", () => {
    expect(buildViewTree({ ...opts, query: "alp" }).map((r) => r.id)).toEqual(["private"]);
    expect(buildViewTree({ ...opts, query: "zzz" })).toEqual([]);
  });
});

describe("helpers", () => {
  it("folder options are indented paths in tree order", () => {
    expect(folderOptions(groups).map((o) => o.label)).toEqual(["Lobby", "Lobby / Floor 1", "Warehouse"]);
  });
  it("findView looks in both lists", () => {
    expect(findView("s3", mine, shared)?.name).toBe("Overview");
    expect(findView("p1", mine, shared)?.name).toBe("Alpha");
    expect(findView("x", mine, shared)).toBeUndefined();
  });
});
