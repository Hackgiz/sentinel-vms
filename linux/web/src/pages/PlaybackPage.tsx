import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Hls from "hls.js";
import { api, can, formatBytes, type Camera, type ClipExport, type Evidence, type Timeline, type TimelineSpan, type User } from "../api";
import {
  adjacentMarker,
  dateInputValue,
  dayBounds,
  fmtClock,
  fmtDuration,
  markersOf,
  mediaToWall,
  nextSpanStart,
  parseDateInput,
  recordedBetween,
  wallToMedia,
} from "../playback";
import PlaybackTimeline, { type Lane } from "../shared/Timeline";
import "./PlaybackPage.css";

const MAX_CAMERAS = 4;
const MAX_CLIP = 30 * 60e3;
const zooms = [
  { label: "24h", ms: 24 * 3600e3 },
  { label: "6h", ms: 6 * 3600e3 },
  { label: "1h", ms: 3600e3 },
  { label: "10m", ms: 10 * 60e3 },
];
const rates = [1, 2, 4, 8];

/** The playlist a tile has loaded, and the spans it was built from. */
interface Loaded {
  spans: TimelineSpan[];
  version: number;
}

function loadSelection(): string[] {
  try {
    return JSON.parse(localStorage.getItem("playback.cameras") ?? "[]");
  } catch {
    return [];
  }
}

