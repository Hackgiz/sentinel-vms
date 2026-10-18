import { describe, expect, it } from "vitest";
import type { Bookmark } from "../shared/api";
import {
  bookmarkColorNames,
  bookmarkColors,
  bookmarkDuration,
  bookmarkLabel,
  bookmarkSpan,
  colorOf,
  inWindow,
  isRangeBookmark,
  newBookmarkTime,
  removeBookmark,
  sortBookmarks,
  upsertBookmark,
} from "./bookmarks";
import { emptySelection, setIn, setOut } from "./timelineMath";

const bm = (id: string, atMs: number, durationMs = 0, extra: Partial<Bookmark> = {}): Bookmark => ({
  id,
  cameraId: "c1",
  atMs,
  durationMs,
  title: id,
  note: "",
  color: "blue",
  createdBy: "u",
  createdAt: 1,
  canEdit: true,
  ...extra,
});

describe("bookmark model", () => {
  it("matches the server palette", () => {
    expect(bookmarkColorNames).toEqual(["red", "orange", "yellow", "green", "teal", "blue", "purple", "pink", "gray"]);
    expect(colorOf({ color: "red" })).toBe(bookmarkColors.red);
    expect(colorOf({ color: "nonsense" })).toBe(bookmarkColors.blue);
  });
  it("tells points from ranges and labels untitled ones", () => {
    expect(isRangeBookmark(bm("a", 1))).toBe(false);
    expect(isRangeBookmark(bm("a", 1, 5))).toBe(true);
    expect(bookmarkSpan(bm("a", 10, 5))).toEqual({ start: 10, end: 15 });
    expect(bookmarkLabel({ title: "  " })).toBe("Bookmark");
    expect(bookmarkLabel({ title: "Door" })).toBe("Door");
  });
  it("finds bookmarks in a window, ranges by overlap", () => {
    const list = [bm("p", 100), bm("r", 200, 100), bm("late", 500)];
    expect(inWindow(list, 90, 110).map((b) => b.id)).toEqual(["p"]);
    expect(inWindow(list, 290, 400).map((b) => b.id)).toEqual(["r"]);
    expect(inWindow(list, 301, 400)).toEqual([]);
  });
  it("keeps the list sorted through upsert and remove", () => {
    let list = sortBookmarks([bm("b", 200), bm("a", 100)]);
    expect(list.map((b) => b.id)).toEqual(["a", "b"]);
    list = upsertBookmark(list, bm("c", 150));
    expect(list.map((b) => b.id)).toEqual(["a", "c", "b"]);
    list = upsertBookmark(list, bm("a", 300, 0, { title: "moved" }));
    expect(list.map((b) => b.id)).toEqual(["c", "b", "a"]);
    expect(removeBookmark(list, "b").map((b) => b.id)).toEqual(["c", "a"]);
  });
  it("sizes a new bookmark from the playhead or the marked range", () => {
    expect(newBookmarkTime(1234.4, emptySelection, true)).toEqual({ atMs: 1234, durationMs: 0 });
    const sel = setOut(setIn(emptySelection, 1000), 6000);
    expect(newBookmarkTime(9, sel, true)).toEqual({ atMs: 1000, durationMs: 5000 });
    expect(newBookmarkTime(9, sel, false)).toEqual({ atMs: 9, durationMs: 0 });
  });
  it("words durations", () => {
    expect(bookmarkDuration(0)).toBe("");
    expect(bookmarkDuration(30_000)).toBe("30 s");
    expect(bookmarkDuration(80_000)).toBe("1 min 20 s");
    expect(bookmarkDuration(3_600_000)).toBe("1 h");
  });
});
