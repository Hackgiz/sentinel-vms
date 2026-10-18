import { describe, expect, it } from "vitest";
import { arrowHead, directionArrows, directionOf, lineProblem, severityOf, sideOf } from "./lines";
import type { Pt } from "./zones";

describe("lineProblem (mirrors internal/store/zones_lines.go)", () => {
  it("accepts two distinct in-frame points", () => {
    expect(lineProblem([[0.1, 0.1], [0.9, 0.8]])).toBeNull();
    expect(lineProblem([[0, 0], [1, 1]])).toBeNull();
  });
  it("needs exactly two points", () => {
    expect(lineProblem([])).toMatch(/exactly 2/);
    expect(lineProblem([[0.5, 0.5]])).toMatch(/exactly 2/);
    expect(lineProblem([[0, 0], [0.5, 0.5], [1, 1]])).toMatch(/exactly 2/);
  });
  it("rejects off-frame, coincident and speck-sized lines", () => {
    expect(lineProblem([[0, 0], [1.2, 1]])).toMatch(/outside/);
    expect(lineProblem([[0, 0], [Number.NaN, 1]])).toMatch(/outside/);
    expect(lineProblem([[0.5, 0.5], [0.5, 0.5]])).toMatch(/same place/);
    expect(lineProblem([[0.5, 0.5], [0.5, 0.505]])).toMatch(/too short/);
  });
});

describe("directionOf", () => {
  it("reads the stored direction and defaults to both", () => {
    expect(directionOf({ direction: "ltr" })).toBe("ltr");
    expect(directionOf({ direction: "rtl" })).toBe("rtl");
    expect(directionOf({ direction: "both" })).toBe("both");
    expect(directionOf({ direction: "up" })).toBe("both");
    expect(directionOf({})).toBe("both");
    expect(directionOf(undefined)).toBe("both");
    expect(directionOf(null)).toBe("both");
  });
});

describe("sideOf and the direction arrows", () => {
  const p0: Pt = [0.2, 0.5];
  const p1: Pt = [0.8, 0.5];
  it("puts the right-hand side (ltr destination) below a left-to-right line", () => {
    expect(sideOf(p0, p1, [0.5, 0.7])).toBeGreaterThan(0); // below = right-hand side
    expect(sideOf(p0, p1, [0.5, 0.3])).toBeLessThan(0);
    expect(sideOf(p1, p0, [0.5, 0.7])).toBeLessThan(0); // reversing the line swaps sides
  });
  it("draws ltr toward the right-hand side, rtl toward the left, both for both", () => {
    const [ltr] = directionArrows(p0, p1, "ltr");
    expect(ltr.to[1]).toBeGreaterThan(ltr.from[1]); // down
    expect(ltr.to[0]).toBeCloseTo(ltr.from[0], 6); // perpendicular
    const [rtl] = directionArrows(p0, p1, "rtl");
    expect(rtl.to[1]).toBeLessThan(rtl.from[1]); // up
    expect(directionArrows(p0, p1, "both")).toHaveLength(2);
    // Arrows leave from the middle of the line.
    expect(ltr.from).toEqual([0.5, 0.5]);
  });
  it("keeps arrows on the side the server counts as the destination", () => {
    for (const [a, b] of [[[0.2, 0.2], [0.8, 0.9]], [[0.9, 0.1], [0.1, 0.6]], [[0.5, 0.9], [0.5, 0.1]]] as [Pt, Pt][]) {
      const [arrow] = directionArrows(a, b, "ltr");
      expect(sideOf(a, b, arrow.to)).toBeGreaterThan(0);
    }
  });
  it("stays perpendicular on screen for a wide picture", () => {
    const aspect = 2;
    const [arrow] = directionArrows([0.2, 0.2], [0.8, 0.8], "ltr", 0.1, aspect);
    const along = [(0.8 - 0.2) * aspect, 0.8 - 0.2];
    const out = [(arrow.to[0] - arrow.from[0]) * aspect, arrow.to[1] - arrow.from[1]];
    expect(along[0] * out[0] + along[1] * out[1]).toBeCloseTo(0, 9);
  });
  it("has no arrows for a degenerate line and builds arrowheads", () => {
    expect(directionArrows([0.5, 0.5], [0.5, 0.5], "both")).toEqual([]);
    expect(arrowHead([0.5, 0.5], [0.5, 0.5])).toBe("");
    expect(arrowHead([0.5, 0.5], [0.5, 0.6]).split(" ")).toHaveLength(3);
  });
});

describe("severityOf", () => {
  it("reads the stored severity and defaults to Critical", () => {
    expect(severityOf({ severity: "Critical" })).toBe("Critical");
    expect(severityOf({ severity: "Warning" })).toBe("Warning");
    expect(severityOf({ direction: "ltr", severity: "Info" })).toBe("Info");
    expect(severityOf({ severity: "info" })).toBe("Critical");
    expect(severityOf({ direction: "ltr" })).toBe("Critical");
    expect(severityOf(undefined)).toBe("Critical");
    expect(severityOf(null)).toBe("Critical");
  });
});