function timeInputValue(t: number): string {
  const d = new Date(t);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function parseTimeInput(day: number, v: string): number | null {
  const m = /^(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(v);
  if (!m) return null;
  const d = new Date(day);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate(), Number(m[1]), Number(m[2]), Number(m[3] ?? 0)).getTime();
}

export default function PlaybackPage({ user }: { user: User }) {
  const [cameras, setCameras] = useState<Camera[] | null>(null);
  const [selected, setSelected] = useState<string[]>(loadSelection);
  const [day, setDay] = useState(() => dayBounds(Date.now())[0]);
  const dayEnd = dayBounds(day)[1];
  const [timelines, setTimelines] = useState<Record<string, Timeline>>({});
  const [loaded, setLoaded] = useState<Record<string, Loaded>>({});
  const [wall, setWall] = useState<number | null>(null);
  const [playing, setPlaying] = useState(false);
  const [rate, setRate] = useState(1);
  const [sound, setSound] = useState(false);
  const [zoom, setZoom] = useState(zooms[0].ms);
  const [viewStart, setViewStart] = useState(day);
  const [gaps, setGaps] = useState<Record<string, boolean>>({});
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const [clipStart, setClipStart] = useState<number | null>(null);
  const [clipEnd, setClipEnd] = useState<number | null>(null);

  // The clock lives in refs so the 4 Hz tick doesn't depend on React renders.
  const wallRef = useRef<number | null>(null);
  const playingRef = useRef(false);
  const rateRef = useRef(1);
  const lastTick = useRef(0);
  const videos = useRef(new Map<string, HTMLVideoElement>());
  const loadedRef = useRef(loaded);
  const timelinesRef = useRef(timelines);
  const versionRef = useRef(0);
  loadedRef.current = loaded;
  timelinesRef.current = timelines;
  rateRef.current = rate;

  const cams = useMemo(
    () => (cameras ?? []).filter((c) => selected.includes(c.id)).slice(0, MAX_CAMERAS),
    [cameras, selected],
  );
  const camIDs = cams.map((c) => c.id).join(",");

  useEffect(() => {
    api
      .cameras()
      .then((c) => {
        setCameras(c);
        setSelected((sel) => {
          const valid = sel.filter((id) => c.some((x) => x.id === id));
          return valid.length ? valid : c.slice(0, 1).map((x) => x.id);
        });
      })
      .catch((e) => setError(e.message));
  }, []);
  useEffect(() => {
    try {
      localStorage.setItem("playback.cameras", JSON.stringify(selected));
    } catch {}
  }, [selected]);

  const setClock = useCallback((t: number | null) => {
    wallRef.current = t;
    setWall(t);
  }, []);

  // Load the day's timelines; reload playlists only when the day or camera set changes.
  useEffect(() => {
    if (!camIDs) return;
    let alive = true;
    const ids = camIDs.split(",");
    const fetchAll = (initial: boolean) =>
      Promise.all(ids.map((id) => api.timeline(id, day, dayEnd).then((tl) => [id, tl] as const)))
        .then((list) => {
          if (!alive) return;
          const next = Object.fromEntries(list);
          setTimelines(next);
          setNow(Date.now());
          setError("");
          if (!initial) return;
          const l: Record<string, Loaded> = {};
          for (const [id, tl] of list) l[id] = { spans: tl.spans, version: ++versionRef.current };
          setLoaded(l);
          // Start near the newest recording today, at the first one on past days.
          const all = list.flatMap(([, tl]) => tl.spans);
          let start = day;
          if (all.length) {
            const today = dayBounds(Date.now())[0] === day;
            start = today ? Math.max(...all.map((s) => s.end)) - 30e3 : Math.min(...all.map((s) => s.start));
          }
          setClock(start);
        })
        .catch((e) => alive && setError(e.message));
    fetchAll(true);
    const today = dayBounds(Date.now())[0] === day;
    const t = today ? setInterval(() => fetchAll(false), 30000) : undefined;
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [camIDs, day, dayEnd, setClock]);

  /** Reload a camera's playlist when the fresh timeline covers t but the loaded one doesn't. */
  const reloadIfNewer = useCallback((id: string, t: number) => {
    const fresh = timelinesRef.current[id];
    const cur = loadedRef.current[id];
    if (!fresh || !cur || wallToMedia(cur.spans, t) !== null || wallToMedia(fresh.spans, t) === null) return false;
    setLoaded((l) => ({ ...l, [id]: { spans: fresh.spans, version: ++versionRef.current } }));
    return true;
  }, []);

  const seek = useCallback(
    (t: number) => {
      const ids = camIDs ? camIDs.split(",") : [];
      // In a gap on every camera: go to the next recording, if any.
      if (ids.length && !ids.some((id) => wallToMedia(timelinesRef.current[id]?.spans ?? [], t) !== null)) {
        const next = ids.map((id) => nextSpanStart(timelinesRef.current[id]?.spans ?? [], t)).filter((x): x is number => x !== null);
        if (next.length && Math.min(...next) - t < 5 * 60e3) t = Math.min(...next);
      }
      setClock(t);
      for (const id of ids) {
        const v = videos.current.get(id);
        const m = wallToMedia(loadedRef.current[id]?.spans ?? [], t);
        if (m === null) {
          if (!reloadIfNewer(id, t)) v?.pause();
          continue;
        }
        if (v) {
          v.currentTime = m;
          if (playingRef.current) v.play().catch(() => {});
        }
      }
    },
    [camIDs, reloadIfNewer, setClock],
  );

  const setPlayingBoth = useCallback((p: boolean) => {
    playingRef.current = p;
    setPlaying(p);
    lastTick.current = performance.now();
    for (const v of videos.current.values()) {
      if (!p) v.pause();
    }
  }, []);

  // The clock: follow a playing camera ("leader"), keep the others within a
  // second of it, pause cameras that have a gap, skip gaps nobody recorded.
  useEffect(() => {
    const ids = camIDs ? camIDs.split(",") : [];
    const timer = setInterval(() => {
      const nowPerf = performance.now();
      const dt = (nowPerf - lastTick.current) * rateRef.current;
      lastTick.current = nowPerf;
      let w = wallRef.current;
      if (!playingRef.current || w === null || !ids.length) return;
      const expected = w + dt;
      w = expected;
      for (const id of ids) {
        const v = videos.current.get(id);
        const spans = loadedRef.current[id]?.spans ?? [];
        if (!v || v.paused || v.seeking || v.readyState < 3) continue;
        const lw = mediaToWall(spans, v.currentTime);
        if (lw === null) continue;
        // A leader that jumped over its own gap is followed only if no other
        // camera recorded something in between.
        const othersBetween = lw > expected + 1500 && ids.some((o) => o !== id && recordedBetween(timelinesRef.current[o]?.spans ?? [], expected, lw));
        if (Math.abs(lw - expected) < 1500 || (lw > expected && !othersBetween)) {
          w = lw;
          break;
        }
      }
      // Nobody recorded at w: jump to the next recording, or stop at the end.
      if (!ids.some((id) => wallToMedia(timelinesRef.current[id]?.spans ?? [], w!) !== null)) {
        const next = ids.map((id) => nextSpanStart(timelinesRef.current[id]?.spans ?? [], w!)).filter((x): x is number => x !== null);
        if (!next.length) {
          setPlayingBoth(false); // past the last recording
          return;
        }
        w = Math.min(...next);
      }
      wallRef.current = w;
      setWall(w);
      const gapNow: Record<string, boolean> = {};
      for (const id of ids) {
        const v = videos.current.get(id);
        const m = wallToMedia(loadedRef.current[id]?.spans ?? [], w);
        gapNow[id] = m === null && wallToMedia(timelinesRef.current[id]?.spans ?? [], w) === null;
        if (!v) continue;
        if (m === null) {
          if (!v.paused) v.pause();
          reloadIfNewer(id, w);
          continue;
        }
        if (v.playbackRate !== rateRef.current) v.playbackRate = rateRef.current;
        if (v.paused) {
          if (Math.abs(v.currentTime - m) > 0.3) v.currentTime = m;
          v.play().catch(() => {});
        } else if (!v.seeking && Math.abs(v.currentTime - m) > 1.5) {
          v.currentTime = m;
        }
      }
      setGaps((g) => (ids.some((id) => g[id] !== gapNow[id]) ? gapNow : g));
    }, 250);
    return () => clearInterval(timer);
  }, [camIDs, reloadIfNewer, setPlayingBoth]);

  // Keep the playhead in view when zoomed in.
  useEffect(() => {
    if (zoom >= dayEnd - day) {
      setViewStart(day);
      return;
    }
    if (wall === null) return;
    setViewStart((vs) => {
      if (wall >= vs + zoom * 0.05 && wall <= vs + zoom * 0.95 && vs >= day && vs + zoom <= dayEnd) return vs;
      return Math.min(Math.max(wall - zoom * 0.2, day), dayEnd - zoom);
    });
  }, [wall, zoom, day, dayEnd]);

  const markers = useMemo(
    () => cams.flatMap((c) => (timelines[c.id] ? markersOf(c.id, timelines[c.id]) : [])).sort((a, b) => a.at - b.at),
    [cams, timelines],
  );
  const lanes: Lane[] = cams.map((c) => ({
    id: c.id,
    name: c.name,
    spans: timelines[c.id]?.spans ?? [],
    pending: timelines[c.id]?.pending ?? [],
    markers: timelines[c.id] ? markersOf(c.id, timelines[c.id]) : [],
  }));

  // Jumps land 3 s before the marker (pre-roll); remember it so the next
  // "next event" press doesn't land on the same one again.
  const lastJump = useRef(0);
  const jump = useCallback(
    (dir: 1 | -1) => {
      const w = wallRef.current;
      if (w === null) return;
      const from = dir > 0 && w < lastJump.current && lastJump.current - w <= 3500 ? lastJump.current : w;
      const m = adjacentMarker(markers, from, dir);
      if (!m) return;
      lastJump.current = m.at;
      seek(m.at - 3000);
    },
    [markers, seek],
  );

  // Keyboard: space play/pause, ←/→ 10 s, shift+←/→ previous/next event.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName;
      if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || wallRef.current === null) return;
      if (e.key === " ") {
        e.preventDefault();
        setPlayingBoth(!playingRef.current);
      } else if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        e.preventDefault();
        const dir = e.key === "ArrowRight" ? 1 : -1;
        if (e.shiftKey) jump(dir);
        else seek(wallRef.current + dir * 10e3);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [jump, seek, setPlayingBoth]);

  const changeDay = (d: number) => {
    setPlayingBoth(false);
    setTimelines({});
    setLoaded({});
    setClock(null);
    setClipStart(null);
    setClipEnd(null);
    setDay(dayBounds(d)[0]);
  };

  const toggleCamera = (id: string) => {
    setPlayingBoth(false);
    setSelected((sel) => (sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id].slice(-MAX_CAMERAS)));
  };

  if (cameras === null) return error ? <div className="error">{error}</div> : null;
  const today = dayBounds(Date.now())[0];
  const columns = cams.length <= 1 ? 1 : 2;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Playback</h1>
          <div className="sub">Timeline of recordings, events and alarms — up to {MAX_CAMERAS} cameras in sync</div>
        </div>
        <div className="pb-date">
          <button className="btn small" onClick={() => changeDay(day - 12 * 3600e3)} aria-label="Previous day">
            ‹
          </button>
          <input
            type="date"
            value={dateInputValue(day)}
            max={dateInputValue(today)}
            onChange={(e) => {
              const d = parseDateInput(e.target.value);
              if (d !== null) changeDay(d);
            }}
          />
          <button className="btn small" disabled={day >= today} onClick={() => changeDay(dayEnd + 12 * 3600e3)} aria-label="Next day">
            ›
          </button>
          {day !== today && (
            <button className="btn small" onClick={() => changeDay(Date.now())}>
              Today
            </button>
          )}
        </div>
      </div>
      {error && <div className="error">{error}</div>}
      {cameras.length === 0 ? (
        <div className="card empty">No cameras yet.</div>
      ) : (
        <>
          <div className="pb-cams">
            {cameras.map((c) => (
              <button
                key={c.id}
                className={`pb-chip ${selected.includes(c.id) ? "on" : ""}`}
                onClick={() => toggleCamera(c.id)}
                title={selected.includes(c.id) ? "Remove from playback" : "Add to playback"}
              >
                {c.name}
              </button>
            ))}
          </div>
          {cams.length === 0 ? (
            <div className="card empty">Pick a camera above.</div>
          ) : (
            <div className="live-grid" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
              {cams.map((c, i) => (
                <PlaybackTile
                  key={c.id}
                  camera={c}
                  url={
                    loaded[c.id]?.spans.length
                      ? `/api/v1/cameras/${c.id}/playback.m3u8?from=${day}&to=${dayEnd}&v=${loaded[c.id].version}`
                      : ""
                  }
                  loading={!timelines[c.id]}
                  gap={!!gaps[c.id] || (wall !== null && !!timelines[c.id] && wallToMedia(timelines[c.id].spans, wall) === null)}
                  muted={!sound || i > 0}
                  startPosition={() => {
                    const t = wallRef.current;
                    return t === null ? 0 : (wallToMedia(loadedRef.current[c.id]?.spans ?? [], t) ?? 0);
                  }}
                  onVideo={(v) => {
                    if (v) videos.current.set(c.id, v);
                    else videos.current.delete(c.id);
                  }}
                />
              ))}
            </div>
          )}

          <div className="card pb-controls">
            <div className="pb-buttons">
              <button className="btn" onClick={() => jump(-1)} disabled={!markers.length} title="Previous event (Shift+←)">
                ⏮ Event
              </button>
              <button className="btn" onClick={() => wall !== null && seek(wall - 10e3)} disabled={wall === null} title="Back 10 s (←)">
                −10s
              </button>
              <button className="btn primary pb-play" onClick={() => setPlayingBoth(!playing)} disabled={wall === null} title="Play/pause (space)">
                {playing ? "❚❚ Pause" : "▶ Play"}
              </button>
              <button className="btn" onClick={() => wall !== null && seek(wall + 10e3)} disabled={wall === null} title="Forward 10 s (→)">
                +10s
              </button>
              <button className="btn" onClick={() => jump(1)} disabled={!markers.length} title="Next event (Shift+→)">
                Event ⏭
              </button>
              <div className="pb-clock mono">{wall !== null ? fmtClock(wall) : "--:--:--"}</div>
              <div className="grow" />
              <label className="check pb-sound">
                <input type="checkbox" checked={sound} onChange={(e) => setSound(e.target.checked)} /> Sound
              </label>
              <div className="seg" role="group" aria-label="Speed">
                {rates.map((r) => (
                  <button key={r} className={r === rate ? "on" : ""} onClick={() => setRate(r)}>
                    {r}×
                  </button>
                ))}
              </div>
              <div className="seg" role="group" aria-label="Zoom">
                {zooms.map((z) => (
                  <button key={z.label} className={z.ms === zoom ? "on" : ""} onClick={() => setZoom(z.ms)}>
                    {z.label}
                  </button>
                ))}
              </div>
            </div>
            <PlaybackTimeline
              viewStart={viewStart}
              viewEnd={viewStart + Math.min(zoom, dayEnd - day)}
              lanes={lanes}
              playhead={wall}
              selection={clipStart !== null && clipEnd !== null && clipEnd > clipStart ? { start: clipStart, end: clipEnd } : null}
              now={day === today ? now : dayEnd}
              onSeek={seek}
            />
            <div className="pb-legend">
              <span>
                <i className="lg-span" /> Recorded
              </span>
              <span>
                <i className="lg-motion" /> Motion
              </span>
              <span>
                <i className="lg-person" /> Person / vehicle
              </span>
              <span>
                <i className="lg-alarm" /> Alarm
              </span>
              <span className="muted">Click or drag the timeline to scrub · gaps are time with no recording</span>
            </div>
          </div>

          {can.exportEvidence(user) && wall !== null && cams.length > 0 && (
            <ExportPanel
              cams={cams}
              day={day}
              wall={wall}
              timelines={timelines}
              start={clipStart}
              end={clipEnd}
              setStart={setClipStart}
              setEnd={setClipEnd}
            />
          )}
        </>
      )}
    </>
  );
}

