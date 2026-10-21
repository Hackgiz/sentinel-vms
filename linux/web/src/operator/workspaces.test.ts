import { describe, expect, it } from "vitest";
import type { Permission } from "../shared/api";
import {
  msToNextMinute,
  activate,
  badgeText,
  closeViewTab,
  desktopMenuLetters,
  fixedWorkspaces,
  initialTabs,
  keyLabel,
  openViewTab,
  operatorLink,
  parseTabs,
  permitted,
  reconcile,
  reservedLetters,
  serializeTabs,
  shortcutFor,
  viewIdOf,
  viewTabId,
  visibleFixed,
  type FixedId,
  type TabsState,
} from "./workspaces";

const all = new Set<FixedId>(fixedWorkspaces.map((w) => w.id));
const some = new Set<FixedId>(["live", "map", "search", "alarms"]);

describe("fixed tabs", () => {
  it("shows only registered workspaces, Live always, in model order", () => {
    expect(visibleFixed(some).map((w) => w.id)).toEqual(["live", "map", "search", "alarms"]);
    expect(visibleFixed(new Set()).map((w) => w.id)).toEqual(["live"]);
    expect(visibleFixed(all).map((w) => w.label)).toEqual(["Live", "Map", "Search", "Exports", "Alarm Manager", "Incidents", "System Monitor", "Reports", "Insights", "Rules", "People", "Plates"]);
  });
  it("gives every tab its own Ctrl key, none colliding with Playback (2), Events (4) or the sidebar (B)", () => {
    const keys = fixedWorkspaces.map((w) => w.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).not.toContain(2);
    expect(keys).not.toContain(4);
    for (const l of reservedLetters) expect(keys).not.toContain(l);
    // WAVE17: nor a letter the desktop app's menu binds (copy, paste, close…).
    for (const l of desktopMenuLetters) expect(keys).not.toContain(l);
    // A letter key is one lowercase letter (shortcutFor matches e.key exactly), and not R (the desktop app's Reload).
    for (const k of keys) if (typeof k === "string") expect(k).toMatch(/^[a-qs-z]$/);
  });
  it("labels the shortcut in the tooltip", () => {
    expect(fixedWorkspaces.map(keyLabel)).toContain("Ctrl+9");
    expect(fixedWorkspaces.filter((w) => w.id === "insights" || w.id === "rules").map(keyLabel)).toEqual(["Ctrl+I", "Ctrl+E"]);
    expect(fixedWorkspaces.filter((w) => w.id === "people" || w.id === "plates").map(keyLabel)).toEqual(["Ctrl+P", "Ctrl+L"]);
  });
  it("binds Ctrl+P and Ctrl+L to People and Plates only when those panes are there (WAVE17)", () => {
    expect(shortcutFor("p", all)).toEqual({ tab: "people" });
    expect(shortcutFor("l", all)).toEqual({ tab: "plates" });
    expect(shortcutFor("p", some)).toBeNull();
    expect(shortcutFor("l", some)).toBeNull();
    expect(shortcutFor("P", all)).toBeNull();
  });
});

// WAVE15: Insights and Rules from the Admin, gated like the server's routes.
describe("pane permissions", () => {
  const as = (...perms: Permission[]) => (p: Permission) => perms.includes(p);
  const shown = (can: (p: Permission) => boolean) => fixedWorkspaces.filter((w) => permitted(w.id, can)).map((w) => w.id);
  it("shows Insights with viewLive and Rules with changeSettings", () => {
    expect(shown(as("viewLive"))).toContain("insights");
    expect(shown(as("viewLive"))).not.toContain("rules");
    expect(shown(as("changeSettings"))).toContain("rules");
    expect(shown(as("changeSettings"))).not.toContain("insights");
    expect(shown(as("viewLive", "changeSettings"))).toEqual(expect.arrayContaining(["insights", "rules"]));
  });
  it("hides both from a user with neither, and keeps the other tabs", () => {
    const none = shown(() => false);
    expect(none).not.toContain("insights");
    expect(none).not.toContain("rules");
    expect(none).toEqual(["live", "map", "search", "exports", "alarms", "incidents", "monitor", "reports"]);
  });
  it("shows People and Plates with viewLive, without needing changeSettings (WAVE17)", () => {
    expect(shown(as("viewLive"))).toEqual(expect.arrayContaining(["people", "plates"]));
    expect(shown(as("changeSettings"))).not.toContain("people");
    expect(shown(as("changeSettings"))).not.toContain("plates");
    expect(shown(() => false)).not.toContain("people");
  });
});

describe("Insights links", () => {
  it("opens the Rules tab on the template and System Monitor for storage; no camera screen here", () => {
    expect(operatorLink({ to: "rules", templateId: "after-hours-people" })).toEqual({ tab: "rules", templateId: "after-hours-people" });
    expect(operatorLink({ to: "storage" })).toEqual({ tab: "monitor" });
    expect(operatorLink({ to: "camera", cameraId: "cam-3" })).toBeNull();
  });
  it("targets tabs that exist", () => {
    const ids = fixedWorkspaces.map((w) => w.id as string);
    expect(ids).toContain(operatorLink({ to: "rules", templateId: "x" })!.tab);
    expect(ids).toContain(operatorLink({ to: "storage" })!.tab);
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
  it("binds the lettered tabs, lowercase only, only when present", () => {
    expect(shortcutFor("i", all)).toEqual({ tab: "insights" });
    expect(shortcutFor("e", all)).toEqual({ tab: "rules" });
    expect(shortcutFor("I", all)).toBeNull();
    expect(shortcutFor("i", some)).toBeNull();
    expect(shortcutFor("r", all)).toBeNull();
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
