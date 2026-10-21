import { describe, expect, it } from "vitest";
import { ApiError } from "../shared/api";
import { emptySearch } from "./searchModel";
import { availability, scoreBars, scoreText, semanticError, semanticRequest, textProblem } from "./semanticModel";

const now = Date.UTC(2026, 9, 21, 12);

describe("semantic search", () => {
  it("is usable only when the server says available, with the reason as tooltip", () => {
    expect(availability(null).enabled).toBe(false);
    const off = availability({ available: false, reason: "clip_text.onnx is missing" });
    expect(off).toEqual({ enabled: false, tooltip: 'Semantic search is unavailable: clip_text.onnx is missing. Download the "Semantic search" models under System → Models.' });
    expect(availability({ available: false }).tooltip).toContain("no semantic search model is installed");
    const on = availability({ available: true, indexed: 1234 });
    expect(on.enabled).toBe(true);
    expect(on.tooltip).toContain("1,234 events indexed");
  });

  it("checks the text length", () => {
    expect(textProblem(" a ")).toMatch(/few words/);
    expect(textProblem("x".repeat(201))).toMatch(/200/);
    expect(textProblem("red truck")).toBeNull();
  });

  it("scopes the request like the filter bar", () => {
    expect(semanticRequest("  a cat on the porch ", { ...emptySearch, range: "24h", cameras: ["c1"] }, now)).toEqual({ text: "a cat on the porch", limit: 30, cameras: ["c1"], from: now - 24 * 3600e3 });
    expect(semanticRequest("van", { ...emptySearch, range: "all" }, now, 500)).toEqual({ text: "van", limit: 100 });
    expect(semanticRequest("van", { ...emptySearch, range: "custom", from: 10, to: 20 }, now, 0)).toEqual({ text: "van", limit: 1, from: 10, to: 20 });
  });

  it("draws bars relative to the best hit", () => {
    expect(scoreBars([{ score: 0.32 }, { score: 0.24 }, { score: -0.1 }])).toEqual([100, 75, 0]);
    expect(scoreBars([])).toEqual([]);
    expect(scoreBars([{ score: 0 }])).toEqual([0]);
    expect(scoreText(0.3149)).toBe("0.31");
  });

  it("turns a 503 into the unavailable message", () => {
    expect(semanticError(new ApiError(503, "unavailable", "semantic search is unavailable: clip_image.onnx is missing"))).toEqual({ text: "Semantic search is unavailable: clip_image.onnx is missing", unavailable: true });
    expect(semanticError(new ApiError(400, "bad_request", "text is too short"))).toEqual({ text: "text is too short", unavailable: false });
  });
});
