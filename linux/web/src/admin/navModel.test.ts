import { describe, expect, it } from "vitest";
import { allLeaves, breadcrumb, buildTree, filterTree, moveFocus, parseHash, routeHash, siteTitle, visibleRows, type SectionInfo } from "./navModel";

const sections: SectionInfo[] = [
  { id: "overview", label: "Overview", adminOnly: true },
  { id: "servers", label: "Recording servers", adminOnly: true },
  { id: "cameras", label: "Cameras", adminOnly: true },
  { id: "groups", label: "Groups", adminOnly: true },
  { id: "users", label: "Users", adminOnly: true },
  { id: "audit", label: "Audit", adminOnly: false },
  { id: "health", label: "Health", adminOnly: false },
];

describe("buildTree", () => {
  it("places sections into the contract's categories", () => {
    const t = buildTree("Admin", sections);
    expect(t.categories.map((c) => c.label)).toEqual(["Basics", "Servers", "Devices", "Security", "System Dashboard", "Logs"]);
    expect(allLeaves(t).find((l) => l.id === "users")?.hash).toBe("#/admin/security/users");
  });
  it("shows a Supervisor only Audit and Health", () => {
    const t = buildTree("Supervisor", sections);
    expect(allLeaves(t).map((l) => l.id).sort()).toEqual(["audit", "health"]);
    expect(t.categories.map((c) => c.label)).toEqual(["System Dashboard", "Logs"]);
  });
  it("puts unplaced appended sections under Other", () => {
    const t = buildTree("Admin", [...sections, { id: "newthing", label: "New Thing", adminOnly: true }]);
    const other = t.categories.at(-1)!;
    expect(other.label).toBe("Other");
    expect(other.leaves[0].hash).toBe("#/admin/other/newthing");
  });
  it("hides not-yet-merged catalog leaves", () => {
    expect(allLeaves(buildTree("Admin", sections)).some((l) => l.id === "roles")).toBe(false);
  });
});

describe("filterTree", () => {
  const t = buildTree("Admin", sections);
  it("narrows to matching leaves", () => {
    const f = filterTree(t, "cameras");
    expect(allLeaves(f).map((l) => l.id)).toEqual(["cameras"]);
  });
  it("keeps all leaves of a matching category", () => {
    expect(allLeaves(filterTree(t, "devices")).map((l) => l.id)).toEqual(["cameras", "groups"]);
  });
  it("empty query is identity; no match is empty", () => {
    expect(filterTree(t, " ")).toBe(t);
    expect(filterTree(t, "zzz").categories).toEqual([]);
  });
});

describe("hash routing", () => {
  const t = buildTree("Admin", sections);
  it("round-trips every leaf", () => {
    for (const leaf of allLeaves(t)) {
      const r = parseHash(leaf.hash, t);
      expect(r.kind === "leaf" && r.leaf.id).toBe(leaf.id);
      expect(routeHash(r)).toBe(leaf.hash);
    }
  });
  it("resolves legacy single-segment links", () => {
    const r = parseHash("#/admin/servers", t);
    expect(r.kind === "leaf" && r.leaf.slug).toBe("recording-servers");
  });
  it("sends unknown or hidden hashes to the root", () => {
    expect(parseHash("#/admin/nope/nothing", t).kind).toBe("root");
    expect(parseHash("#/admin", t).kind).toBe("root");
    expect(parseHash("#/other", t).kind).toBe("root");
    expect(parseHash("#/admin/devices/cameras", buildTree("Supervisor", sections)).kind).toBe("root");
  });
  it("builds breadcrumbs", () => {
    expect(breadcrumb(allLeaves(t)[0])).toEqual(["Basics", "Overview"]);
  });
});

describe("rows and keyboard", () => {
  const t = buildTree("Admin", sections);
  it("hides leaves of collapsed categories unless filtering", () => {
    const collapsed = { devices: false };
    expect(visibleRows(t, collapsed, false).some((r) => r.key === "leaf:cameras")).toBe(false);
    expect(visibleRows(t, collapsed, true).some((r) => r.key === "leaf:cameras")).toBe(true);
  });
  it("moves focus up/down, clamped", () => {
    const rows = visibleRows(t, {}, false);
    expect(moveFocus(rows, "root", -1)).toBe("root");
    expect(moveFocus(rows, "root", 1)).toBe(rows[1].key);
    expect(moveFocus(rows, rows.at(-1)!.key, 1)).toBe(rows.at(-1)!.key);
  });
});

describe("siteTitle", () => {
  it("falls back to Sentinel", () => {
    expect(siteTitle(undefined)).toBe("Sentinel");
    expect(siteTitle("  ")).toBe("Sentinel");
    expect(siteTitle(" HQ ")).toBe("HQ");
  });
});
