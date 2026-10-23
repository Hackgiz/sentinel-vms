// The playback timeline bar: a ruler centered on the playhead that pans by
// drag (scrubbing every tile) and zooms with the wheel or a pinch from one
// minute to 24 hours, the selected camera's recordings and event/alarm
// markers, a thin "any recording" lane for the other tiles, transport
// controls with a speed menu and Live, and the in/out export selection.
import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import type { Bookmark } from "../shared/api";
import type { Marker } from "../shared/time";
import { fmtClock } from "../shared/time";
import { markerTone } from "../shared/alarmKind";
import "../shared/timelineBar.css";
import { bookmarkDuration, bookmarkLabel, bookmarkSpan, colorOf, inWindow, isRangeBookmark } from "./bookmarks";
import {
  hasLowRes,
  panCenter,
  selectionRange,
  speeds,
  tickStep,
  ticks,
  timeToX,
  viewRange,
  wheelFactor,
  xToTime,
  zoomSpan,
  type Range,
  type Selection,
} from "./timelineMath";

export interface BarLane {
  name: string;
  spans: Range[];
  pending: Range[];
  markers: Marker[];
  /** Footage recovered from the camera's SD card (edgeRanges): drawn with a hatch over the band. */
  edge?: Range[];
}

export interface TimelineBarProps {
  /** Anything with a playhead position (the operator's SyncClock, or the dashboard engine). */
  clock: { position: number | null };
  /** The playhead as last steered (also drives re-render when paused). */
  wall: number | null;
  playing: boolean;
  rate: number;
  span: number;
  onSpan: (ms: number) => void;
  lane: BarLane | null;
  anyLane: Range[];
  /** The selected camera's bookmarks; the bar draws the ones in view, and clicking a glyph seeks. */
  bookmarks?: Bookmark[];
  selection: Selection;
  onMarkIn: () => void;
  onMarkOut: () => void;
  onClearSelection: () => void;
  onSeek: (t: number) => void;
  onPlayPause: () => void;
  onStep: (ms: number) => void;
  onGap: (dir: -1 | 1) => void;
  onRate: (r: number) => void;
  onLive: () => void;
  /** Watching live: the playhead is pinned to now and the bar reads LIVE. */
  live?: boolean;
  /** What the gap-skip pair jumps to now: "gap" or "event" (default gap). */
  skipLabels?: { prev: string; next: string };
  /** Sound of the selected tile. */
  sound: boolean;
  onSound: (on: boolean) => void;
  /** Export: undefined hides the controls (no permission). */
  exporter?: { blocker: string; progress: number | null; message: string; onExport: () => void; onCancel: () => void; onLock?: () => void };
  /** "Recover from camera" (ONVIF Profile G): undefined hides it (camera can't, or no export permission). */
  recover?: { title: string; disabled: boolean; running: boolean; message: string; onRecover: () => void; onCancel: () => void };
}

const pad = (n: number) => String(n).padStart(2, "0");
const localInput = (t: number) => {
  const d = new Date(t);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
};
const dateLabel = (t: number) => new Date(t).toLocaleDateString(undefined, { weekday: "short", year: "numeric", month: "short", day: "numeric" });

