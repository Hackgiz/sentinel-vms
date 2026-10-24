import { describe, expect, it } from "vitest";
import type { Marker } from "../shared/time";
import { emptySelection, mergeSpans, setIn, setOut } from "../operator/timelineMath";
import { nextSpanStart, wallToMedia } from "../playback";
import {
  DAY,
  FUTURE_MARGIN,
  HOUR,
  clusterMarkers,
  comfortablyInView,
  dataWindow,
  fmtBehind,
  fracToTime,
  inWindow,
  isFollowingNow,
  isLiveEdge,
  needsNextWindow,
  playWindowAround,
  liveView,
  panView,
  parseViewerRoute,
  stepTime,
  timeToFrac,
  viewAround,
  viewerHash,
  windowAround,
  windowCovers,
  zoomView,
  exportProblem,
  lanesFor,
  selectedCamera,
  skipTarget,
} from "./viewerMath";

const now = 1_800_000_000_000;

describe("live view", () => {
  it("ends just after now and follows it", () => {
    const v = liveView(now, HOUR);
    expect(v.end - v.start).toBe(HOUR);
    expect(v.end).toBeCloseTo(now + HOUR * FUTURE_MARGIN);
    expect(isFollowingNow(v, now)).toBe(true);
    expect(isFollowingNow(panView(v, -HOUR / 2, now), now)).toBe(false);
  });
});

describe("playhead positioning", () => {
  const v = { start: 1000, end: 3000 };
  it("maps time to a fraction and back", () => {
    expect(timeToFrac(2000, v)).toBe(0.5);
    expect(timeToFrac(1000, v)).toBe(0);
    expect(timeToFrac(4000, v)).toBe(1.5);
    expect(fracToTime(0.25, v)).toBe(1500);
    expect(fracToTime(timeToFrac(2750, v), v)).toBe(2750);
  });
  it("keeps the playhead comfortably in view", () => {
    expect(comfortablyInView(2000, v)).toBe(true);
    expect(comfortablyInView(1050, v)).toBe(false);
    expect(comfortablyInView(2950, v)).toBe(false);
  });
  it("viewAround leaves room ahead of the playhead and never reaches past live", () => {
    const t = now - 3 * HOUR;
    const a = viewAround(t, HOUR, now);
    expect(timeToFrac(t, a)).toBeCloseTo(0.8);
    const b = viewAround(now - 60e3, HOUR, now);
    expect(b.end).toBeCloseTo(now + HOUR * FUTURE_MARGIN);
    expect(b.end - b.start).toBe(HOUR);
  });
});

describe("pan and zoom", () => {
  it("pans back freely but not into the future", () => {
    const v = liveView(now, HOUR);
    const back = panView(v, -2 * HOUR, now);
    expect(back.end).toBe(v.end - 2 * HOUR);
    expect(back.end - back.start).toBe(HOUR);
    expect(panView(back, 10 * HOUR, now)).toEqual(v);
  });
  it("zooms around the anchor", () => {
    const v = { start: now - 6 * HOUR, end: now - 2 * HOUR };
    const anchor = now - 5 * HOUR;
    const z = zoomView(v, anchor, 0.5, now);
    expect(z.end - z.start).toBe(2 * HOUR);
    expect(timeToFrac(anchor, z)).toBeCloseTo(timeToFrac(anchor, v));
  });
  it("zoom is limited to 1 minute .. 24 hours", () => {
    const v = liveView(now, HOUR);
    const wide = zoomView(v, now, 1000, now);
    expect(wide.end - wide.start).toBe(DAY);
    const tiny = zoomView(v, now, 1e-6, now);
    expect(tiny.end - tiny.start).toBe(60e3);
  });
});

describe("data window", () => {
  it("is 24 h and ends at now when the view is live", () => {
    const v = liveView(now, 6 * HOUR);
    const w = dataWindow(v, now);
    expect(w.to).toBe(now);
    expect(w.to - w.from).toBe(DAY);
    expect(windowCovers(w, v, now)).toBe(true);
  });
  it("covers a full 24 h live view (the future margin has no footage)", () => {
    const v = liveView(now, DAY);
    expect(windowCovers(dataWindow(v, now), v, now)).toBe(true);
  });
  it("is centered on a view in the past and covers it", () => {
    const v = { start: now - 5 * DAY, end: now - 5 * DAY + 6 * HOUR };
    const w = dataWindow(v, now);
    expect(windowCovers(w, v, now)).toBe(true);
    expect(w.to - w.from).toBe(DAY);
    expect(inWindow(w, v.start)).toBe(true);
  });
  it("stops covering when the view leaves it", () => {
    const w = windowAround(now - 2 * DAY, now);
    expect(windowCovers(w, { start: now - 3.5 * DAY, end: now - 3.4 * DAY }, now)).toBe(false);
  });
  it("a window around a recent time ends at now", () => {
    expect(windowAround(now - HOUR, now).to).toBe(now);
  });
});

