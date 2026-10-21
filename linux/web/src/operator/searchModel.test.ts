import { describe, expect, it } from "vitest";
import { activeCount, emptySearch, fromFilter, toQuery, windowOf } from "./searchModel";

const now = Date.UTC(2026, 9, 15, 12);

describe("search model", () => {
  it("resolves presets against now", () => {
    expect(windowOf({ ...emptySearch, range: "1h" }, now)).toEqual({ from: now - 3600e3, to: 0 });
    expect(windowOf({ ...emptySearch, range: "all" }, now)).toEqual({ from: 0, to: 0 });
    expect(windowOf({ ...emptySearch, range: "custom", from: 5, to: 9 }, now)).toEqual({ from: 5, to: 9 });
  });

  it("builds query parameters, leaving unset filters out", () => {
    const q = toQuery({ ...emptySearch, range: "24h", cameras: ["a", "b"], kinds: ["Person"], q: " red jacket ", threatOnly: true, minScore: 0.5, anomaly: false }, now);
    expect(q).toEqual({
      cameras: "a,b",
      kinds: "Person",
      labels: undefined,
      from: now - 24 * 3600e3,
      to: undefined,
      minScore: 0.5,
      threats: "elevated,high",
      anomaly: false,
      q: "red jacket",
    });
    expect(toQuery({ ...emptySearch, range: "all" }, now)).toMatchObject({ from: undefined, cameras: undefined, q: undefined, threats: undefined });
  });

  it("turns a server filter back into bar state", () => {
    const s = fromFilter({ kinds: ["Person"], from: 100, to: 200, q: "van", threats: ["high", "elevated"], anomaly: true });
    expect(s).toMatchObject({ range: "custom", from: 100, to: 200, kinds: ["Person"], q: "van", threatOnly: true, threats: [], anomaly: true });
    expect(fromFilter({ threats: ["low"] })).toMatchObject({ range: "all", threatOnly: false, threats: ["low"] });
    expect(activeCount(s)).toBe(4);
  });

  it("carries the wave-16 plate, person and track filters", () => {
    const q = toQuery({ ...emptySearch, plate: " ab-12 ", person: "p1", track: "cam1:7" }, now);
    expect(q).toMatchObject({ plate: "AB12", person: "p1", track: "cam1:7" });
    expect(toQuery({ ...emptySearch, plate: " - " }, now).plate).toBeUndefined();
    expect(activeCount({ ...emptySearch, plate: "AB", track: "c:1" })).toBe(2);
    expect(activeCount({ ...emptySearch, plate: "--" })).toBe(0);
    expect(fromFilter({ plate: "AB12", person: "p1", track: "c:1" })).toMatchObject({ plate: "AB12", person: "p1", track: "c:1" });
    expect(fromFilter({})).toMatchObject({ plate: "", person: "", track: "" });
  });
});
