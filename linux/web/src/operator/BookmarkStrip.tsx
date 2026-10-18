// The Playback page's bookmark strip (WAVE7-FEATURES.md, Stream B): an add
// button / inline title field (the B key opens it), and a collapsible list of
// the shown day's bookmarks for the selected camera with seek, edit, delete
// and "export this" on ranges. State lives in Playback (the bookmarks come
// with the day's timeline read); this component only talks to the API.
import { useState } from "react";
import { errorMessage, type ApiClient, type Bookmark, type BookmarkColor } from "../shared/api";
import { fmtClock } from "../shared/time";
import { bookmarkColorNames, bookmarkDuration, bookmarkLabel, colorOf, defaultBookmarkColor, isRangeBookmark, newBookmarkTime } from "./bookmarks";
import type { Selection } from "./timelineMath";
import { selectionRange } from "./timelineMath";

export interface BookmarkStripProps {
  client: ApiClient;
  cameraId: string | null;
  cameraName: string;
  /** The shown day's bookmarks of that camera, sorted. */
  bookmarks: Bookmark[];
  /** The playhead when the add field was opened (null: closed). */
  adding: number | null;
  onAdding: (at: number | null) => void;
  /** The playhead now (the add button opens the field here; B does the same). */
  playhead: number | null;
  selection: Selection;
  onSeek: (t: number) => void;
  onUpsert: (b: Bookmark) => void;
  onRemove: (id: string) => void;
  /** Pre-fill the export dialog with a range bookmark; undefined without export permission. */
  onExport?: (b: Bookmark) => void;
  onError: (message: string) => void;
}

export default function BookmarkStrip(p: BookmarkStripProps) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [useRange, setUseRange] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<{ id: string; title: string; note: string; color: BookmarkColor } | null>(null);
  const hasRange = selectionRange(p.selection) !== null;

  async function add() {
    if (p.adding === null || !p.cameraId || busy) return;
    setBusy(true);
    try {
      const t = newBookmarkTime(p.adding, p.selection, useRange);
      p.onUpsert(await p.client.bookmarks.create(p.cameraId, { ...t, title: title.trim(), color: defaultBookmarkColor }));
      setTitle("");
      setUseRange(false);
      p.onAdding(null);
      setOpen(true);
    } catch (e) {
      p.onError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    if (!editing || busy) return;
    setBusy(true);
    try {
      p.onUpsert(await p.client.bookmarks.update(editing.id, { title: editing.title.trim(), note: editing.note.trim(), color: editing.color }));
      setEditing(null);
    } catch (e) {
      p.onError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function remove(b: Bookmark) {
    if (!window.confirm(`Delete the bookmark "${bookmarkLabel(b)}"?`)) return;
    try {
      await p.client.bookmarks.remove(b.id);
      p.onRemove(b.id);
    } catch (e) {
      p.onError(errorMessage(e));
    }
  }

  return (
    <div className="op-bm" role="region" aria-label="Bookmarks">
      <div className="op-bm-head">
        <button className="btn small" onClick={() => setOpen((o) => !o)} aria-expanded={open} title="Bookmarks on the selected camera for this day">
          {open ? "▾" : "▸"} Bookmarks ({p.bookmarks.length})
        </button>
        {p.adding === null ? (
          <button className="btn small" disabled={!p.cameraId || p.playhead === null} onClick={() => p.onAdding(p.playhead)} title="Bookmark the playhead (B)">
            + Bookmark
          </button>
        ) : (
          <form
            className="op-bm-add"
            onSubmit={(e) => {
              e.preventDefault();
              void add();
            }}
          >
            <span className="muted small mono">{fmtClock(p.adding)}</span>
            <input
              type="text"
              autoFocus
              value={title}
              maxLength={120}
              placeholder={`Title (${p.cameraName}), Enter to save`}
              aria-label="Bookmark title"
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setTitle("");
                  p.onAdding(null);
                }
              }}
            />
            {hasRange && (
              <label className="check small" title="Make the bookmark cover the marked in/out range">
                <input type="checkbox" checked={useRange} onChange={(e) => setUseRange(e.target.checked)} /> Marked range
              </label>
            )}
            <button className="btn small primary" type="submit" disabled={busy}>
              Save
            </button>
            <button className="btn small" type="button" onClick={() => p.onAdding(null)}>
              Cancel
            </button>
          </form>
        )}
      </div>
      {open && (
        <div className="op-bm-list">
          {p.bookmarks.length === 0 && <div className="muted small op-bm-row">No bookmarks on this day. Press B to pin the playhead.</div>}
          {p.bookmarks.map((b) =>
            editing?.id === b.id ? (
              <form
                key={b.id}
                className="op-bm-edit"
                onSubmit={(e) => {
                  e.preventDefault();
                  void save();
                }}
              >
                <input type="text" autoFocus value={editing.title} maxLength={120} aria-label="Title" placeholder="Title" onChange={(e) => setEditing({ ...editing, title: e.target.value })} onKeyDown={(e) => e.key === "Escape" && setEditing(null)} />
                <input type="text" value={editing.note} maxLength={2000} aria-label="Note" placeholder="Note" onChange={(e) => setEditing({ ...editing, note: e.target.value })} onKeyDown={(e) => e.key === "Escape" && setEditing(null)} />
                <select className="select-inline" aria-label="Color" value={editing.color} onChange={(e) => setEditing({ ...editing, color: e.target.value as BookmarkColor })}>
                  {bookmarkColorNames.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
                <button className="btn small primary" type="submit" disabled={busy}>
                  Save
                </button>
                <button className="btn small" type="button" onClick={() => setEditing(null)}>
                  Cancel
                </button>
              </form>
            ) : (
              <div key={b.id} className="op-bm-row">
                <span className="op-bm-dot" style={{ background: colorOf(b) }} aria-hidden="true" />
                <span className="mono small">{fmtClock(b.atMs)}</span>
                <button className="op-bm-title" onClick={() => p.onSeek(b.atMs)} title="Seek to this bookmark">
                  {bookmarkLabel(b)}
                  {isRangeBookmark(b) && <span className="muted small"> ({bookmarkDuration(b.durationMs)})</span>}
                </button>
                {b.note && <span className="muted small op-bm-note" title={b.note}>{b.note}</span>}
                {isRangeBookmark(b) && p.onExport && (
                  <button className="btn small" onClick={() => p.onExport!(b)} title="Open the export dialog with this range">
                    Export this
                  </button>
                )}
                {b.canEdit && (
                  <>
                    <button className="btn small" onClick={() => setEditing({ id: b.id, title: b.title, note: b.note, color: (b.color as BookmarkColor) || defaultBookmarkColor })}>
                      Edit
                    </button>
                    <button className="btn small" onClick={() => void remove(b)} aria-label={`Delete ${bookmarkLabel(b)}`}>
                      Delete
                    </button>
                  </>
                )}
              </div>
            ),
          )}
        </div>
      )}
    </div>
  );
}
