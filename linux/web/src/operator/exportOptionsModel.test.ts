import { describe, expect, it } from "vitest";
import { addRegion, maxRegions, noOptions, optionsBody, optionsReencode, optionsSummary, rectFrom, regionProblem, removeRegion } from "./exportOptionsModel";

describe("export options model", () => {
  it("builds a rectangle from two corners in any order", () => {
    expect(rectFrom([0.5, 0.6], [0.2, 0.1])).toEqual({ x0: 0.2, y0: 0.1, x1: 0.5, y1: 0.6 });
    expect(rectFrom([0.123456, 0], [0.5, 0.5]).x0).toBe(0.1235);
  });

  it("rejects out-of-frame and tiny regions", () => {
    expect(regionProblem({ x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.4 })).toBeNull();
    expect(regionProblem({ x0: -0.1, y0: 0, x1: 0.4, y1: 0.4 })).toMatch(/outside/);
    expect(regionProblem({ x0: 0.1, y0: 0.1, x1: 0.105, y1: 0.4 })).toMatch(/small/);
  });

  it("adds regions up to the limit and ignores clicks", () => {
    let list = addRegion([], { x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.4 });
    expect(list).toHaveLength(1);
    expect(addRegion(list, { x0: 0.2, y0: 0.2, x1: 0.2, y1: 0.2 })).toBe(list);
    for (let i = 0; i < 12; i++) list = addRegion(list, { x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.4 });
    expect(list).toHaveLength(maxRegions);
    expect(removeRegion(list, 0)).toHaveLength(maxRegions - 1);
  });

  it("only sends the options that are on", () => {
    expect(optionsBody(noOptions)).toEqual({});
    expect(optionsBody({ watermark: true, blurRegions: [] })).toEqual({ watermark: true });
    const r = { x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.4 };
    expect(optionsBody({ watermark: false, blurRegions: [r] })).toEqual({ blurRegions: [r] });
    // Never free text: the body has no watermark text field.
    expect(Object.keys(optionsBody({ watermark: true, blurRegions: [r] })).sort()).toEqual(["blurRegions", "watermark"]);
  });

  it("describes the options", () => {
    const r = { x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.4 };
    expect(optionsSummary(noOptions)).toBe("");
    expect(optionsSummary({ watermark: true, blurRegions: [r, r] })).toBe("watermark, 2 pixelated regions");
    expect(optionsSummary({ watermark: false, blurRegions: [r] })).toBe("1 pixelated region");
    expect(optionsReencode(noOptions)).toBe(false);
    expect(optionsReencode({ watermark: true, blurRegions: [] })).toBe(true);
  });
});
