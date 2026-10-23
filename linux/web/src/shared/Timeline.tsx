import { useRef, useState, type CSSProperties, type PointerEvent } from "react";
import { dayBounds, fmtClock, fmtHM, type Marker, type TimeSpan } from "./time";
import { markerTone } from "./alarmKind";
import "./timeline.css";

type TimelineSpan = TimeSpan;

/**
 * One camera's row. From an API v2 Timeline:
 * `{ id, name, spans: tl.spans, pending: tl.pending, markers: markersFromV2(id, tl) }`.
 * Spans from several recording servers (a camera that moved) can share a lane.
 */
export interface Lane {
  id: string;
  name: string;
  spans: TimelineSpan[];
  pending: TimelineSpan[];
  markers: Marker[];
}

const HOUR = 3600e3;

/** Tick spacing and label spacing for a visible range. */
function tickSteps(range: number): [number, number] {
  if (range > 12 * HOUR) return [HOUR, 2 * HOUR];
  if (range > 3 * HOUR) return [HOUR / 2, HOUR];
  if (range > 30 * 60e3) return [5 * 60e3, 10 * 60e3];
  return [60e3, 2 * 60e3];
}

function markerClass(m: Marker): string {
  if (m.alarm) return m.severity === "Critical" ? "alarm critical" : "alarm";
  return m.kind.toLowerCase();
}

/**
 * Day timeline: one lane per camera with recorded spans (gaps are empty),
 * event ticks and alarm flags, the playhead, the export selection and a
 * future area. Click or drag to scrub; the seek happens on release.
 */
export default function PlaybackTimeline({
  viewStart,
  viewEnd,
  lanes,
  playhead,
  selection,
  now,
  onSeek,
}: {
  viewStart: number;
  viewEnd: number;
  lanes: Lane[];
  playhead: number | null;
  selection: { start: number; end: number } | null;
  now: number;
  onSeek: (t: number) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const [drag, setDrag] = useState<number | null>(null);
  const range = viewEnd - viewStart;
  const pct = (t: number) => ((t - viewStart) / range) * 100;
  const clampPct = (t: number) => Math.min(100, Math.max(0, pct(t)));
  const timeAt = (e: PointerEvent) => {
    const r = ref.current!.getBoundingClientRect();
    const f = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    return viewStart + f * range;
  };

  const [tick, label] = tickSteps(range);
  const base = dayBounds(viewStart)[0];
  const ticks: number[] = [];
  for (let t = base + Math.ceil((viewStart - base) / tick) * tick; t <= viewEnd; t += tick) ticks.push(t);

  const visible = (s: TimelineSpan) => s.end > viewStart && s.start < viewEnd;
  const bar = (s: TimelineSpan) => ({ left: `${clampPct(s.start)}%`, width: `${Math.max(0.15, clampPct(s.end) - clampPct(s.start))}%` });
  const head = drag ?? playhead;

  return (
    <div
      className="tl"
      ref={ref}
      onPointerDown={(e) => {
        e.currentTarget.setPointerCapture(e.pointerId);
        setDrag(timeAt(e));
      }}
      onPointerMove={(e) => {
        const t = timeAt(e);
        setHover(t);
        if (drag !== null) setDrag(t);
      }}
      onPointerUp={(e) => {
        if (drag !== null) onSeek(timeAt(e));
        setDrag(null);
      }}
      onPointerCancel={() => setDrag(null)}
      onPointerLeave={() => setHover(null)}
    >
      <div className="tl-ticks">
        {ticks.map((t) => (
          <div key={t} className={`tl-tick ${(t - base) % label === 0 ? "major" : ""}`} style={{ left: `${pct(t)}%` }}>
            {(t - base) % label === 0 && <span>{fmtHM(t)}</span>}
          </div>
        ))}
      </div>
      {lanes.map((lane) => (
        <div key={lane.id} className="tl-lane" title={lane.name}>
          {lanes.length > 1 && <div className="tl-lane-name">{lane.name}</div>}
          {lane.spans.filter(visible).map((s) => (
            <div key={`s${s.start}`} className="tl-span" style={bar(s)} />
          ))}
          {lane.pending.filter(visible).map((s) => (
            <div key={`p${s.start}`} className="tl-span pending" style={bar(s)} />
          ))}
          {lane.markers
            .filter((m) => m.at >= viewStart && m.at <= viewEnd)
            .map((m, i) => (
              <div key={i} className={`tl-marker ${markerClass(m)}`} style={{ left: `${pct(m.at)}%`, "--mk": markerTone(m) } as CSSProperties} title={`${fmtClock(m.at)} · ${m.label}`} />
            ))}
        </div>
      ))}
      {now < viewEnd && <div className="tl-future" style={{ left: `${clampPct(now)}%` }} />}
      {selection && selection.end > viewStart && selection.start < viewEnd && (
        <div className="tl-selection" style={{ left: `${clampPct(selection.start)}%`, width: `${clampPct(selection.end) - clampPct(selection.start)}%` }} />
      )}
      {head !== null && head >= viewStart && head <= viewEnd && (
        <div className="tl-head" style={{ left: `${pct(head)}%` }}>
          <span>{fmtClock(head)}</span>
        </div>
      )}
      {hover !== null && drag === null && (
        <div className="tl-hover" style={{ left: `${pct(hover)}%` }}>
          <span>{fmtClock(hover)}</span>
        </div>
      )}
    </div>
  );
}