describe("stepping and live edge", () => {
  it("steps by 10 s and never past now", () => {
    expect(stepTime(now - 60e3, 10e3, now)).toBe(now - 50e3);
    expect(stepTime(now - 5e3, 10e3, now)).toBe(now);
    expect(stepTime(now - 60e3, -10e3, now)).toBe(now - 70e3);
  });
  it("snaps to live only at the edge", () => {
    expect(isLiveEdge(now - 2000, now)).toBe(true);
    expect(isLiveEdge(now - 30e3, now)).toBe(false);
  });
});

describe("range merging and gap skipping", () => {
  const spans = [
    { start: 0, end: 1000, media: 0 },
    { start: 1000, end: 2000, media: 1 },
    { start: 5000, end: 6000, media: 2 },
  ];
  it("merges touching recordings into stretches", () => {
    expect(mergeSpans(spans)).toEqual([
      { start: 0, end: 2000 },
      { start: 5000, end: 6000 },
    ]);
  });
  it("finds media time and the next recording after a gap", () => {
    expect(wallToMedia(spans, 1500)).toBe(1.5);
    expect(wallToMedia(spans, 3000)).toBeNull();
    expect(nextSpanStart(spans, 3000)).toBe(5000);
  });
});

describe("marker clustering", () => {
  const mk = (at: number, kind = "Motion", alarm = false, severity?: string): Marker => ({ at, cameraID: "c", kind, label: kind, alarm, severity });
  const v = { start: 0, end: 1000 };
  it("merges close markers and counts them", () => {
    const c = clusterMarkers([mk(100), mk(101), mk(102), mk(500)], v, 1000, 5);
    expect(c.map((x) => x.count)).toEqual([3, 1]);
  });
  it("lets alarms and detections outrank motion", () => {
    const c = clusterMarkers([mk(100), mk(101, "Person"), mk(102, "alarm", true, "Critical")], v, 1000, 5);
    expect(c).toHaveLength(1);
    expect(c[0].critical).toBe(true);
    expect(c[0].alarm).toBe(true);
  });
  it("drops markers outside the view", () => {
    expect(clusterMarkers([mk(-5), mk(2000)], v, 1000)).toHaveLength(0);
  });
});

describe("deep links", () => {
  it("parses camera and time", () => {
    expect(parseViewerRoute("#/live")).toEqual({ page: "live", camera: null, t: null });
    expect(parseViewerRoute("#/live/cam-1")).toEqual({ page: "live", camera: "cam-1", t: null });
    expect(parseViewerRoute("#/playback/cam%201/1800000000000")).toEqual({ page: "playback", camera: "cam 1", t: 1800000000000 });
    expect(parseViewerRoute("")).toEqual({ page: "", camera: null, t: null });
  });
  it("round-trips through viewerHash", () => {
    expect(parseViewerRoute(viewerHash("playback", "a b", 1800000000000))).toEqual({ page: "playback", camera: "a b", t: 1800000000000 });
    expect(viewerHash("live", null)).toBe("#/live");
  });
});

describe("recording lanes", () => {
  const tl = {
    a: { spans: [{ start: 0, end: 1000 }, { start: 1000, end: 2000 }] },
    b: { spans: [{ start: 1500, end: 3000 }] },
    c: { spans: [{ start: 5000, end: 6000 }] },
  };
  it("puts the selected camera on the main lane and merges the rest", () => {
    const l = lanesFor("a", ["a", "b", "c"], tl);
    expect(l.selected).toEqual([{ start: 0, end: 2000 }]);
    expect(l.any).toEqual([
      { start: 1500, end: 3000 },
      { start: 5000, end: 6000 },
    ]);
  });
  it("has an empty main lane with no selection", () => {
    expect(lanesFor(null, ["a"], tl).selected).toEqual([]);
  });
  it("selects the expanded camera, else the clicked tile, else the first", () => {
    expect(selectedCamera("b", "a", ["a", "b"])).toBe("b");
    expect(selectedCamera(null, "b", ["a", "b"])).toBe("b");
    expect(selectedCamera(null, "gone", ["a", "b"])).toBe("a");
    expect(selectedCamera(null, null, [])).toBeNull();
  });
});

