import { describe, expect, it } from "vitest";
import { ApiError, permissionsOf, type Permission } from "../shared/api";
import {
  DEFAULT_RANGE,
  OTHER,
  actionFor,
  actionPermission,
  alertChangeError,
  barLabel,
  dayLabel,
  formatShare,
  isEmpty,
  kindLabel,
  rangeQuery,
  stackDaily,
  type Insights,
  type InsightsDay,
  type Recommendation,
} from "./insights";

const DAY = 24 * 3600_000;
const day = (d: string, byKind: Record<string, number>, events = 0): InsightsDay => ({
  day: d,
  events,
  alarms: Object.values(byKind).reduce((s, n) => s + n, 0),
  byKind,
});
const canAll = () => true;
const canOnly = (...perms: Permission[]) => (p: Permission) => perms.includes(p);

describe("rangeQuery", () => {
  const now = Date.UTC(2026, 9, 21, 12);
  it("ends now and reaches back the range's length", () => {
    expect(rangeQuery("24h", now)).toEqual({ from: now - DAY, to: now });
    expect(rangeQuery("7d", now)).toEqual({ from: now - 7 * DAY, to: now });
    expect(rangeQuery("30d", now)).toEqual({ from: now - 30 * DAY, to: now });
  });
  it("defaults to 7 days", () => {
    expect(DEFAULT_RANGE).toBe("7d");
    expect(rangeQuery("nope" as never, now)).toEqual({ from: now - 7 * DAY, to: now });
  });
});

describe("isEmpty", () => {
  const totals = (alarms: number, events: number) => ({ totals: { alarms, events, camerasWithAlarms: 0, offlineCameras: 0 } }) as Insights;
  it("is empty only with no alarms and no events", () => {
    expect(isEmpty(totals(0, 0))).toBe(true);
    expect(isEmpty(totals(1, 0))).toBe(false);
    expect(isEmpty(totals(0, 1))).toBe(false);
  });
});

describe("stackDaily", () => {
  const daily = [
    day("2026-10-15", { Motion: 9, Person: 2, Vehicle: 1, audio: 1 }, 40),
    day("2026-10-16", {}, 3),
    day("2026-10-17", { Person: 3, "Camera Offline": 2, Tamper: 1, Storage: 1 }, 12),
  ];

  it("keeps the four busiest kinds, in rule-kind order, and sums the rest as Other", () => {
    // Totals: Motion 9, Person 5, Camera Offline 2, then Vehicle/Tamper/Storage/audio 1 each; the tie goes by rule-kind order (Vehicle).
    const { kinds, bars } = stackDaily(daily);
    expect(kinds).toEqual(["Motion", "Person", "Vehicle", "Camera Offline", OTHER]);
    expect(bars[0].segments).toEqual([
      { kind: "Motion", count: 9 },
      { kind: "Person", count: 2 },
      { kind: "Vehicle", count: 1 },
      { kind: OTHER, count: 1 },
    ]);
    expect(bars[2].segments).toEqual([
      { kind: "Person", count: 3 },
      { kind: "Camera Offline", count: 2 },
      { kind: OTHER, count: 2 },
    ]);
    expect(bars.map((b) => b.total)).toEqual([13, 0, 7]);
  });

  it("keeps every day, oldest first, with empty days empty", () => {
    const { bars } = stackDaily(daily);
    expect(bars.map((b) => b.day)).toEqual(["2026-10-15", "2026-10-16", "2026-10-17"]);
    expect(bars[1]).toEqual({ day: "2026-10-16", events: 3, total: 0, segments: [] });
  });

  it("has no Other with four kinds or fewer", () => {
    const { kinds } = stackDaily([day("2026-10-15", { Person: 1, Vehicle: 2, Motion: 3, Tamper: 4 })]);
    expect(kinds).toEqual(["Motion", "Person", "Vehicle", "Tamper"]);
  });

  it("gives a kind the same slot whatever its rank", () => {
    const a = stackDaily([day("2026-10-15", { Person: 9, Motion: 1 })]).kinds;
    const b = stackDaily([day("2026-10-15", { Person: 1, Motion: 9 })]).kinds;
    expect(a).toEqual(["Motion", "Person"]);
    expect(b).toEqual(a);
  });

  it("counts alarms byKind doesn't name as Other", () => {
    const { kinds, bars } = stackDaily([{ day: "2026-10-15", events: 0, alarms: 5, byKind: { Person: 2, "": 1 } }]);
    expect(kinds).toEqual(["Person", OTHER]);
    expect(bars[0].segments).toEqual([
      { kind: "Person", count: 2 },
      { kind: OTHER, count: 3 },
    ]);
    expect(bars[0].total).toBe(5);
  });

  it("is empty for no days", () => {
    expect(stackDaily([])).toEqual({ kinds: [], bars: [] });
  });
});

describe("formatShare", () => {
  it("rounds to whole percent", () => {
    expect(formatShare(0.5)).toBe("50%");
    expect(formatShare(1 / 3)).toBe("33%");
    expect(formatShare(2 / 3)).toBe("67%");
    expect(formatShare(0.01)).toBe("1%");
    expect(formatShare(0.99)).toBe("99%");
  });
  it("never shows a sliver as 0% or most as 100%", () => {
    expect(formatShare(0.004)).toBe("<1%");
    expect(formatShare(0.995)).toBe(">99%");
  });
  it("handles none, all and junk", () => {
    expect(formatShare(0)).toBe("0%");
    expect(formatShare(-1)).toBe("0%");
    expect(formatShare(Number.NaN)).toBe("0%");
    expect(formatShare(1)).toBe("100%");
    expect(formatShare(1.2)).toBe("100%");
  });
});

