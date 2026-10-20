import { describe, expect, it } from "vitest";
import { cameraRows, series, severityTotals, type Activity } from "./reportModel";

const H = 3600_000;
const base: Activity = {
  from: 10 * H,
  to: 13 * H,
  totals: { events: 0, alarms: 0 },
  events: [
    { cameraId: "c1", kind: "Person", hour: 10 * H, count: 3 },
    { cameraId: "c1", kind: "Vehicle", hour: 11 * H, count: 1 },
    { cameraId: "c2", kind: "Motion", hour: 11 * H, count: 5 },
  ],
  alarms: [
    { cameraId: "c1", kind: "Person", severity: "Critical", count: 1 },
    { cameraId: "c2", kind: "Motion", severity: "Info", count: 2 },
    { cameraId: "c2", kind: "Motion", severity: "Warning", count: 1 },
  ],
};

describe("cameraRows", () => {
  it("sums per camera, names them, and picks the most common kind", () => {
    const rows = cameraRows(base, new Map([["c1", "Front door"]]));
    expect(rows).toEqual([
      { cameraId: "c2", name: "c2", events: 5, alarms: 3, topKind: "Motion" },
      { cameraId: "c1", name: "Front door", events: 4, alarms: 1, topKind: "Person" },
    ]);
  });

  it("is empty when nothing happened", () => {
    expect(cameraRows({ ...base, events: [], alarms: [] }, new Map())).toEqual([]);
  });
});

describe("severityTotals", () => {
  it("orders Critical, then Warning, then Info, then anything else", () => {
    const withOther: Activity = { ...base, alarms: [...base.alarms, { cameraId: "c1", kind: "x", severity: "Notice", count: 9 }] };
    expect(severityTotals(withOther).map((s) => s.severity)).toEqual(["Critical", "Warning", "Info", "Notice"]);
  });

  it("adds counts that share a severity", () => {
    expect(severityTotals(base).find((s) => s.severity === "Info")?.count).toBe(2);
  });
});

describe("series", () => {
  it("fills hourly buckets with zeros for empty hours", () => {
    const s = series(base, "hour");
    expect(s.map((b) => b.count)).toEqual([3, 6, 0]);
  });

  it("covers the whole range even when the first bucket is empty", () => {
    const late: Activity = { ...base, events: [{ cameraId: "c1", kind: "Person", hour: 12 * H, count: 2 }] };
    expect(series(late, "hour").map((b) => b.count)).toEqual([0, 0, 2]);
  });
});
