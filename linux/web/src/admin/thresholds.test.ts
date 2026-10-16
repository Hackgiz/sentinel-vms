import { describe, expect, it } from "vitest";
import { DEFAULT_THRESHOLDS, levelColor, levelFor, thresholdError } from "./thresholds";

describe("levelFor", () => {
  const t = { amber: 70, red: 90 };
  it("changes level exactly at the marks", () => {
    expect(levelFor(69.9, t)).toBe("ok");
    expect(levelFor(70, t)).toBe("amber");
    expect(levelFor(89.9, t)).toBe("amber");
    expect(levelFor(90, t)).toBe("red");
    expect(levelFor(100, t)).toBe("red");
  });
  it("treats a missing reading as ok", () => {
    expect(levelFor(null, t)).toBe("ok");
    expect(levelFor(undefined, t)).toBe("ok");
    expect(levelFor(NaN, t)).toBe("ok");
  });
  it("maps levels to colours", () => {
    expect(levelColor("ok")).toBeUndefined();
    expect(levelColor("amber")).toBe("var(--amber)");
    expect(levelColor("red")).toBe("var(--red)");
  });
});

describe("thresholdError", () => {
  it("accepts the defaults", () => expect(thresholdError(DEFAULT_THRESHOLDS)).toBeNull());
  it("rejects amber at or above red, and out-of-range values", () => {
    expect(thresholdError({ ...DEFAULT_THRESHOLDS, cpu: { amber: 90, red: 90 } })).toMatch(/CPU/);
    expect(thresholdError({ ...DEFAULT_THRESHOLDS, disk: { amber: 0, red: 90 } })).toMatch(/DISK/);
    expect(thresholdError({ ...DEFAULT_THRESHOLDS, mem: { amber: 50, red: 101 } })).toMatch(/MEM/);
    expect(thresholdError({ ...DEFAULT_THRESHOLDS, mem: { amber: 50.5, red: 90 } })).toMatch(/MEM/);
  });
});
