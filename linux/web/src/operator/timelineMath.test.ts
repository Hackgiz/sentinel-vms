import { describe, expect, it } from "vitest";
import {
  clampSpan,
  emptySelection,
  exportBlocker,
  hasLowRes,
  maxSpanMs,
  mergeCoverage,
  mergeSpans,
  minSpanMs,
  nextGap,
  panCenter,
  prevGap,
  selectionRange,
  setIn,
  setOut,
  tickStep,
  ticks,
  timeToX,
  viewRange,
  wheelFactor,
  xToTime,
  zoomSpan,
  type Range,
} from "./timelineMath";

describe("zoom", () => {
  it("stays between 1 minute and 24 hours", () => {
    expect(zoomSpan(minSpanMs, 0.1)).toBe(minSpanMs);
    expect(zoomSpan(maxSpanMs, 10)).toBe(maxSpanMs);
    expect(clampSpan(5 * 60e3)).toBe(5 * 60e3);
    expect(zoomSpan(600e3, 2)).toBe(1200e3);
  });
  it("wheel down zooms out, wheel up zooms in", () => {
    expect(wheelFactor(100)).toBeGreaterThan(1);
    expect(wheelFactor(-100)).toBeLessThan(1);
    expect(wheelFactor(10, true)).toBeGreaterThan(wheelFactor(10));
  });
});

describe("pan and zoom mapping", () => {
  it("centers the playhead", () => {
    const { start, end } = viewRange(1000, 200);
    expect(start).toBe(900);
    expect(end).toBe(1100);
    expect(timeToX(1000, start, 200, 400)).toBe(200);
  });
  it("round-trips x and time", () => {
    const t = xToTime(150, 5000, 60e3, 600);
    expect(t).toBe(5000 + 15e3);
    expect(timeToX(t, 5000, 60e3, 600)).toBeCloseTo(150);
  });
  it("dragging right goes back in time", () => {
    expect(panCenter(100e3, 100, 60e3, 600)).toBe(100e3 - 10e3);
    expect(panCenter(100e3, -100, 60e3, 600)).toBe(110e3);
  });
});

describe("ruler ticks", () => {
  it("picks coarser steps as the span grows", () => {
    expect(tickStep(60e3, 900)).toBe(10e3);
    expect(tickStep(3600e3, 900)).toBe(10 * 60e3);
    expect(tickStep(24 * 3600e3, 900)).toBeGreaterThanOrEqual(3 * 3600e3);
    expect(tickStep(3600e3, 100)).toBeGreaterThan(tickStep(3600e3, 1800));
  });
  it("never goes below one second", () => {
    expect(tickStep(1e3, 5000)).toBe(1000);
  });
  it("aligns to the step in local time and stays inside the window", () => {
    const start = new Date(2026, 2, 3, 10, 7, 20).getTime();
    const end = new Date(2026, 2, 3, 10, 12, 0).getTime();
    const t = ticks(start, end, 60e3);
    expect(t.map((x) => x.label)).toEqual(["10:08", "10:09", "10:10", "10:11", "10:12"]);
    expect(t.every((x) => x.t >= start && x.t <= end)).toBe(true);
  });
  it("labels seconds and midnight, and marks majors", () => {
    const s = new Date(2026, 2, 3, 10, 0, 0).getTime();
    const sec = ticks(s, s + 30e3, 10e3);
    expect(sec.map((x) => x.label)).toEqual(["10:00:00", "10:00:10", "10:00:20", "10:00:30"]);
    expect(sec[0].major).toBe(true);
    expect(sec[1].major).toBe(false);
    const m = new Date(2026, 2, 4, 0, 0, 0).getTime();
    const day = ticks(m - 3600e3, m + 3600e3, 3600e3);
    expect(day.find((x) => x.t === m)).toMatchObject({ label: "03-04", major: true });
  });
});

