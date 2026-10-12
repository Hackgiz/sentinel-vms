import { describe, expect, it } from "vitest";
import { locate, recordersOf, skipGap, spansOn, steer, SyncClock, wallAt, type PlaybackSpan } from "./sync";

const s = 1000;
// A camera that moved from rs1 to rs2 at 100 s, with a gap 60–80 s on rs1.
const moved: PlaybackSpan[] = [
  { start: 0, end: 60 * s, media: 0, recorderId: "rs1" },
  { start: 80 * s, end: 100 * s, media: 60, recorderId: "rs1" },
  { start: 100 * s, end: 160 * s, media: 0, recorderId: "rs2" },
];

describe("footage across recording servers", () => {
  it("finds the server and media time for a wall time", () => {
    expect(locate(moved, 30 * s)).toEqual({ recorderId: "rs1", media: 30 });
    expect(locate(moved, 90 * s)).toEqual({ recorderId: "rs1", media: 70 });
    expect(locate(moved, 130 * s)).toEqual({ recorderId: "rs2", media: 30 });
    expect(locate(moved, 70 * s)).toBeNull();
    expect(recordersOf(moved)).toEqual(["rs1", "rs2"]);
  });

  it("maps a player's media time back to wall time on its own server", () => {
    expect(wallAt(moved, "rs1", 65)).toBe(85 * s);
    expect(wallAt(moved, "rs2", 10)).toBe(110 * s);
    expect(wallAt(moved, "rs2", 999)).toBe(160 * s); // clamped at the end
    expect(wallAt(moved, "rs3", 1)).toBeNull();
  });

  it("lays spans end to end when the server sends no media offsets, using the camera's server", () => {
    const bare: PlaybackSpan[] = [
      { start: 10 * s, end: 20 * s },
      { start: 50 * s, end: 70 * s },
    ];
    expect(spansOn(bare, "rsX", "rsX").map((x) => x.media)).toEqual([0, 10]);
    expect(locate(bare, 55 * s, "rsX")).toEqual({ recorderId: "rsX", media: 15 });
  });

  it("skips time no camera recorded", () => {
    const other: PlaybackSpan[] = [{ start: 65 * s, end: 70 * s, recorderId: "rs9" }];
    expect(skipGap([moved], 70 * s)).toBe(80 * s);
    expect(skipGap([moved, other], 66 * s)).toBe(66 * s);
    expect(skipGap([moved, other], 61 * s)).toBe(65 * s);
    expect(skipGap([moved], 200 * s)).toBeNull();
  });
});

describe("SyncClock", () => {
  it("runs at its rate, pauses, and follows a close player", () => {
    let now = 0;
    const c = new SyncClock(() => now);
    expect(c.position).toBeNull();
    c.play(); // nothing to play before a seek
    expect(c.playing).toBe(false);
    c.seek(10 * s);
    c.play();
    now = 2000;
    expect(c.position).toBe(12 * s);
    c.setRate(4);
    now = 3000;
    expect(c.position).toBe(16 * s);
    c.pause();
    now = 9000;
    expect(c.position).toBe(16 * s);
    expect(c.follow(16.5 * s)).toBe(true);
    expect(c.position).toBe(16.5 * s);
    expect(c.follow(30 * s)).toBe(false); // still seeking: ignored
    expect(c.follow(null)).toBe(false);
  });

  it("settles past gaps and stops after the last recording", () => {
    let now = 0;
    const c = new SyncClock(() => now);
    c.seek(59 * s);
    c.play();
    now = 2000; // 61 s: in the gap
    expect(c.settle([moved])).toBe(80 * s);
    expect(c.playing).toBe(true);
    c.seek(159 * s);
    now = 5000; // 162 s: past the end
    expect(c.settle([moved])).toBeNull();
    expect(c.playing).toBe(false);
  });
});

describe("steer", () => {
  const player = (loaded: string | null, current: number, paused: boolean) => ({ loaded, current, paused });

  it("loads the other server's playlist when the playhead crosses a move", () => {
    expect(steer(moved, 130 * s, player("rs1", 70, false), true)).toEqual({ kind: "load", recorderId: "rs2", media: 30 });
    expect(steer(moved, 130 * s, player(null, 0, true), false)).toEqual({ kind: "load", recorderId: "rs2", media: 30 });
  });

  it("seeks drifted players, starts paused ones, pauses in gaps", () => {
    expect(steer(moved, 30 * s, player("rs1", 30.5, false), true)).toEqual({ kind: "ok" });
    expect(steer(moved, 30 * s, player("rs1", 33, false), true)).toEqual({ kind: "seek", media: 30 });
    expect(steer(moved, 30 * s, player("rs1", 30, true), true)).toEqual({ kind: "play", media: 30 });
    expect(steer(moved, 30 * s, player("rs1", 10, true), false)).toEqual({ kind: "seek", media: 30 });
    expect(steer(moved, 30 * s, player("rs1", 30.1, true), false)).toEqual({ kind: "ok" });
    expect(steer(moved, 70 * s, player("rs1", 30, false), true)).toEqual({ kind: "gap" });
  });
});