describe("labels", () => {
  it("names kinds", () => {
    expect(kindLabel("audio")).toBe("Audio");
    expect(kindLabel("Camera Offline")).toBe("Camera Offline");
    expect(kindLabel("")).toBe(OTHER);
  });
  it("reads the server's date as a calendar day", () => {
    expect(dayLabel("2026-10-14", "en-US")).toBe("Wed, Oct 14");
    expect(dayLabel("not a day", "en-US")).toBe("not a day");
  });
  it("describes a bar", () => {
    const { bars } = stackDaily([day("2026-10-14", { Person: 3, Motion: 1 }, 1)]);
    expect(barLabel(bars[0], "en-US")).toBe("Wed, Oct 14: 4 alarms (Motion 1, Person 3), 1 event");
    expect(barLabel({ day: "2026-10-14", events: 0, total: 1, segments: [{ kind: "Person", count: 1 }] }, "en-US")).toBe("Wed, Oct 14: 1 alarm (Person 1), 0 events");
    expect(barLabel({ day: "2026-10-14", events: 2, total: 0, segments: [] }, "en-US")).toBe("Wed, Oct 14: 0 alarms, 2 events");
  });
});

describe("actionFor", () => {
  const rec = (action?: Recommendation["action"], cameraId?: string): Recommendation => ({
    id: "r",
    level: "suggestion",
    title: "t",
    detail: "d",
    ...(cameraId ? { cameraId } : {}),
    ...(action ? { action } : {}),
  });
  const motionOnly = rec({ type: "setAlertOn", cameraId: "cam-7", alertOn: "peopleAndVehicles" }, "cam-7");
  const noPeopleRule = rec({ type: "openTemplate", templateId: "after-hours-people" });
  const repeatOffline = rec({ type: "openCamera", cameraId: "cam-3" }, "cam-3");
  const storage = rec({ type: "openStorage" });

  it("changes the camera's alarms in place for setAlertOn", () => {
    expect(actionFor(motionOnly, canAll)).toEqual({ kind: "setAlertOn", label: "Switch to people & vehicles", cameraId: "cam-7", alertOn: "peopleAndVehicles" });
  });

  it("links to the other sections", () => {
    expect(actionFor(noPeopleRule, canAll)).toEqual({ kind: "link", label: "Use template", href: "#/admin/rules-and-events/rules?template=after-hours-people" });
    expect(actionFor(repeatOffline, canAll)).toEqual({ kind: "link", label: "Open camera", href: "#/admin/devices/cameras?camera=cam-3" });
    expect(actionFor(storage, canAll)).toEqual({ kind: "link", label: "Check storage", href: "#/admin/system-dashboard/system-monitor" });
  });

  it("shows each button only with the permission its action needs", () => {
    expect(actionPermission).toEqual({ setAlertOn: "manageCameras", openTemplate: "changeSettings", openCamera: "manageCameras", openStorage: "viewLive" });
    for (const r of [motionOnly, noPeopleRule, repeatOffline, storage]) {
      const needs = actionPermission[r.action!.type];
      expect(actionFor(r, canOnly(needs))).not.toBeNull();
      expect(actionFor(r, (p) => p !== needs)).toBeNull();
    }
  });

  it("leaves a Supervisor only the storage link", () => {
    const supervisor = canOnly(...permissionsOf("Supervisor"));
    expect([motionOnly, noPeopleRule, repeatOffline, storage].map((r) => actionFor(r, supervisor)?.label ?? null)).toEqual([null, null, null, "Check storage"]);
  });

  it("falls back to the recommendation's camera", () => {
    expect(actionFor(rec({ type: "openCamera" }, "cam-9"), canAll)).toMatchObject({ href: "#/admin/devices/cameras?camera=cam-9" });
    expect(actionFor(rec({ type: "setAlertOn", alertOn: "people" }, "cam-9"), canAll)).toMatchObject({ cameraId: "cam-9", label: "Switch to people only" });
  });

  it("has no button without an action or what the action needs", () => {
    expect(actionFor(rec(), canAll)).toBeNull();
    expect(actionFor(rec({ type: "setAlertOn", alertOn: "people" }), canAll)).toBeNull();
    expect(actionFor(rec({ type: "setAlertOn", cameraId: "cam-1" }), canAll)).toBeNull();
    expect(actionFor(rec({ type: "openTemplate" }), canAll)).toBeNull();
    expect(actionFor(rec({ type: "openCamera" }), canAll)).toBeNull();
    expect(actionFor(rec({ type: "somethingNew" } as never), canAll)).toBeNull();
  });
});

describe("alertChangeError", () => {
  it("names the missing permission on a 403", () => {
    expect(alertChangeError(new ApiError(403, "forbidden", "You don't have permission to do that."))).toBe("You need permission to change cameras.");
  });
  it("passes other failures through", () => {
    expect(alertChangeError(new ApiError(404, "not_found", "Not found. It may have been removed."))).toBe("Not found. It may have been removed.");
    expect(alertChangeError(new Error("offline"))).toBe("offline");
  });
});
