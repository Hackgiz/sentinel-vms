import { describe, expect, it } from "vitest";
import { msToNextMinute, activate, badgeText, closeViewTab, fixedWorkspaces, initialTabs, openViewTab, parseTabs, reconcile, serializeTabs, shortcutFor, viewIdOf, viewTabId, visibleFixed, type FixedId, type TabsState } from "./workspaces";

const all = new Set<FixedId>(fixedWorkspaces.map((w) => w.id));
const some = new Set<FixedId>(["live", "map", "search", "alarms"]);

describe("fixed tabs", () => {
  it("shows only registered workspaces, Live always, in model order", () => {
    expect(visibleFixed(some).map((w) => w.id)).toEqual(["live", "map", "search", "alarms"]);
    expect(visibleFixed(new Set()).map((w) => w.id)).toEqual(["live"]);
    expect(visibleFixed(all).map((w) => w.label)).toEqual(["Live", "Map", "Search", "Exports", "Alarm Manager", "Incidents", "System Monitor"]);
  });
  it("gives every tab its own Ctrl digit, none colliding with Playback (2) or Events (4)", () => {
    const keys = fixedWorkspaces.map((w) => w.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).not.toContain(2);
    expect(keys).not.toContain(4);
  });
});

describe("badge", () => {
  it("is empty at zero and 9+ past nine", () => {
    expect([0, 1, 9, 10, 250].map(badgeText)).toEqual(["", "1", "9", "9+", "9+"]);
  });
});

describe("view tabs", () => {
  it("opens once, switches to an open one", () => {
    let s = openViewTab(initialTabs, "a");
    s = openViewTab(s, "b");
    s = openViewTab(s, "a");
    expect(s).toEqual({ active: "view:a", views: ["a", "b"] });
    expect(viewIdOf(s.active)).toBe("a");
    expect(viewIdOf("map")).toBeNull();
  });
  it("closing the active tab activates its left neighbour, else the next, else Live", () => {
    const s: TabsState = { active: "view:b", views: ["a", "b", "c"] };
    expect(closeViewTab(s, "b")).toEqual({ active: "view:a", views: ["a", "c"] });
    expect(closeViewTab({ active: "view:a", views: ["a", "c"] }, "a")).toEqual({ active: "view:c", views: ["c"] });
    expect(closeViewTab({ active: "view:a", views: ["a"] }, "a")).toEqual({ active: "live", views: [] });
  });
  it("closing another tab keeps the active one", () => {
    expect(closeViewTab({ active: "map", views: ["a"] }, "a")).toEqual({ active: "map", views: [] });
    expect(closeViewTab({ active: "map", views: ["a"] }, "zzz")).toEqual({ active: "map", views: ["a"] });
  });
  it("activate is a no-op on the active tab", () => {
    const s = initialTabs;
    expect(activate(s, "live")).toBe(s);
    expect(activate(s, "map").active).toBe("map");
  });
});

describe("reconcile", () => {
  it("drops tabs of views that are gone and falls back to Live", () => {
    const s: TabsState = { active: "view:b", views: ["a", "b"] };
    expect(reconcile(s, all, (id) => id === "a")).toEqual({ active: "live", views: ["a"] });
  });
  it("falls back from a workspace that isn't registered", () => {
    expect(reconcile({ active: "exports", views: [] }, some, () => true).active).toBe("live");
    expect(reconcile({ active: "map", views: [] }, some, () => true).active).toBe("map");
  });
  it("returns the same object when nothing changes", () => {
    const s: TabsState = { active: "view:a", views: ["a"] };
    expect(reconcile(s, some, () => true)).toBe(s);
  });
});

describe("shortcuts", () => {
  it("keeps Ctrl+1..5", () => {
    expect(shortcutFor("1", some)).toEqual({ tab: "live", mode: "live" });
    expect(shortcutFor("2", some)).toEqual({ tab: "live", mode: "playback" });
    expect(shortcutFor("3", some)).toEqual({ tab: "alarms" });
    expect(shortcutFor("4", some)).toEqual({ tab: "live", events: true });
    expect(shortcutFor("5", some)).toEqual({ tab: "search" });
  });
  it("numbers the new tabs onward, only when present", () => {
    expect(shortcutFor("6", some)).toEqual({ tab: "map" });
    expect(shortcutFor("7", some)).toBeNull();
    expect(shortcutFor("7", all)).toEqual({ tab: "exports" });
    expect(shortcutFor("9", all)).toEqual({ tab: "monitor" });
    expect(shortcutFor("0", all)).toBeNull();
    expect(shortcutFor("b", all)).toBeNull();
  });
});

describe("persistence", () => {
  it("round-trips and reads garbage as the default", () => {
    const s: TabsState = { active: "view:x", views: ["x", "y"] };
    expect(parseTabs(serializeTabs(s))).toEqual(s);
    expect(parseTabs("nope")).toEqual(initialTabs);
    expect(parseTabs(null)).toEqual(initialTabs);
    expect(parseTabs('{"active":3,"views":["a","a",7,""]}')).toEqual({ active: "live", views: ["a"] });
  });
  it("viewTabId inverts viewIdOf", () => {
    expect(viewIdOf(viewTabId("q"))).toBe("q");
  });
});

describe("clock", () => {
  it("ticks on the minute", () => {
    expect(msToNextMinute(0)).toBe(60_000);
    expect(msToNextMinute(59_999)).toBe(1);
    expect(msToNextMinute(120_000 + 30_000)).toBe(30_000);
  });
});
