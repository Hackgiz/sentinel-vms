// Timeline bookmarks (WAVE7-FEATURES.md, Stream B): the pure helpers behind
// the Playback page's bookmark glyphs and list. No React here.
import type { Bookmark, BookmarkColor } from "../shared/api";
import type { Range, Selection } from "./timelineMath";
import { selectionRange } from "./timelineMath";

/** The server's palette tokens (api/openapi.yaml BookmarkColor) and how they draw. */
export const bookmarkColors: Record<BookmarkColor, string> = {
  red: "#ef4444",
  orange: "#f97316",
  yellow: "#eab308",
  green: "#22c55e",
  teal: "#14b8a6",
  blue: "#3b82f6",
  purple: "#a855f7",
  pink: "#ec4899",
  gray: "#9ca3af",
};
export const bookmarkColorNames = Object.keys(bookmarkColors) as BookmarkColor[];
export const defaultBookmarkColor: BookmarkColor = "blue";

export const colorOf = (b: { color: string }): string => bookmarkColors[b.color as BookmarkColor] ?? bookmarkColors[defaultBookmarkColor];

export const isRangeBookmark = (b: Pick<Bookmark, "durationMs">): boolean => b.durationMs > 0;

export const bookmarkLabel = (b: Pick<Bookmark, "title">): string => b.title.trim() || "Bookmark";

/** The time a bookmark covers; a point is a zero-length range. */
export const bookmarkSpan = (b: Pick<Bookmark, "atMs" | "durationMs">): Range => ({ start: b.atMs, end: b.atMs + b.durationMs });

/** Bookmarks touching [start, end]. */
export function inWindow(list: Bookmark[], start: number, end: number): Bookmark[] {
  return list.filter((b) => b.atMs <= end && b.atMs + b.durationMs >= start);
}

export const sortBookmarks = (list: Bookmark[]): Bookmark[] => [...list].sort((a, b) => a.atMs - b.atMs || a.createdAt - b.createdAt || a.id.localeCompare(b.id));

export const upsertBookmark = (list: Bookmark[], b: Bookmark): Bookmark[] => sortBookmarks([...list.filter((x) => x.id !== b.id), b]);

export const removeBookmark = (list: Bookmark[], id: string): Bookmark[] => list.filter((x) => x.id !== id);

/** What a new bookmark covers: the marked in/out range when asked and valid, else the playhead. */
export function newBookmarkTime(at: number, selection: Selection, useSelection: boolean): { atMs: number; durationMs: number } {
  const r = useSelection ? selectionRange(selection) : null;
  if (r && r.end > r.start) return { atMs: Math.round(r.start), durationMs: Math.round(r.end - r.start) };
  return { atMs: Math.round(at), durationMs: 0 };
}

/** "1 min 20 s" for a range's length; "" for a point. */
export function bookmarkDuration(ms: number): string {
  if (ms <= 0) return "";
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return [h && `${h} h`, m && `${m} min`, (sec || (!h && !m)) && `${sec} s`].filter(Boolean).join(" ");
}