function usePlayhead(clock: { position: number | null }, wall: number | null, playing: boolean): number | null {
  const [t, setT] = useState<number | null>(wall);
  useEffect(() => {
    setT(clock.position ?? wall);
    if (!playing) return;
    let raf = 0;
    let last = 0;
    const tick = (ts: number) => {
      if (ts - last > 60) {
        last = ts;
        setT(clock.position);
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [clock, wall, playing]);
  return t;
}

export default function TimelineBar(p: TimelineBarProps) {
  const trackRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(800);
  const time = usePlayhead(p.clock, p.wall, p.playing || !!p.live);
  const center = time ?? Date.now();
  const { start, end } = viewRange(center, p.span);
  const [editing, setEditing] = useState<string | null>(null);

  useEffect(() => {
    const el = trackRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 800));
    ro.observe(el);
    setWidth(el.clientWidth || 800);
    return () => ro.disconnect();
  }, []);

  // Wheel and pinch zoom (non-passive, to keep the page from scrolling or zooming).
  const spanRef = useRef(p.span);
  spanRef.current = p.span;
  const onSpanRef = useRef(p.onSpan);
  onSpanRef.current = p.onSpan;
  useEffect(() => {
    const el = trackRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      onSpanRef.current(zoomSpan(spanRef.current, wheelFactor(e.deltaY, e.ctrlKey)));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, []);

  // Drag pans (scrubs); a click without movement jumps to that time.
  const drag = useRef<{ x: number; center: number; moved: boolean; pending: number | null } | null>(null);
  const seekRef = useRef(p.onSeek);
  seekRef.current = p.onSeek;
  const onDown = (e: React.PointerEvent) => {
    if (e.button !== 0 || time === null) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    drag.current = { x: e.clientX, center: time, moved: false, pending: null };
  };
  const onMove = (e: React.PointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    if (!d.moved && Math.abs(dx) < 3) return;
    d.moved = true;
    const next = Math.min(Date.now(), panCenter(d.center, dx, p.span, width));
    if (d.pending === null) {
      requestAnimationFrame(() => {
        const dd = drag.current;
        if (dd?.pending != null) seekRef.current(dd.pending);
        if (dd) dd.pending = null;
      });
    }
    d.pending = next;
  };
  const onUp = (e: React.PointerEvent) => {
    const d = drag.current;
    drag.current = null;
    if (!d) return;
    if (d.moved) {
      if (d.pending !== null) p.onSeek(d.pending);
    } else {
      const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
      p.onSeek(Math.min(Date.now(), xToTime(e.clientX - rect.left, start, p.span, width)));
    }
  };

  const step = tickStep(p.span, width);
  const marks = useMemo(() => ticks(start, end, step), [start, end, step]);
  const x = (t: number) => timeToX(t, start, p.span, width);
  const band = (r: Range) => {
    const l = Math.max(0, x(r.start));
    const rr = Math.min(width, x(r.end));
    return rr > l ? { left: l, width: Math.max(1, rr - l) } : null;
  };
  const inView = (r: Range) => r.end >= start && r.start <= end;
  const sel = selectionRange(p.selection);
  const exp = p.exporter;
  const skip = p.skipLabels ?? { prev: "gap", next: "gap" };

  return (
    <div className="tl-bar" role="region" aria-label="Timeline">
      <div className="tl-top">
        <div className="tl-top-left">
          <input
            type="datetime-local"
            step={1}
            className="op-date"
            aria-label="Jump to date and time"
            title="Jump to a date and time"
            max={localInput(Date.now())}
            value={editing ?? (time !== null ? localInput(time) : "")}
            onFocus={() => time !== null && setEditing(localInput(time))}
            onBlur={() => setEditing(null)}
            onChange={(e) => {
              setEditing(e.target.value);
              const t = new Date(e.target.value).getTime();
              if (Number.isFinite(t)) p.onSeek(Math.min(Date.now(), t));
            }}
          />
        </div>
        <div className="tl-readout" aria-live="off">
          <span className="tl-clock mono">{time !== null ? fmtClock(time) : "--:--:--"}</span>
          {p.live && <span className="tl-live-tag">● LIVE</span>}
          <span className="tl-date muted small">{time !== null ? dateLabel(time) : ""}</span>
        </div>
        <div className="tl-top-right">
          <button className="btn small" disabled={time === null} onClick={p.onMarkIn} title="Mark in ([)">
            [ In
          </button>
          <button className="btn small" disabled={time === null} onClick={p.onMarkOut} title="Mark out (])">
            Out ]
          </button>
          {(p.selection.inAt !== null || p.selection.outAt !== null) && (
            <button className="btn small" onClick={p.onClearSelection} title="Clear the selection" aria-label="Clear the selection">
              ✕
            </button>
          )}
          {exp &&
            sel &&
            (exp.progress !== null ? (
              <>
                <span className="muted small">Exporting… {Math.round(exp.progress * 100)}%</span>
                <button className="btn small" onClick={exp.onCancel}>
                  Cancel
                </button>
              </>
            ) : (
              <>
                <button className="btn small primary" disabled={!!exp.blocker} title={exp.blocker || "Export the selected range as MP4"} onClick={exp.onExport}>
                  Export…
                </button>
                {exp.onLock && (
                  <button className="btn small" disabled={!!exp.blocker} title={exp.blocker || "Lock the selected range as evidence in the Exports workspace"} onClick={exp.onLock}>
                    Lock as evidence…
                  </button>
                )}
              </>
            ))}
          {exp?.message && <span className="muted small tl-msg">{exp.message}</span>}
          {p.recover &&
            (p.recover.running ? (
              <button className="btn small" onClick={p.recover.onCancel} title="Stop recovering from the camera">
                Stop recovery
              </button>
            ) : (
              <button className="btn small" disabled={p.recover.disabled} onClick={p.recover.onRecover} title={p.recover.title}>
                Recover from camera
              </button>
            ))}
          {p.recover?.message && <span className="muted small tl-msg">{p.recover.message}</span>}
        </div>
      </div>

      <div className="tl-track" ref={trackRef} onPointerDown={onDown} onPointerMove={onMove} onPointerUp={onUp} onPointerCancel={() => (drag.current = null)}>
        <div className="tl-ruler">
          {marks.map((m) => (
            <div key={m.t} className={`tl-tick ${m.major ? "major" : ""}`} style={{ left: x(m.t) }}>
              <span>{m.label}</span>
            </div>
          ))}
        </div>
        <div className="tl-lane tl-any" title="Any recording on the other tiles">
          {p.anyLane.filter(inView).map((r, i) => {
            const b = band(r);
            return b && <div key={i} className={`tl-band any${r.quality === "low" ? " low" : ""}`} style={b} />;
          })}
        </div>
        <div className="tl-lane tl-cam" title={p.lane ? `${p.lane.name}: recordings` : undefined}>
          {p.lane?.spans.filter(inView).map((r, i) => {
            const b = band(r);
            const low = r.quality === "low";
            return b && <div key={i} className={`tl-band${low ? " low" : ""}`} style={b} title={low ? "Low-res" : undefined} />;
          })}
          {p.lane?.pending.filter(inView).map((r, i) => {
            const b = band(r);
            return b && <div key={`p${i}`} className="tl-band pending" style={b} />;
          })}
          {p.lane?.edge?.filter(inView).map((r, i) => {
            const b = band(r);
            return b && <div key={`e${i}`} className="tl-band edge" style={b} title="Recovered from the camera's SD card" />;
          })}
          {p.lane?.markers
            .filter((m) => m.at >= start && m.at <= end)
            .map((m, i) => (
              <button
                key={i}
                className={`tl-marker ${m.alarm ? "alarm" : m.kind.toLowerCase()}`}
                style={{ left: x(m.at), "--mk": markerTone(m) } as CSSProperties}
                title={`${m.alarm ? "Alarm" : m.kind}: ${m.label}`}
                aria-label={`${m.alarm ? "Alarm" : "Event"} ${m.label}`}
                onPointerDown={(e) => e.stopPropagation()}
                onClick={() => p.onSeek(m.at)}
              />
            ))}
        </div>
        {p.bookmarks &&
          inWindow(p.bookmarks, start, end).map((b) => {
            const color = colorOf(b);
            const tip = `${bookmarkLabel(b)}${isRangeBookmark(b) ? ` (${bookmarkDuration(b.durationMs)})` : ""}${b.note ? `\n${b.note}` : ""}`;
            const rangeBand = isRangeBookmark(b) ? band(bookmarkSpan(b)) : null;
            return (
              <div key={b.id}>
                {rangeBand && <div className="tl-bm-band" style={{ ...rangeBand, background: color }} />}
                <button
                  className="tl-bm"
                  style={{ left: x(b.atMs), color }}
                  title={tip}
                  aria-label={`Bookmark ${bookmarkLabel(b)}`}
                  onPointerDown={(e) => e.stopPropagation()}
                  onClick={() => p.onSeek(b.atMs)}
                />
              </div>
            );
          })}
        {sel && inView(sel) && <div className="tl-sel" style={band(sel) ?? undefined} />}
        {p.selection.inAt !== null && p.selection.inAt >= start && p.selection.inAt <= end && (
          <div className="tl-handle in" style={{ left: x(p.selection.inAt) }}>
            [
          </div>
        )}
        {p.selection.outAt !== null && p.selection.outAt >= start && p.selection.outAt <= end && (
          <div className="tl-handle out" style={{ left: x(p.selection.outAt) }}>
            ]
          </div>
        )}
        <div className="tl-playhead" aria-hidden="true">
          {time !== null && <span className="tl-tag mono">{fmtClock(time)}</span>}
        </div>
        {p.lane === null && <div className="tl-empty muted small">Select a tile to see its recordings.</div>}
      </div>

      <div className="tl-transport">
        <div className="tl-left muted small">
          <span className="tl-name">{p.lane?.name ?? ""}</span>
          {(hasLowRes(p.lane?.spans ?? []) || hasLowRes(p.anyLane)) && (
            <span className="tl-legend" title="Low-res: the camera's sub-stream, recorded around the clock. Full-res: around events.">
              <i className="tl-key" aria-hidden="true" />
              Full-res
              <i className="tl-key low" aria-hidden="true" />
              Low-res
            </span>
          )}
          <label className="check" title="Hear the selected tile">
            <input type="checkbox" checked={p.sound} onChange={(e) => p.onSound(e.target.checked)} /> Sound
          </label>
        </div>
        <div className="tl-controls" role="group" aria-label="Transport">
          <button className="btn small" disabled={time === null} onClick={() => p.onGap(-1)} title={`Previous ${skip.prev}: ${skip.prev === "event" ? "jump to the previous event" : "back over the gap to the earlier footage"}`}>
            |« Prev {skip.prev}
          </button>
          <button className="btn small" disabled={time === null} onClick={(e) => p.onStep(e.shiftKey ? -10000 : -1000)} title="Step back 1 s (←); Shift-click or Shift+← for 10 s" aria-label="Step back 1 second">
            ‹ 1 s
          </button>
          <button className="btn primary tl-play" disabled={time === null || !!p.live} onClick={p.onPlayPause} title="Play/pause (Space)" aria-label={p.playing ? "Pause" : "Play"}>
            {p.playing ? "❚❚" : "▶"}
          </button>
          <button className="btn small" disabled={time === null} onClick={(e) => p.onStep(e.shiftKey ? 10000 : 1000)} title="Step forward 1 s (→); Shift-click or Shift+→ for 10 s" aria-label="Step forward 1 second">
            1 s ›
          </button>
          <button className="btn small" disabled={time === null} onClick={() => p.onGap(1)} title={`Next ${skip.next}: ${skip.next === "event" ? "jump to the next event" : "skip the gap to the next footage"}`}>
            Next {skip.next} »|
          </button>
          <select className="select-inline tl-speed" aria-label="Speed" value={p.rate} onChange={(e) => p.onRate(Number(e.target.value))}>
            {speeds.map((s) => (
              <option key={s} value={s}>
                {s}×
              </option>
            ))}
          </select>
        </div>
        <div className="tl-right">
          <span className="muted small tl-span">{spanLabel(p.span)} visible</span>
          <button className="btn small live" onClick={p.onLive} disabled={!!p.live} title={p.live ? "Watching live" : "Back to live (L)"}>
            {p.live ? "● Live" : "Back to live ●"}
          </button>
        </div>
      </div>
    </div>
  );
}

function spanLabel(ms: number): string {
  if (ms >= 3600e3) return `${Math.round((ms / 3600e3) * 10) / 10} h`;
  if (ms >= 60e3) return `${Math.round((ms / 60e3) * 10) / 10} min`;
  return `${Math.round(ms / 1000)} s`;
}