describe("gap navigation", () => {
  const spans = [
    { start: 0, end: 1000 },
    { start: 1000, end: 2000 }, // touches the first: one stretch
    { start: 5000, end: 6000 },
    { start: 9000, end: 10000 },
  ];
  it("merges touching spans", () => {
    expect(mergeSpans(spans)).toEqual([
      { start: 0, end: 2000 },
      { start: 5000, end: 6000 },
      { start: 9000, end: 10000 },
    ]);
  });
  it("next gap lands on the footage after the gap", () => {
    expect(nextGap(spans, 500)).toBe(5000);
    expect(nextGap(spans, 3000)).toBe(5000);
    expect(nextGap(spans, 5500)).toBe(9000);
    expect(nextGap(spans, 9500)).toBeNull();
  });
  it("prev gap lands at the end of the footage before the gap", () => {
    expect(prevGap(spans, 9500)).toBe(5000);
    expect(prevGap(spans, 5500)).toBe(1000);
    expect(prevGap(spans, 3000)).toBe(1000);
    expect(prevGap(spans, 500)).toBeNull();
  });
});

describe("low-res spans (wave 22)", () => {
  const m = 60e3;
  // Dual mode: low-res 24/7 from 0 to 60 min, full-res events at 10–12 and 30–31 min, then a gap, then low again.
  const spans: Range[] = [
    { start: 0, end: 30 * m, quality: "low" },
    { start: 30 * m, end: 60 * m, quality: "low" },
    { start: 10 * m, end: 12 * m },
    { start: 30 * m, end: 31 * m, quality: "full" },
    { start: 90 * m, end: 100 * m, quality: "low" },
  ];
  it("keeps quality and lets full-res win where both exist", () => {
    expect(mergeSpans(spans)).toEqual([
      { start: 0, end: 10 * m, quality: "low" },
      { start: 10 * m, end: 12 * m },
      { start: 12 * m, end: 30 * m, quality: "low" },
      { start: 30 * m, end: 31 * m },
      { start: 31 * m, end: 60 * m, quality: "low" },
      { start: 90 * m, end: 100 * m, quality: "low" },
    ]);
  });
  it("never merges low into full", () => {
    const touching: Range[] = [
      { start: 0, end: 1000 },
      { start: 1000, end: 2000, quality: "low" },
    ];
    expect(mergeSpans(touching)).toEqual([{ start: 0, end: 1000 }, { start: 1000, end: 2000, quality: "low" }]);
  });
  it("drops low-res that full-res covers entirely", () => {
    expect(mergeSpans([{ start: 0, end: 10e3 }, { start: 2e3, end: 8e3, quality: "low" }])).toEqual([{ start: 0, end: 10e3 }]);
  });
  it("leaves full-only timelines as before (no quality field)", () => {
    expect(mergeSpans([{ start: 0, end: 1000, quality: "full" }])).toEqual([{ start: 0, end: 1000 }]);
    expect(hasLowRes(mergeSpans([{ start: 0, end: 1000 }]))).toBe(false);
    expect(hasLowRes(mergeSpans(spans))).toBe(true);
  });
  it("a switch between low and full is not a gap", () => {
    expect(mergeCoverage(spans)).toEqual([
      { start: 0, end: 60 * m },
      { start: 90 * m, end: 100 * m },
    ]);
    expect(nextGap(spans, 5 * m)).toBe(90 * m);
    expect(prevGap(spans, 95 * m)).toBe(60 * m - 1000);
  });
});

describe("in/out selection", () => {
  it("is empty until both points are set", () => {
    expect(selectionRange(emptySelection)).toBeNull();
    expect(selectionRange(setIn(emptySelection, 10))).toBeNull();
    expect(selectionRange(setOut(setIn(emptySelection, 10), 50))).toEqual({ start: 10, end: 50 });
  });
  it("drops a point the new one crosses", () => {
    const sel = setOut(setIn(emptySelection, 10), 50);
    expect(setIn(sel, 80)).toEqual({ inAt: 80, outAt: null });
    expect(setOut(sel, 5)).toEqual({ inAt: null, outAt: 5 });
    expect(setIn(sel, 20)).toEqual({ inAt: 20, outAt: 50 });
  });
  it("explains why it can't be exported", () => {
    expect(exportBlocker(emptySelection, 1800e3)).toMatch(/Mark/);
    expect(exportBlocker({ inAt: 0, outAt: 3600e3 }, 1800e3)).toMatch(/30 minutes/);
    expect(exportBlocker({ inAt: 0, outAt: 60e3 }, 1800e3)).toBe("");
  });
});
