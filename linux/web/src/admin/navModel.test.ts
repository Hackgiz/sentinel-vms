import { describe, expect, it } from "vitest";
import { allLeaves, breadcrumb, buildTree, catalog, filterTree, hashParams, moveFocus, parseHash, routeHash, sectionHash, siteTitle, visibleRows, type SectionInfo } from "./navModel";

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
  it("has 25 catalog leaves, Sentinel Cloud after Site Information", () => {
    // WAVE12(C): +1 for Sentinel Cloud; WAVE14(C): +1 for Insights; WAVE16(F): +2 for People and Plates; WAVE22(C): +1 for Recording
    expect(catalog.flatMap((c) => c.leaves)).toHaveLength(25);
    const t = buildTree("Admin", [...sections, { id: "site", label: "Site information", adminOnly: true }, { id: "cloud", label: "Sentinel Cloud", adminOnly: true }]);
    expect(t.categories[0].leaves.map((l) => l.id)).toEqual(["overview", "site", "cloud"]);
    expect(allLeaves(t).find((l) => l.id === "cloud")?.hash).toBe("#/admin/basics/sentinel-cloud");
  });
  it("puts Insights directly under Overview, for Supervisors too", () => {
    const withInsights = [...sections, { id: "site", label: "Site information", adminOnly: true }, { id: "insights", label: "Insights", adminOnly: false }];
    const t = buildTree("Admin", withInsights);
    expect(t.categories[0].leaves.map((l) => l.id)).toEqual(["overview", "insights", "site"]);
    expect(allLeaves(t).find((l) => l.id === "insights")?.hash).toBe("#/admin/basics/insights");
    const sup = buildTree("Supervisor", withInsights);
    expect(sup.categories[0].label).toBe("Basics");
    expect(sup.categories[0].leaves.map((l) => l.id)).toEqual(["insights"]);
  });
  it("puts People then Plates after Users, readable by Supervisors", () => {
    const withWave16 = [...sections, { id: "roles", label: "Roles", adminOnly: true }, { id: "directory", label: "Directory", adminOnly: true }, { id: "people", label: "People", adminOnly: false }, { id: "plates", label: "Plates", adminOnly: false }];
    const t = buildTree("Admin", withWave16);
    expect(t.categories.find((c) => c.slug === "security")?.leaves.map((l) => l.id)).toEqual(["roles", "users", "people", "plates", "directory"]);
    expect(allLeaves(t).find((l) => l.id === "people")?.hash).toBe("#/admin/security/people");
    expect(sectionHash("plates")).toBe("#/admin/security/plates");
    const sup = buildTree("Supervisor", withWave16);
    expect(sup.categories.find((c) => c.slug === "security")?.leaves.map((l) => l.id)).toEqual(["people", "plates"]);
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
  it("routes deep links and reads their parameters", () => {
    const r = parseHash("#/admin/devices/cameras?camera=cam-7", t);
    expect(r.kind === "leaf" && r.leaf.id).toBe("cameras");
    const legacy = parseHash("#/admin/cameras?camera=cam-7", t);
    expect(legacy.kind === "leaf" && legacy.leaf.id).toBe("cameras");
    expect(hashParams("#/admin/devices/cameras?camera=cam-7").get("camera")).toBe("cam-7");
    expect(hashParams("#/admin/devices/cameras").get("camera")).toBeNull();
  });
  it("links to a section by id", () => {
    expect(sectionHash("rules", { template: "after-hours-people" })).toBe("#/admin/rules-and-events/rules?template=after-hours-people");
    expect(sectionHash("health")).toBe("#/admin/system-dashboard/system-monitor");
    expect(sectionHash("cameras", { camera: "a b" })).toBe("#/admin/devices/cameras?camera=a+b");
    expect(hashParams(sectionHash("cameras", { camera: "a b" })).get("camera")).toBe("a b");
    expect(sectionHash("newthing")).toBe("#/admin/newthing");
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
