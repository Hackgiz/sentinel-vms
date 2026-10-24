// Playback mode: the workspace's camera grid showing recorded video, every
// tile on one clock (SyncClock), with the timeline bar along the bottom.
// The grid, the selected tile and the layout are the Live mode's; tiles
// follow the clock, the selected tile's camera fills the recording lane.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage, type Bookmark, type Camera, type Timeline } from "../shared/api";
import { dayBounds, markersFromV2 } from "../shared/time";
import BookmarkStrip from "./BookmarkStrip";
import { inWindow, removeBookmark, sortBookmarks, upsertBookmark } from "./bookmarks";
import CameraGrid from "./CameraGrid";
import { useData } from "./data";
import { exportReencodes } from "../shared/zones";
import { exportWindow, maxExportMs, saveBlob } from "./export";
import { camerasOf, type GridLayout } from "./layout";
import PlaybackTile, { type PlayerHandle } from "./PlaybackTile";
import { useSigned } from "./session";
import { locate, steer, wallAt, type PlaybackSpan, type SyncClock } from "./sync";
import { needsNextWindow, playWindowAround } from "../viewer/viewerMath";
import TimelineBar from "./TimelineBar";
import { clampSpan, emptySelection, exportBlocker, mergeSpans, nextGap, prevGap, selectionRange, setIn, setOut, type Selection } from "./timelineMath";
import type { PlaybackRequest } from "./Workspace";
import { edgeRanges, recoverTarget, recoverTitle } from "./edgeRecovery";
import { useEdgeRecovery } from "./useEdgeRecovery";

const maxCameras = 16;

/** WAVE24(D2): the hour either side of the playhead: the playlist window and the spans whose media offsets it plays. */
export interface PlayWindow {
  from: number;
  to: number;
  spans: PlaybackSpan[];
}
/** Browsers cap media playback rate; beyond it the clock runs ahead and tiles are re-seeked. */
const maxMediaRate = 16;
const spanKey = (siteId: string) => `operator.timeline.${siteId}`;

function loadSpan(siteId: string): number {
  try {
    const v = Number(localStorage.getItem(spanKey(siteId)));
    return Number.isFinite(v) && v > 0 ? clampSpan(v) : 3600e3;
  } catch {
    return 3600e3;
  }
}

export interface PlaybackProps {
  clock: SyncClock;
  layout: GridLayout;
  selected: number | null;
  onSelect: (i: number) => void;
  onAssign: (index: number, cameraId: string) => void;
  onSwap: (a: number, b: number) => void;
  onClear: (index: number) => void;
  request: PlaybackRequest | null;
  onRequestDone: () => void;
  onLive: () => void;
}