describe("gap and event skip", () => {
  const spans = [
    { start: 0, end: 1000 },
    { start: 5000, end: 6000 },
    { start: 9000, end: 10000 },
  ];
  it("skips the next gap when no event comes first", () => {
    expect(skipTarget(spans, [], 500, 1)).toEqual({ kind: "gap", at: 5000 });
  });
  it("prefers an event that comes before the next gap", () => {
    expect(skipTarget(spans, [{ at: 800 }], 100, 1)).toEqual({ kind: "event", at: 800 });
    expect(skipTarget(spans, [{ at: 7000 }], 100, 1)).toEqual({ kind: "gap", at: 5000 });
  });
  it("goes backward the same way", () => {
    expect(skipTarget(spans, [], 9500, -1)).toEqual({ kind: "gap", at: 5000 });
    expect(skipTarget(spans, [{ at: 5400 }], 9500, -1)).toEqual({ kind: "event", at: 5400 });
  });
  it("is null with nothing ahead", () => {
    expect(skipTarget(spans, [], 9500, 1)).toBeNull();
  });
});

describe("export range", () => {
  const spans = [{ start: 1000, end: 5000 }];
  it("needs both points, in order", () => {
    expect(exportProblem(emptySelection, spans, 1e6)).not.toBe("");
    expect(exportProblem(setOut(setIn(emptySelection, 2000), 3000), spans, 1e6)).toBe("");
  });
  it("drops an out point at or before the new in point", () => {
    expect(setIn({ inAt: 1000, outAt: 2000 }, 3000)).toEqual({ inAt: 3000, outAt: null });
  });
  it("rejects over-long and unrecorded ranges", () => {
    expect(exportProblem(setOut(setIn(emptySelection, 2000), 3000), spans, 500)).not.toBe("");
    expect(exportProblem(setOut(setIn(emptySelection, 8000), 9000), spans, 1e6)).toMatch(/Nothing was recorded/);
  });
});

describe("labels", () => {
  it("formats how far behind live", () => {
    expect(fmtBehind(now - 45e3, now)).toBe("-45s");
    expect(fmtBehind(now - 125e3, now)).toBe("-2m 05s");
    expect(fmtBehind(now - 2 * HOUR - 5 * 60e3, now)).toBe("-2h 05m");
  });
});

describe("play window (WAVE24 D2)", () => {
  const now = Date.parse("2026-10-23T12:00:00Z");
  it("is the hour either side of the playhead, clamped to now", () => {
    const mid = now - 5 * HOUR;
    expect(playWindowAround(mid, now)).toEqual({ from: mid - HOUR, to: mid + HOUR });
    expect(playWindowAround(now - 10 * 60e3, now)).toEqual({ from: now - 10 * 60e3 - HOUR, to: now });
  });

  it("asks for the next window near an end or outside", () => {
    const t = now - 5 * HOUR;
    const w = playWindowAround(t, now);
    expect(needsNextWindow(w, t, now)).toBe(false);
    expect(needsNextWindow(w, w.to - 11 * 60e3, now)).toBe(false);
    expect(needsNextWindow(w, w.to - 9 * 60e3, now)).toBe(true);
    expect(needsNextWindow(w, w.from + 9 * 60e3, now)).toBe(true);
    expect(needsNextWindow(w, w.from + 11 * 60e3, now)).toBe(false);
    expect(needsNextWindow(w, w.to + 1, now)).toBe(true);
    expect(needsNextWindow(w, w.from - 1, now)).toBe(true);
  });

  it("does not chase a window that already reaches the present", () => {
    const w = playWindowAround(now - 20 * 60e3, now);
    expect(w.to).toBe(now);
    expect(needsNextWindow(w, now - 5 * 60e3, now)).toBe(false);
    expect(needsNextWindow(w, now - 4 * 60e3, now + 60e3)).toBe(false);
    expect(needsNextWindow(w, now - 5 * 60e3, now + 11 * 60e3)).toBe(true);
  });
});
