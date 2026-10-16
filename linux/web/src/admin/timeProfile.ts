// Time profile <-> weekly grid conversion (WAVE6-ADMIN.md, Stream C).
// The server stores spans {days, start:"HH:MM", end:"HH:MM"}; the editor
// paints a 7 x 96 grid of 15-minute cells (row 0 = Sunday).

export const CELL_MINUTES = 15;
export const CELLS_PER_DAY = (24 * 60) / CELL_MINUTES;

export type Span = { days: number[]; start: string; end: string };
export type Grid = boolean[][];

export const emptyGrid = (): Grid => Array.from({ length: 7 }, () => Array<boolean>(CELLS_PER_DAY).fill(false));

export function clock(minutes: number): string {
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}

export function minutesOf(hhmm: string): number {
  const [h, m] = hhmm.split(":").map(Number);
  return h * 60 + m;
}

/** Paint a profile's spans onto the grid; an end before the start runs past midnight into the next day. */
export function spansToGrid(weekly: Span[]): Grid {
  const g = emptyGrid();
  const paint = (day: number, from: number, to: number) => {
    for (let c = Math.floor(from / CELL_MINUTES); c < Math.ceil(to / CELL_MINUTES) && c < CELLS_PER_DAY; c++) g[day][c] = true;
  };
  for (const sp of weekly) {
    const from = minutesOf(sp.start);
    const to = minutesOf(sp.end);
    for (const d of sp.days) {
      if (d < 0 || d > 6) continue;
      if (from < to) paint(d, from, to);
      else if (from > to) {
        paint(d, from, 24 * 60);
        paint((d + 1) % 7, 0, to);
      }
    }
  }
  return g;
}

/** Runs of painted cells per day, merged across days that share the same runs. */
export function gridToSpans(g: Grid): Span[] {
  const runs = g.map((row) => {
    const out: [number, number][] = [];
    let start = -1;
    for (let c = 0; c <= CELLS_PER_DAY; c++) {
      const on = c < CELLS_PER_DAY && row[c];
      if (on && start < 0) start = c;
      if (!on && start >= 0) {
        out.push([start * CELL_MINUTES, c * CELL_MINUTES]);
        start = -1;
      }
    }
    return out;
  });
  // Days with an identical set of runs share spans: group by the day's full signature.
  const sig = runs.map((rs) => rs.map(([a, b]) => `${a}-${b}`).join(","));
  const out: Span[] = [];
  const done = new Set<number>();
  sig.forEach((s, day) => {
    if (!s || done.has(day)) return;
    const days = sig.flatMap((x, d) => (x === s ? [d] : []));
    days.forEach((d) => done.add(d));
    for (const [a, b] of runs[day]) out.push({ days, start: clock(a), end: clock(b) });
  });
  return out;
}

/** "Mon–Fri 08:00–17:30" style one-liner for the list. */
export function describeSpans(weekly: Span[]): string {
  if (weekly.length === 0) return "Never active";
  const names = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
  const days = (d: number[]) => {
    const s = [...d].sort((a, b) => a - b);
    if (s.length === 7) return "Every day";
    if (s.length > 2 && s.every((x, i) => i === 0 || x === s[i - 1] + 1)) return `${names[s[0]]}–${names[s[s.length - 1]]}`;
    return s.map((x) => names[x]).join(", ");
  };
  return weekly.map((sp) => `${days(sp.days)} ${sp.start}–${sp.end}${minutesOf(sp.end) < minutesOf(sp.start) ? " (overnight)" : ""}`).join("; ");
}