export default function Playback({ clock, layout, selected, onSelect, onAssign, onSwap, onClear, request, onRequestDone, onLive }: PlaybackProps) {
  const { session, can } = useSigned();
  const data = useData();
  const client = session.client;
  const [day, setDay] = useState(() => dayBounds(request?.at ?? clock.position ?? Date.now())[0]);
  const dayEnd = dayBounds(day)[1];
  const [timelines, setTimelines] = useState<Record<string, Timeline>>({});
  const [want, setWant] = useState<Record<string, string | null>>({});
  const [plays, setPlaysState] = useState<Record<string, PlayWindow>>({});
  const [versions, setVersions] = useState<Record<string, number>>({});
  const [gaps, setGaps] = useState<Record<string, boolean>>({});
  const [bookmarks, setBookmarks] = useState<Record<string, Bookmark[]>>({});
  /** The playhead the bookmark title field was opened at (B), null while closed. */
  const [adding, setAdding] = useState<number | null>(null);
  const [wall, setWall] = useState<number | null>(clock.position);
  const [playing, setPlaying] = useState(clock.playing);
  const [rate, setRate] = useState(clock.rate);
  const [span, setSpan] = useState(() => loadSpan(session.site.id));
  const [sound, setSound] = useState(false);
  const [error, setError] = useState("");
  const [selection, setSelection] = useState<Selection>(emptySelection);
  const [progress, setProgress] = useState<number | null>(null);
  const [exported, setExported] = useState("");
  const exportAbort = useRef<AbortController | null>(null);
  /** Bumped to re-read the day's timelines (after an edge recovery brought footage back). */
  const [reload, setReload] = useState(0);
  const edge = useEdgeRecovery(client, () => setReload((n) => n + 1));

  const players = useRef(new Map<string, PlayerHandle>());
  const reloadedAt = useRef(new Map<string, number>());
  const playsRef = useRef(plays);
  const loadingPlays = useRef(new Set<string>());
  const timelinesRef = useRef(timelines);
  timelinesRef.current = timelines;
  const pendingSeek = useRef<number | null>(request?.at ?? null);
  const dayRef = useRef(day);
  dayRef.current = day;
  /** The day whose timelines are in `timelines`; steering waits for it. */
  const loadedDay = useRef<number | null>(null);

  const cams = useMemo(
    () => camerasOf(layout).map((id) => data.camera(id)).filter((c): c is Camera => !!c).slice(0, maxCameras),
    [layout, data],
  );
  const camIds = cams.map((c) => c.id).join(",");
  const camsRef = useRef(cams);
  camsRef.current = cams;
  // Steering and media offsets come from the play window; the day's timelines only draw the lanes.
  const spansOf = (id: string): PlaybackSpan[] => playsRef.current[id]?.spans ?? [];

  /** Fetch a camera's play window around `at`; `bump` re-reads the playlist even if the window is the same. */
  const loadPlay = useCallback(
    (id: string, at: number, bump = false) => {
      if (loadingPlays.current.has(id)) return;
      loadingPlays.current.add(id);
      const w = playWindowAround(at, Date.now());
      const from = Math.round(w.from);
      const to = Math.round(w.to);
      client.cameras
        .timeline(id, from, to)
        .then((tl) => {
          const n = { ...playsRef.current, [id]: { from, to, spans: tl.spans } };
          playsRef.current = n;
          setPlaysState(n);
          if (bump) setVersions((vs) => ({ ...vs, [id]: (vs[id] ?? 0) + 1 }));
        })
        .then(
          () => loadingPlays.current.delete(id),
          // A failed read is retried after a pause, not on every steering tick.
          () => void setTimeout(() => loadingPlays.current.delete(id), 3000),
        );
    },
    [client],
  );

  // The selected tile's camera, else the first one on the grid.
  const selCam = (selected !== null ? data.camera(layout.tiles[selected]?.cameraId ?? "") : undefined) ?? cams[0];

  useEffect(() => {
    try {
      localStorage.setItem(spanKey(session.site.id), String(span));
    } catch {
      /* storage blocked */
    }
  }, [span, session.site.id]);

  // A request from Alarms or Events: that day, that time (the camera is already on the grid).
  useEffect(() => {
    if (!request) return;
    setDay(dayBounds(request.at)[0]);
    pendingSeek.current = request.at;
    clock.seek(request.at);
    onRequestDone();
  }, [request, onRequestDone, clock]);

  const pauseAll = useCallback(() => {
    for (const p of players.current.values()) if (!p.video.paused) p.video.pause();
  }, []);

  const setClockPlaying = useCallback(
    (p: boolean) => {
      if (p) clock.play();
      else {
        clock.pause();
        pauseAll();
      }
      setPlaying(clock.playing);
    },
    [clock, pauseAll],
  );

  const resetDay = useCallback((d: number) => {
    dayRef.current = d;
    loadedDay.current = null;
    setDay(d);
    setTimelines({});
    setWant({});
    setGaps({});
  }, []);

  /** Steer every player to the clock; called on a timer and after seeks. */
  const steerAll = useCallback(() => {
    const list = camsRef.current;
    const pos = clock.position;
    if (pos !== null) {
      // The playhead crossed midnight (or was scrubbed there): load that day.
      const d = dayBounds(pos)[0];
      if (d !== dayRef.current) {
        pendingSeek.current = pos;
        resetDay(d);
        setWall(pos);
        return;
      }
    }
    if (!list.length || loadedDay.current !== dayRef.current) return;
    // WAVE24(D2): each camera plays from its own hour-either-side window. Fetch the
    // next one when the playhead nears an end; wait for it when the playhead is outside.
    if (pos !== null) {
      let waiting = false;
      for (const c of list) {
        const w = playsRef.current[c.id];
        if (!w || needsNextWindow(w, pos, Date.now())) loadPlay(c.id, pos);
        if (!w || pos < w.from || pos >= w.to) waiting = true;
      }
      if (waiting) return;
    }
    // Follow a camera that is actually playing (its decoder is the truth).
    if (clock.playing) {
      for (const c of list) {
        const p = players.current.get(c.id);
        if (!p || p.video.paused || p.video.seeking || p.video.readyState < 3) continue;
        if (clock.follow(wallAt(spansOf(c.id), p.recorderId, p.video.currentTime, c.recorderId))) break;
      }
    }
    const t = clock.settle(list.map((c) => spansOf(c.id)));
    if (t === null) {
      if (clock.position !== null) {
        setClockPlaying(false);
        setWall(clock.position);
      }
      return;
    }
    if (t > Date.now() + 1000) {
      clock.seek(Date.now());
      setClockPlaying(false);
    }
    setWall(t);
    const nextWant: Record<string, string | null> = {};
    const gapNow: Record<string, boolean> = {};
    for (const c of list) {
      const spans = spansOf(c.id);
      const p = players.current.get(c.id);
      const s = steer(spans, t, { loaded: p?.recorderId ?? null, current: p?.video.currentTime ?? 0, paused: p?.video.paused ?? true }, clock.playing, c.recorderId);
      gapNow[c.id] = s.kind === "gap";
      if (s.kind === "gap") {
        if (p && !p.video.paused) p.video.pause();
        continue;
      }
      if (s.kind === "load") {
        nextWant[c.id] = s.recorderId;
        continue;
      }
      if (!p) continue;
      const v = p.video;
      const mediaRate = Math.min(clock.rate, maxMediaRate);
      if (v.playbackRate !== mediaRate) {
        try {
          v.playbackRate = mediaRate;
        } catch {
          /* rate not supported by this browser */
        }
      }
      if ((s.kind === "seek" || s.kind === "play") && Number.isFinite(v.duration) && s.media > v.duration - 0.25) {
        // Footage newer than the loaded playlist (today): read it again, at most every 10 s.
        const last = reloadedAt.current.get(c.id) ?? 0;
        if (Date.now() - last > 10_000) {
          reloadedAt.current.set(c.id, Date.now());
          loadPlay(c.id, t, true);
        }
        continue;
      }
      if (s.kind === "seek") {
        v.currentTime = s.media;
        if (clock.playing) v.play().catch(() => {});
      } else if (s.kind === "play") {
        if (Math.abs(v.currentTime - s.media) > 0.3) v.currentTime = s.media;
        v.play().catch(() => {});
      }
    }
    setWant((w) => {
      const merged = { ...w, ...nextWant };
      return Object.keys(nextWant).some((k) => w[k] !== nextWant[k]) ? merged : w;
    });
    setGaps((g) => (list.some((c) => g[c.id] !== gapNow[c.id]) ? gapNow : g));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clock, setClockPlaying, resetDay, loadPlay]);

  const seek = useCallback(
    (t: number) => {
      clock.seek(Math.min(t, Date.now()));
      setWall(clock.position);
      steerAll();
    },
    [clock, steerAll],
  );

  // The day's timelines; today's are re-read every 30 s.
  useEffect(() => {
    if (!camIds) {
      setTimelines({});
      return;
    }
    let alive = true;
    const ids = camIds.split(",");
    const read = (initial: boolean) =>
      // Bookmarks ride along with the day's timeline read (no loop of their own); a failure keeps the last list.
      Promise.all(
        ids.map((id) =>
          Promise.all([client.cameras.timeline(id, day, dayEnd), client.bookmarks.list(id, day, dayEnd).catch(() => null)]).then(
            ([tl, bm]) => [id, tl, bm] as const,
          ),
        ),
      )
        .then((list) => {
          if (!alive) return;
          setBookmarks((prev) => Object.fromEntries(list.map(([id, , bm]) => [id, bm ? sortBookmarks(bm) : prev[id] ?? []])));
          const next = Object.fromEntries(list.map(([id, tl]) => [id, tl]));
          setTimelines(next);
          timelinesRef.current = next;
          loadedDay.current = day;
          setError("");
          if (!initial) return;
          const spans = list.flatMap(([, tl]) => tl.spans);
          let start = pendingSeek.current;
          pendingSeek.current = null;
          if (start === null && clock.position !== null && clock.position >= day && clock.position < dayEnd) start = clock.position;
          if (start === null) {
            const today = dayBounds(Date.now())[0] === day;
            start = spans.length ? (today ? Math.max(...spans.map((s) => s.end)) - 30e3 : Math.min(...spans.map((s) => s.start))) : day;
          }
          seek(start);
        })
        .catch((e) => alive && setError(errorMessage(e)));
    void read(true);
    const t = dayBounds(Date.now())[0] === day ? setInterval(() => void read(false), 30_000) : undefined;
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [camIds, day, dayEnd, client, clock, seek, reload]);

  useEffect(() => {
    const t = setInterval(steerAll, 250);
    return () => clearInterval(t);
  }, [steerAll]);

  // Leaving Playback stops the clock (its position is kept for the way back).
  useEffect(
    () => () => {
      if (clock.playing) clock.pause();
    },
    [clock],
  );

  const onPlayer = useCallback(
    (id: string, p: PlayerHandle | null) => {
      if (p) players.current.set(id, p);
      else players.current.delete(id);
      if (p) steerAll();
    },
    [steerAll],
  );

  const step = useCallback((ms: number) => clock.position !== null && seek(clock.position + ms), [clock, seek]);

  const selSpans = selCam ? timelines[selCam.id]?.spans ?? [] : [];
  const gap = useCallback(
    (dir: -1 | 1) => {
      const t = clock.position;
      if (t === null) return;
      const spans = selCam ? timelinesRef.current[selCam.id]?.spans ?? [] : [];
      const to = dir === 1 ? nextGap(spans, t) : prevGap(spans, t);
      if (to !== null) seek(to);
    },
    [clock, seek, selCam],
  );

  const markIn = useCallback(() => clock.position !== null && setSelection((s) => setIn(s, clock.position!)), [clock]);
  const markOut = useCallback(() => clock.position !== null && setSelection((s) => setOut(s, clock.position!)), [clock]);

  // Keyboard: space, ←/→ step (shift: 10 s), [ ] in/out, B bookmark, L live.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement)?.tagName;
      if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key === " ") {
        e.preventDefault();
        if (clock.position !== null) setClockPlaying(!clock.playing);
      } else if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        e.preventDefault();
        step((e.key === "ArrowRight" ? 1 : -1) * (e.shiftKey ? 10e3 : 1e3));
      } else if (e.key === "[") markIn();
      else if (e.key === "]") markOut();
      else if (e.key === "l" || e.key === "L") onLive();
      else if ((e.key === "b" || e.key === "B") && clock.position !== null && selCam) {
        e.preventDefault();
        setAdding(clock.position);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [clock, setClockPlaying, step, markIn, markOut, onLive, selCam]);

  const range = selectionRange(selection);
  async function runExport() {
    if (!range || !selCam) return;
    const ac = new AbortController();
    exportAbort.current = ac;
    setError("");
    setExported("");
    setProgress(0);
    try {
      const files = await exportWindow({
        client,
        camera: selCam,
        spans: selSpans,
        from: range.start,
        to: range.end,
        route: session.route,
        onProgress: (p) => setProgress(p.done / Math.max(1, p.total)),
        signal: ac.signal,
      });
      for (const f of files) saveBlob(f.name, f.blob);
      setExported(files.length === 1 ? `Saved ${files[0].name}` : `Saved ${files.length} files (the range spans several recordings).`);
    } catch (e) {
      if ((e as Error)?.name !== "AbortError") setError(errorMessage(e));
    } finally {
      setProgress(null);
      exportAbort.current = null;
    }
  }

  const camBookmarks = useMemo(() => (selCam ? inWindow(bookmarks[selCam.id] ?? [], day, dayEnd) : []), [bookmarks, selCam, day, dayEnd]);
  const otherCams = cams.filter((c) => c.id !== selCam?.id);
  const anyLane = useMemo(() => mergeSpans(otherCams.flatMap((c) => timelines[c.id]?.spans ?? [])), [otherCams.map((c) => c.id).join(","), timelines]); // eslint-disable-line react-hooks/exhaustive-deps
  const tl = selCam ? timelines[selCam.id] : undefined;
  // WAVE22(C): merged so full-res wins where a low-res span overlaps it; low-res keeps quality "low" (drawn lighter).
  const laneSpans = useMemo(() => mergeSpans(tl?.spans ?? []), [tl]);
  const lane = selCam ? { name: selCam.name, spans: laneSpans, pending: tl?.pending ?? [], markers: tl ? markersFromV2(selCam.id, tl) : [], edge: edgeRanges(tl?.spans ?? []) } : null;
  // "Recover from camera": the camera keeps its own recordings (ONVIF Profile G) and the user may export.
  const canRecover = !!selCam?.edgeRecording && can("exportEvidence");
  const recoverAt = canRecover ? recoverTarget(range, [...(tl?.spans ?? []), ...(tl?.pending ?? [])], wall, { start: day, end: dayEnd }, Date.now()) : null;
  const edgeHere = !!selCam && edge.job?.cameraId === selCam.id;

  return (
    <>
      {error && <div className="error op-pb-error">{error}</div>}
      <CameraGrid
        layout={layout}
        selected={selected}
        onSelect={onSelect}
        onAssign={onAssign}
        onSwap={onSwap}
        onClear={onClear}
        renderCell={(c, i, tools) => {
          const at = wall !== null ? locate(timelines[c.id]?.spans ?? [], wall, c.recorderId) : null;
          return (
            <PlaybackTile
              camera={c}
              recorderId={plays[c.id] ? (want[c.id] ?? at?.recorderId ?? null) : null}
              from={plays[c.id]?.from ?? day}
              to={plays[c.id]?.to ?? dayEnd}
              version={versions[c.id] ?? 0}
              gap={!!gaps[c.id]}
              muted={!sound || i !== selected}
              onPlayer={onPlayer}
            >
              {tools}
            </PlaybackTile>
          );
        }}
      />
      <BookmarkStrip
        client={client}
        cameraId={selCam?.id ?? null}
        cameraName={selCam?.name ?? ""}
        bookmarks={camBookmarks}
        adding={adding}
        onAdding={setAdding}
        playhead={wall}
        selection={selection}
        onSeek={seek}
        onUpsert={(b) => setBookmarks((m) => ({ ...m, [b.cameraId]: upsertBookmark(m[b.cameraId] ?? [], b) }))}
        onRemove={(id) => setBookmarks((m) => Object.fromEntries(Object.entries(m).map(([k, v]) => [k, removeBookmark(v, id)])))}
        onExport={
          can("exportEvidence")
            ? (b) => {
                setSelection({ inAt: b.atMs, outAt: b.atMs + b.durationMs });
                data.setExportDraft({ cameraId: b.cameraId, from: b.atMs, to: b.atMs + b.durationMs });
              }
            : undefined
        }
        onError={setError}
      />
      <TimelineBar
        clock={clock}
        wall={wall}
        playing={playing}
        rate={rate}
        span={span}
        onSpan={setSpan}
        lane={lane}
        anyLane={anyLane}
        bookmarks={camBookmarks}
        selection={selection}
        onMarkIn={markIn}
        onMarkOut={markOut}
        onClearSelection={() => setSelection(emptySelection)}
        onSeek={seek}
        onPlayPause={() => setClockPlaying(!clock.playing)}
        onStep={step}
        onGap={gap}
        onRate={(r) => {
          clock.setRate(r);
          setRate(r);
        }}
        onLive={onLive}
        sound={sound}
        onSound={setSound}
        exporter={
          can("exportEvidence")
            ? { blocker: exportBlocker(selection, maxExportMs), progress, message: exported || (exportReencodes(selCam?.masks) ? "Privacy masks are on: the clip is re-encoded, so it takes longer." : ""), onExport: () => void runExport(), onCancel: () => exportAbort.current?.abort(), onLock: () => range && selCam && data.setExportDraft({ cameraId: selCam.id, from: range.start, to: range.end }) }
            : undefined
        }
        recover={
          canRecover && selCam
            ? {
                title: edge.running && !edgeHere ? "A recovery is running for another camera." : recoverTitle(recoverAt),
                disabled: !recoverAt || edge.running,
                running: edge.running && edgeHere,
                message: edgeHere ? edge.message : "",
                onRecover: () => recoverAt && void edge.start(selCam.id, recoverAt),
                onCancel: () => void edge.cancel(),
              }
            : undefined
        }
      />
    </>
  );
}