function PlaybackTile({
  camera,
  url,
  loading,
  gap,
  muted,
  startPosition,
  onVideo,
}: {
  camera: Camera;
  url: string;
  loading: boolean;
  gap: boolean;
  muted: boolean;
  startPosition: () => number;
  onVideo: (v: HTMLVideoElement | null) => void;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const [failed, setFailed] = useState("");
  const [attempt, setAttempt] = useState(0);
  const start = useRef(startPosition);
  start.current = startPosition;
  const report = useRef(onVideo);
  report.current = onVideo;

  // React doesn't keep the muted attribute in sync after mount.
  useEffect(() => {
    if (ref.current) ref.current.muted = muted;
  }, [muted]);

  useEffect(() => {
    const video = ref.current;
    if (!video || !url) return;
    setFailed("");
    report.current(video);
    let hls: Hls | undefined;
    const pos = start.current();
    if (Hls.isSupported()) {
      hls = new Hls({
        startPosition: pos,
        maxBufferLength: 20,
        maxMaxBufferLength: 60,
        backBufferLength: 30,
        debug: localStorage.getItem("playback.debug") === "1",
      });
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (data.fatal) {
          if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
            hls?.recoverMediaError();
            return;
          }
          setFailed("Can't play this recording.");
          hls?.destroy();
        }
      });
      hls.loadSource(url);
      hls.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = url;
      video.addEventListener("loadedmetadata", () => (video.currentTime = pos), { once: true });
      video.addEventListener("error", () => setFailed("Can't play this recording."), { once: true });
    } else {
      setFailed("This browser can't play recordings.");
    }
    return () => {
      report.current(null);
      hls?.destroy();
      video.removeAttribute("src");
      video.load();
    };
  }, [url, attempt]);

  return (
    <div className="tile pb-tile">
      <video ref={ref} muted={muted} playsInline />
      <div className="overlay">
        <div className="grow">
          <div className="name">{camera.name}</div>
          {camera.location && <div className="loc">{camera.location}</div>}
        </div>
      </div>
      {(loading || !url || gap || failed) && (
        <div className="placeholder pb-placeholder">
          <div>
            <span className="big">{failed ? "⚠" : "▶"}</span>
            {failed ? (
              <>
                {failed}{" "}
                <button className="btn small" onClick={() => setAttempt((n) => n + 1)}>
                  Retry
                </button>
              </>
            ) : loading ? (
              "Loading…"
            ) : !url ? (
              "No recordings on this day"
            ) : (
              "No recording at this time"
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function ExportPanel({
  cams,
  day,
  wall,
  timelines,
  start,
  end,
  setStart,
  setEnd,
}: {
  cams: Camera[];
  day: number;
  wall: number;
  timelines: Record<string, Timeline>;
  start: number | null;
  end: number | null;
  setStart: (t: number | null) => void;
  setEnd: (t: number | null) => void;
}) {
  const [cameraID, setCameraID] = useState(cams[0].id);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [done, setDone] = useState<{ clip?: ClipExport; evidence?: Evidence } | null>(null);

  useEffect(() => {
    if (!cams.some((c) => c.id === cameraID)) setCameraID(cams[0].id);
  }, [cams, cameraID]);

  const spans = timelines[cameraID]?.spans ?? [];
  const length = start !== null && end !== null ? end - start : 0;
  let problem = "";
  if (start === null || end === null) problem = "Set a start and an end.";
  else if (length <= 0) problem = "The end must be after the start.";
  else if (length > MAX_CLIP) problem = "Clips can be at most 30 minutes.";
  else if (!recordedBetween(spans, start, end)) problem = "Nothing was recorded in that range on this camera.";

  const run = async (kind: "clip" | "evidence") => {
    if (problem || start === null || end === null) return;
    setBusy(kind);
    setError("");
    setDone(null);
    try {
      if (kind === "clip") {
        const clip = await api.exportClip(cameraID, start, end);
        setDone({ clip });
        const a = document.createElement("a");
        a.href = clip.url;
        a.download = clip.filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
      } else {
        const r = await api.exportEvidence(cameraID, start, end, title.trim());
        setDone({ evidence: r.evidence });
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const snapshot = async () => {
    setBusy("snapshot");
    setError("");
    try {
      const res = await fetch(`/api/v1/cameras/${cameraID}/snapshot.jpg?t=${Math.round(wall)}`, { credentials: "same-origin" });
      if (!res.ok) {
        const data = await res.json().catch(() => null);
        throw new Error(data?.error ?? `Snapshot failed (${res.status})`);
      }
      const url = URL.createObjectURL(await res.blob());
      const disp = res.headers.get("Content-Disposition") ?? "";
      const a = document.createElement("a");
      a.href = url;
      a.download = /filename="([^"]+)"/.exec(disp)?.[1] ?? "snapshot.jpg";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  };

  const timeField = (label: string, value: number | null, set: (t: number | null) => void, other: () => void) => (
    <label className="field pb-time">
      <span>{label}</span>
      <div className="pb-time-row">
        <input
          type="time"
          step={1}
          value={value !== null ? timeInputValue(value) : ""}
          onChange={(e) => set(parseTimeInput(day, e.target.value))}
        />
        <button className="btn small" onClick={other} title="Use the playhead time">
          ⌖ Playhead
        </button>
      </div>
    </label>
  );

  return (
    <div className="card pb-export">
      <h2>Export</h2>
      <div className="pb-export-row">
        {cams.length > 1 && (
          <label className="field">
            <span>Camera</span>
            <select value={cameraID} onChange={(e) => setCameraID(e.target.value)}>
              {cams.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
        )}
        {timeField("Start", start, setStart, () => {
          setStart(Math.floor(wall / 1000) * 1000);
          if (end === null || end <= wall) setEnd(Math.floor(wall / 1000) * 1000 + 30e3);
        })}
        {timeField("End", end, setEnd, () => setEnd(Math.ceil(wall / 1000) * 1000))}
        <div className="pb-length">
          <span className="muted small">Length</span>
          <strong>{length > 0 ? fmtDuration(length) : "—"}</strong>
        </div>
      </div>
      <label className="field">
        <span>Evidence title (optional)</span>
        <input type="text" value={title} maxLength={120} placeholder="e.g. Package taken from porch" onChange={(e) => setTitle(e.target.value)} />
      </label>
      {problem && start !== null && end !== null && <div className="hint">{problem}</div>}
      {error && <div className="error">{error}</div>}
      <div className="alarm-actions">
        <button className="btn primary" disabled={!!problem || !!busy} onClick={() => run("clip")}>
          {busy === "clip" ? "Preparing…" : "⤓ Download clip"}
        </button>
        <button className="btn" disabled={!!problem || !!busy} onClick={() => run("evidence")}>
          {busy === "evidence" ? "Saving…" : "🔒 Save to evidence vault"}
        </button>
        <div className="grow" />
        <button className="btn" disabled={!!busy} onClick={snapshot} title="Save the frame at the playhead as a JPEG">
          {busy === "snapshot" ? "Saving…" : "◫ Snapshot"}
        </button>
      </div>
      {done?.clip && (
        <div className="notice success" style={{ marginTop: 12 }}>
          Clip ready: <a href={done.clip.url}>{done.clip.filename}</a> ({formatBytes(done.clip.size)}) — downloading.{" "}
          <span className="mono">SHA-256 {done.clip.sha256}</span>
        </div>
      )}
      {done?.evidence && (
        <div className="notice success" style={{ marginTop: 12 }}>
          Saved as evidence <strong>{done.evidence.caseID}</strong> ({formatBytes(done.evidence.size)}, {done.evidence.range}).{" "}
          <a href={`/api/v1/evidence/${done.evidence.id}/file`}>Download</a>
          <div className="mono">SHA-256 {done.evidence.sha256}</div>
        </div>
      )}
      <p className="muted small" style={{ marginBottom: 0 }}>
        Clips are the camera's original video (no re-encoding), start at the exact second in players that honor MP4 edit lists, and
        skip gaps in the recording. Every export is written to the audit log.
      </p>
    </div>
  );
}
