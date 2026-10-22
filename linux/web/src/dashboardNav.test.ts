import { describe, expect, it } from "vitest";
import type { User } from "./api";
import { alarmBadge, currentPage, homeCan, homeLinks, pageFromHash, pages, visiblePages } from "./dashboardNav";

const user = (role: User["role"]): User => ({ id: "u", name: "n", role, createdAt: 0 });

describe("pageFromHash", () => {
  it("lands on Home without a page or with an unknown one", () => {
    expect(pageFromHash("")).toBe("home");
    expect(pageFromHash("#/")).toBe("home");
    expect(pageFromHash("#/nope")).toBe("home");
  });
  it("reads the page before a path or query", () => {
    expect(pageFromHash("#/alarms")).toBe("alarms");
    expect(pageFromHash("#/live/cam-1/1700000000000")).toBe("live");
    expect(pageFromHash("#/cloud?checkout=success")).toBe("cloud");
    expect(pageFromHash("#/admin/basics/overview")).toBe("admin");
  });
  it("sends old Playback and Compare links to Live", () => {
    expect(pageFromHash("#/playback")).toBe("live");
    expect(pageFromHash("#/compare")).toBe("live");
  });
});

describe("visiblePages", () => {
  it("shows an Admin everything, Home first", () => {
    const v = visiblePages(user("Admin"));
    expect(v.map((p) => p.id)).toEqual(pages.map((p) => p.id));
    expect(v[0].id).toBe("home");
  });
  it("hides the management pages from a Viewer, as before", () => {
    const ids = visiblePages(user("Viewer")).map((p) => p.id);
    expect(ids).toEqual(["home", "live", "alarms", "rules", "events", "recordings", "cloud", "system"]);
  });
  it("gives a Supervisor Audit and Admin but not Users", () => {
    const ids = visiblePages(user("Supervisor")).map((p) => p.id);
    expect(ids).toContain("audit");
    expect(ids).toContain("admin");
    expect(ids).not.toContain("users");
  });
  it("keeps every rail label short enough for the 84 px rail", () => {
    for (const p of pages) expect(p.short.length).toBeLessThanOrEqual(10);
  });
});

describe("currentPage", () => {
  it("falls back to Home for a page the user may not open", () => {
    const v = visiblePages(user("Viewer"));
    expect(currentPage("users", v)).toBe("home");
    expect(currentPage("events", v)).toBe("events");
  });
});

describe("alarmBadge", () => {
  it("is empty at zero and caps at 9+", () => {
    expect(alarmBadge(0)).toBe("");
    expect(alarmBadge(3)).toBe("3");
    expect(alarmBadge(15)).toBe("9+");
  });
});

describe("homeLinks", () => {
  it("opens a camera and a recording on the Live page", () => {
    expect(homeLinks.camera("cam 1")).toBe("#/live/cam%201");
    expect(homeLinks.playback("c", 1700000000000)).toBe("#/live/c/1700000000000");
    expect(pageFromHash(homeLinks.camera("x"))).toBe("live");
  });
  it("opens Insights and People in Admin, for those who may", () => {
    expect(pageFromHash(homeLinks.insights)).toBe("admin");
    expect(pageFromHash(homeLinks.people)).toBe("admin");
    expect(homeCan(user("Viewer"))).toEqual({ alarms: true, insights: false, people: false });
    expect(homeCan(user("Supervisor"))).toEqual({ alarms: true, insights: true, people: true });
  });
});
