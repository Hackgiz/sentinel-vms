import { describe, expect, it } from "vitest";
import type { Camera, CameraGroup, RecordingServer, View } from "../shared/api";
import { activeServers, cameraSections, clampWidth, filterViews, parseSidebarState, sidebarCollapsedWidth, sidebarDefaults, sidebarStorageKey, sidebarWidth, toggleCollapsed } from "./sidebarModel";
import { gridForCount, openAsGrid } from "./layout";

const cam = (id: string, name: string, recorderId: string, location = "", groupIds: string[] = []) => ({ id, name, recorderId, location, groupIds, status: { online: true } }) as unknown as Camera;
const srv = (id: string, name: string, online = true, revoked = false) => ({ id, name, online, revoked }) as unknown as RecordingServer;
const grp = (id: string, name: string, cameraIds: string[]) => ({ id, name, cameraIds }) as unknown as CameraGroup;

const cameras = [cam("1", "Front door", "a", "Lobby", ["g1"]), cam("2", "Dock", "a", "Warehouse"), cam("3", "Gate", "b", "Yard", ["g1"])];
const servers = [srv("b", "Annex", false), srv("a", "Main building"), srv("c", "Spare")];
const groups = [grp("g1", "Entrances", ["1", "3"])];

describe("camera tree", () => {
  it("groups by server, sorted, with counts and online state", () => {
    const s = cameraSections(cameras, servers, groups, "", "server");
    expect(s.map((x) => [x.name, x.total, x.online])).toEqual([
      ["Annex", 1, false],
      ["Main building", 2, true],
      ["Spare", 0, true],
    ]);
  });
  it("groups by group, with the rest last", () => {
    const s = cameraSections(cameras, servers, groups, "", "group");
    expect(s.map((x) => [x.name, x.cameras.map((c) => c.id)])).toEqual([
      ["Entrances", ["1", "3"]],
      ["Other cameras", ["2"]],
    ]);
  });
  it("search matches camera name and location, and drops empty sections", () => {
    expect(cameraSections(cameras, servers, groups, "yard", "server").map((x) => x.name)).toEqual(["Annex"]);
    const s = cameraSections(cameras, servers, groups, "door", "server");
    expect(s).toHaveLength(1);
    expect(s[0].cameras[0].id).toBe("1");
    expect(s[0].total).toBe(2);
  });
  it("a server or group name match keeps all its cameras", () => {
    expect(cameraSections(cameras, servers, groups, "main", "server")[0].cameras).toHaveLength(2);
    expect(cameraSections(cameras, servers, groups, "entr", "group")[0].cameras).toHaveLength(2);
  });
  it("keeps cameras of unknown servers", () => {
    const s = cameraSections([cam("9", "Lost", "zzz")], servers, groups, "", "server");
    expect(s.at(-1)).toMatchObject({ name: "Other cameras", total: 1 });
  });
});

describe("revoked recording servers", () => {
  const withRevoked = [...servers, srv("r1", "RS 1", false, true), srv("r2", "RS 2", false, true)];
  it("are left out of the by-server tree", () => {
    expect(cameraSections(cameras, withRevoked, groups, "", "server").map((x) => x.name)).toEqual(["Annex", "Main building", "Spare"]);
    expect(activeServers(withRevoked).map((s) => s.id)).toEqual(["b", "a", "c"]);
  });
  it("don't match a search by their name", () => {
    expect(cameraSections(cameras, withRevoked, groups, "rs 1", "server")).toEqual([]);
  });
  it("still assigned cameras fall to Other cameras", () => {
    const s = cameraSections([...cameras, cam("7", "Old", "r1")], withRevoked, groups, "", "server");
    expect(s.at(-1)).toMatchObject({ name: "Other cameras", total: 1 });
  });
  it("don't affect the by-group tree", () => {
    expect(cameraSections(cameras, withRevoked, groups, "", "group").map((x) => x.name)).toEqual(["Entrances", "Other cameras"]);
  });
});

describe("views filter", () => {
  const views = [{ id: "1", name: "Lobby" }, { id: "2", name: "Parking" }] as View[];
  it("matches names, case-insensitively", () => {
    expect(filterViews(views, " LOB ").map((v) => v.id)).toEqual(["1"]);
    expect(filterViews(views, "")).toHaveLength(2);
  });
});

describe("sidebar state", () => {
  it("clamps the width and survives junk", () => {
    expect(clampWidth(50)).toBe(200);
    expect(clampWidth(9999)).toBe(520);
    expect(parseSidebarState("nope")).toEqual(sidebarDefaults);
    expect(parseSidebarState(null)).toEqual(sidebarDefaults);
    expect(parseSidebarState(JSON.stringify({ width: 300, collapsed: true, grouping: "group", folded: ["s:a", 3] }))).toEqual({ width: 300, collapsed: true, grouping: "group", folded: ["s:a"] });
  });
});

describe("collapsing", () => {
  it("collapses to a 28 px strip and keeps the width for later", () => {
    expect(sidebarCollapsedWidth).toBe(28);
    expect(sidebarWidth({ width: 320, collapsed: true })).toBe(28);
    expect(sidebarWidth({ width: 320, collapsed: false })).toBe(320);
    expect(sidebarWidth({ width: 10, collapsed: false })).toBe(200);
  });
  it("toggles only the collapsed flag", () => {
    const s = { ...sidebarDefaults, width: 300, folded: ["v"] };
    expect(toggleCollapsed(s)).toEqual({ ...s, collapsed: true });
    expect(toggleCollapsed(toggleCollapsed(s))).toEqual(s);
  });
  it("is remembered per site, and survives a reload", () => {
    expect(sidebarStorageKey("site-a")).toBe("operator.sidebar.site-a");
    expect(sidebarStorageKey("site-a")).not.toBe(sidebarStorageKey("site-b"));
    expect(parseSidebarState(JSON.stringify(toggleCollapsed(sidebarDefaults))).collapsed).toBe(true);
  });
});

describe("open as grid", () => {
  it("sizes the grid to the selection", () => {
    expect(gridForCount(1)).toEqual({ columns: 1, rows: 1 });
    expect(gridForCount(4)).toEqual({ columns: 2, rows: 2 });
    expect(gridForCount(5)).toEqual({ columns: 3, rows: 2 });
    expect(gridForCount(9)).toEqual({ columns: 3, rows: 3 });
    expect(openAsGrid(["a", "b", "c"]).tiles.map((t) => t.cameraId)).toEqual(["a", "b", "c", null]);
  });
});
