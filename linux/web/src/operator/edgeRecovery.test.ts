import { describe, expect, it } from "vitest";
import type { EdgeRecovery } from "../shared/api";
import { edgeJobActive, edgeJobText, edgeRanges, gapAt, maxEdgeRecoveryMs, recoverTarget, recoverTitle, uncoveredMs } from "./edgeRecovery";

const m = 60e3;
const spans = [
  { start: 0, end: 10 * m },
  { start: 10 * m, end: 15 * m, edge: true },
  { start: 15 * m, end: 20 * m, edge: true },
  { start: 30 * m, end: 40 * m },
  { start: 40 * m + 5e3, end: 50 * m, edge: false },
];
const day = { start: 0, end: 24 * 60 * m };

describe("edgeRanges (the timeline hatch)", () => {
  it("keeps only recovered spans, merged into stretches", () => {
    expect(edgeRanges(spans)).toEqual([{ start: 10 * m, end: 20 * m }]);
    expect(edgeRanges([])).toEqual([]);
    expect(edgeRanges([{ start: 5, end: 9 }])).toEqual([]);
  });
});

describe("uncoveredMs", () => {
  it("measures the unrecorded part of a range", () => {
    expect(uncoveredMs(spans, { start: 0, end: 20 * m })).toBe(0);
    expect(uncoveredMs(spans, { start: 15 * m, end: 35 * m })).toBe(10 * m);
    expect(uncoveredMs([], { start: 0, end: m })).toBe(m);
  });
});

describe("gapAt", () => {
  it("finds the hole around the playhead, bounded by footage, the day and now", () => {
    expect(gapAt(spans, 25 * m, day, 24 * 60 * m)).toEqual({ start: 20 * m, end: 30 * m });
    expect(gapAt(spans, 5 * m, day, 24 * 60 * m)).toBeNull(); // on footage
    expect(gapAt(spans, 40 * m + 2e3, day, 24 * 60 * m)).toBeNull(); // 5 s of keyframe slack
    expect(gapAt(spans, 55 * m, day, 70 * m)).toEqual({ start: 50 * m, end: 70 * m }); // up to now
    expect(gapAt(spans, 80 * m, day, 70 * m)).toBeNull(); // in the future
    expect(gapAt([], 5 * m, { start: 2 * m, end: 9 * m }, 1e12)).toEqual({ start: 2 * m, end: 9 * m });
  });
});

describe("recoverTarget", () => {
  it("prefers a selection with unrecorded time, else the gap under the playhead", () => {
    expect(recoverTarget({ start: 15 * m, end: 35 * m }, spans, 5 * m, day, 1e12)).toEqual({ start: 15 * m, end: 35 * m });
    // A fully recorded selection falls back to the playhead's gap.
    expect(recoverTarget({ start: 0, end: 10 * m }, spans, 25 * m, day, 1e12)).toEqual({ start: 20 * m, end: 30 * m });
    expect(recoverTarget(null, spans, 5 * m, day, 1e12)).toBeNull();
    expect(recoverTarget(null, spans, null, day, 1e12)).toBeNull();
  });
  it("clamps to now and to 24 hours", () => {
    expect(recoverTarget({ start: 50 * m, end: 90 * m }, spans, null, day, 60 * m)).toEqual({ start: 50 * m, end: 60 * m });
    const long = recoverTarget({ start: 0, end: 30 * 3600e3 }, [], null, { start: 0, end: 30 * 3600e3 }, 1e12);
    expect(long).toEqual({ start: 0, end: maxEdgeRecoveryMs });
  });
  it("describes the target in the tooltip", () => {
    expect(recoverTitle(null)).toMatch(/Select a gap/);
    expect(recoverTitle({ start: 0, end: 10 * m })).toMatch(/10 min range/);
    expect(recoverTitle({ start: 0, end: 90 * m })).toMatch(/1.5 h range/);
  });
});

describe("edge job text", () => {
  const job = (over: Partial<EdgeRecovery>): EdgeRecovery => ({
    taskId: "t1", state: "running", progress: 0, cameraId: "c", fromMs: 0, toMs: 1, gaps: 1, pieces: 0, recovered: 0, skipped: 0, seconds: 0, ...over,
  });
  it("says what happened", () => {
    expect(edgeJobActive(job({}))).toBe(true);
    expect(edgeJobActive(job({ state: "done" }))).toBe(false);
    expect(edgeJobActive(null)).toBe(false);
    expect(edgeJobText(job({}))).toMatch(/Asking the camera/);
    expect(edgeJobText(job({ pieces: 4, progress: 50 }))).toBe("Recovering from camera… 50%");
    expect(edgeJobText(job({ state: "done", gaps: 0 }))).toMatch(/nothing to recover/);
    expect(edgeJobText(job({ state: "done", pieces: 2, skipped: 2 }))).toMatch(/no footage/);
    expect(edgeJobText(job({ state: "done", pieces: 2, recovered: 2, seconds: 1500 }))).toBe("Recovered 25 min from the camera (2 segments).");
    expect(edgeJobText(job({ state: "failed", error: "camera unreachable" }))).toBe("Recovery failed: camera unreachable");
    expect(edgeJobText(job({ state: "canceled" }))).toBe("Recovery canceled.");
  });
});
