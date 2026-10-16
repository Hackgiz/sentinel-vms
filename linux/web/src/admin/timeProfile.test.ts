import { describe, expect, it } from "vitest";
import { describeSpans, emptyGrid, gridToSpans, spansToGrid, CELLS_PER_DAY } from "./timeProfile";

describe("time profile grid", () => {
  it("round-trips weekday office hours as one span", () => {
    const weekly = [{ days: [1, 2, 3, 4, 5], start: "08:00", end: "17:30" }];
    const g = spansToGrid(weekly);
    expect(g[1][8 * 4]).toBe(true);
    expect(g[1][8 * 4 - 1]).toBe(false);
    expect(g[5][17 * 4 + 1]).toBe(true); // 17:15
    expect(g[5][17 * 4 + 2]).toBe(false); // 17:30 is the exclusive end
    expect(g[0].some(Boolean)).toBe(false);
    expect(gridToSpans(g)).toEqual(weekly);
  });

  it("spreads a past-midnight span over the next day", () => {
    const g = spansToGrid([{ days: [5], start: "22:00", end: "06:00" }]);
    expect(g[5][22 * 4]).toBe(true);
    expect(g[5][CELLS_PER_DAY - 1]).toBe(true);
    expect(g[5][3 * 4]).toBe(false); // Friday 03:00 is not covered
    expect(g[6][0]).toBe(true);
    expect(g[6][6 * 4 - 1]).toBe(true);
    expect(g[6][6 * 4]).toBe(false);
    // Saved back as two same-day spans with an explicit end of day.
    expect(gridToSpans(g)).toEqual([
      { days: [5], start: "22:00", end: "24:00" },
      { days: [6], start: "00:00", end: "06:00" },
    ]);
  });

  it("wraps Saturday night into Sunday", () => {
    const g = spansToGrid([{ days: [6], start: "23:00", end: "01:00" }]);
    expect(g[0][0]).toBe(true);
    expect(g[0][4]).toBe(false);
  });

  it("keeps several runs per day and groups identical days", () => {
    const g = emptyGrid();
    for (const d of [1, 3]) {
      for (let c = 36; c < 40; c++) g[d][c] = true; // 09:00-10:00
      for (let c = 52; c < 56; c++) g[d][c] = true; // 13:00-14:00
    }
    expect(gridToSpans(g)).toEqual([
      { days: [1, 3], start: "09:00", end: "10:00" },
      { days: [1, 3], start: "13:00", end: "14:00" },
    ]);
    expect(gridToSpans(emptyGrid())).toEqual([]);
  });

  it("rounds an unaligned minute outward so nothing painted is lost", () => {
    const g = spansToGrid([{ days: [2], start: "08:05", end: "08:20" }]);
    expect(gridToSpans(g)).toEqual([{ days: [2], start: "08:00", end: "08:30" }]);
  });

  it("describes spans for the list", () => {
    expect(describeSpans([])).toBe("Never active");
    expect(describeSpans([{ days: [1, 2, 3, 4, 5], start: "08:00", end: "17:00" }])).toBe("Mon–Fri 08:00–17:00");
    expect(describeSpans([{ days: [0, 6], start: "22:00", end: "06:00" }])).toBe("Sun, Sat 22:00–06:00 (overnight)");
  });
});
