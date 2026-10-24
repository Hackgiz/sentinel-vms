import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, type Timeline, type TimelineSpan } from "../api";
import { markersOf, mediaToWall, nextSpanStart, recordedBetween, wallToMedia } from "../playback";
import type { Marker } from "../shared/time";
import { clampSpan } from "../operator/timelineMath";
import {
  dataWindow,
  inWindow,
  isLiveEdge,
  skipTarget,
  stepTime,
  windowAround,
  windowCovers,
  type DataWindow,
  type Skip,
  type View,
} from "./viewerMath";

/** The playlist a camera has loaded, and the spans it was built from. */
export interface PlayWindow {
  from: number;
  to: number;
  spans: TimelineSpan[];
  version: number;
}

const REFRESH_MS = 20000;

export type Engine = ReturnType<typeof usePlaybackEngine>;

/**
 * One shared clock for the cameras on screen. Live mode keeps the playhead
 * pinned to now; playback mode keeps every camera's recording in sync at the
 * playhead. The same engine serves the grid and an expanded camera, so the
 * docked timeline keeps its position when a tile is expanded or collapsed.
 */
export function usePlaybackEngine(ids: string[], selectedId: string | null) {
  const idsKey = ids.join(",");
  const idsRef = useRef<string[]>(ids);
  idsRef.current = ids;

  const [now, setNow] = useState(Date.now());
  const [mode, setModeState] = useState<"live" | "playback">("live");
  const [wall, setWallState] = useState<number | null>(null);
  const [playing, setPlayingState] = useState(false);
  const [rate, setRate] = useState(1);
  const [sound, setSound] = useState(false);
  const [spanMs, setSpanMsState] = useState(3600e3);
  const [win, setWin] = useState<DataWindow | null>(null);
  const [tls, setTls] = useState<Record<string, Timeline>>({});
  const [plays, setPlaysState] = useState<Record<string, PlayWindow>>({});
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const modeRef = useRef(mode);
  const wallRef = useRef<number | null>(null);
  const playingRef = useRef(false);
  const rateRef = useRef(1);
  const playsRef = useRef<Record<string, PlayWindow>>({});
  const tlsRef = useRef(tls);
  const winRef = useRef<DataWindow | null>(null);
  const videos = useRef(new Map<string, HTMLVideoElement>());
  const versionRef = useRef(0);
  const lastTick = useRef(0);
  const seekToken = useRef(0);
  rateRef.current = rate;
  tlsRef.current = tls;
  winRef.current = win;

  const setMode = (m: "live" | "playback") => {
    modeRef.current = m;
    setModeState(m);
  };
  const setWall = (t: number | null) => {
    wallRef.current = t;
    setWallState(t);
  };
  const setPlays = (p: Record<string, PlayWindow>) => {
    playsRef.current = p;
    setPlaysState(p);
  };
  const setPlaying = useCallback((p: boolean) => {
    playingRef.current = p;
    setPlayingState(p);
    lastTick.current = performance.now();
    if (!p) for (const v of videos.current.values()) v.pause();
  }, []);

  // The ruler is centered on the playhead (now while live).
  const center = mode === "live" ? now : (wall ?? now);
  const view: View = useMemo(() => ({ start: center - spanMs / 2, end: center + spanMs / 2 }), [center, spanMs]);
  const viewRef = useRef(view);
  viewRef.current = view;

  // ---- data: timelines for the visible window ------------------------------
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    if (!win || !windowCovers(win, view, now)) setWin(dataWindow(view, now));
  }, [win, view, now]);

  useEffect(() => {
    if (!win || !idsKey) return;
    let alive = true;
    Promise.all(
      idsKey.split(",").map((id) =>
        api
          .timeline(id, Math.round(win.from), Math.round(win.to))
          .then((t) => [id, t] as const)
          .catch((e: Error) => e),
      ),
    ).then((list) => {
      if (!alive) return;
      const ok = list.filter((x): x is readonly [string, Timeline] => Array.isArray(x));
      const bad = list.find((x): x is Error => x instanceof Error);
      setTls((prev) => ({ ...prev, ...Object.fromEntries(ok) }));
      setError(ok.length === 0 && bad ? bad.message : "");
    });
    return () => {
      alive = false;
    };
  }, [win, idsKey]);

  // Windows that touch the present are re-fetched so new footage appears.
  useEffect(() => {
    const t = setInterval(() => {
      const w = winRef.current;
      if (w && w.to >= Date.now() - 2 * 60e3) setWin(dataWindow(viewRef.current, Date.now()));
    }, REFRESH_MS);
    return () => clearInterval(t);
  }, []);

  // Markers and gap skipping follow the selected camera.
  const selTl = selectedId ? tls[selectedId] : undefined;
  const markers: Marker[] = useMemo(() => (selTl && selectedId ? markersOf(selectedId, selTl) : []), [selTl, selectedId]);

  // ---- mode switching -------------------------------------------------------
  const goLive = useCallback(() => {
    seekToken.current++;
    setPlaying(false);
    setMode("live");
    setWall(null);
    setPlays({});
  }, [setPlaying]);

  const mk = (tl: { from: number; to: number; spans: TimelineSpan[] }): PlayWindow => ({ from: tl.from, to: tl.to, spans: tl.spans, version: ++versionRef.current });

  /** Jump every visible camera to recorded footage at t (switching from live if needed). */
  const seek = useCallback(
    async (target: number, opts: { play?: boolean; orLive?: boolean } = {}) => {
      const nowMs = Date.now();
      if (isLiveEdge(target, nowMs)) {
        goLive();
        return;
      }
      let t = Math.min(target, nowMs);
      const ids = idsRef.current;
      const token = ++seekToken.current;
      const adopt: Record<string, PlayWindow> = {};
      const spansAt: Record<string, TimelineSpan[]> = {};
      const need: string[] = [];
      for (const id of ids) {
        const cur = playsRef.current[id];
        const tl = tlsRef.current[id];
        const fresh = tl && inWindow(tl, t) ? tl : null;
        // Keep the loaded playlist when it covers t (or nothing newer does).
        const keep = cur && inWindow(cur, t) && (wallToMedia(cur.spans, t) !== null || !fresh || wallToMedia(fresh.spans, t) === null);
        if (keep) spansAt[id] = cur.spans;
        else if (fresh) {
          adopt[id] = mk(fresh);
          spansAt[id] = fresh.spans;
        } else need.push(id);
      }
      if (need.length) {
        // A day of timelines can take a while on a server that has just
        // started: say so, rather than look like the click did nothing.
        setNotice("Loading recordings…");
        const w = windowAround(t, nowMs);
        try {
          const list = await Promise.all(need.map((id) => api.timeline(id, Math.round(w.from), Math.round(w.to)).then((tl) => [id, tl] as const)));
          if (token !== seekToken.current) return;
          for (const [id, tl] of list) {
            adopt[id] = mk(tl);
            spansAt[id] = tl.spans;
          }
        } catch (e) {
          if (token === seekToken.current) {
            setNotice("");
            setError((e as Error).message);
          }
          return;
        }
      }
      if (opts.orLive && ids.every((id) => !spansAt[id]?.length)) {
        setNotice("No recent recordings, so you are watching live.");
        return;
      }
      if (ids.every((id) => wallToMedia(spansAt[id] ?? [], t) === null)) {
        const next = ids.map((id) => nextSpanStart(spansAt[id] ?? [], t)).filter((x): x is number => x !== null);
        if (next.length && Math.min(...next) - t < 5 * 60e3) t = Math.min(...next); // land on the next recording after a short gap
      }
      const wantPlay = opts.play ?? (modeRef.current === "live" ? true : playingRef.current);
      setNotice("");
      setWall(t);
      setMode("playback");
      setPlaying(wantPlay);
      if (Object.keys(adopt).length) setPlays({ ...playsRef.current, ...adopt });
      for (const id of ids) {
        if (adopt[id]) continue;
        const v = videos.current.get(id);
        const m = wallToMedia(spansAt[id] ?? [], t);
        if (v && m !== null) {
          v.currentTime = m;
          if (wantPlay) v.play().catch(() => {});
        } else v?.pause();
      }
    },
    [goLive, setPlaying],
  );

  /** Switch the whole view to playback a few minutes back (the header toggle). */
  const enterPlayback = useCallback(() => seek(Date.now() - 5 * 60e3, { play: true, orLive: true }), [seek]);

  // Cameras joining the view (expand, collapse) during playback load their footage.
  useEffect(() => {
    if (modeRef.current === "playback" && wallRef.current !== null && idsKey) seek(wallRef.current, { play: playingRef.current });
  }, [idsKey, seek]);

  // ---- the playback clock ---------------------------------------------------
  // Follows a playing camera (the leader), keeps the others within a second of
  // it, skips gaps nobody recorded, picks up new footage and returns to live
  // when playback reaches the present.
  useEffect(() => {
    const timer = setInterval(() => {
      const perf = performance.now();
      const dt = (perf - lastTick.current) * rateRef.current;
      lastTick.current = perf;
      if (modeRef.current !== "playback" || !playingRef.current || wallRef.current === null) return;
      const ids = idsRef.current;
      const ps = playsRef.current;
      if (!ids.some((id) => ps[id])) return; // playlists still loading
      const nowMs = Date.now();
      const expected = wallRef.current + dt;
      let w = expected;
      for (const id of ids) {
        const v = videos.current.get(id);
        const p = ps[id];
        if (!v || !p || v.paused || v.seeking || v.readyState < 3) continue;
        const lw = mediaToWall(p.spans, v.currentTime);
        if (lw === null) continue;
        const othersBetween = lw > expected + 1500 && ids.some((o) => o !== id && recordedBetween(ps[o]?.spans ?? [], expected, lw));
        if (Math.abs(lw - expected) < 1500 || (lw > expected && !othersBetween)) {
          w = lw;
          break;
        }
      }
      if (w >= nowMs - 2000) {
        goLive();
        return;
      }
      if (!ids.some((id) => ps[id] && wallToMedia(ps[id].spans, w) !== null)) {
        // Newer footage may have arrived since the playlists were built.
        const upd: Record<string, PlayWindow> = {};
        for (const id of ids) {
          const fresh = tlsRef.current[id];
          if (fresh && inWindow(fresh, w) && wallToMedia(fresh.spans, w) !== null) upd[id] = mk(fresh);
        }
        if (Object.keys(upd).length) {
          setWall(w);
          setPlays({ ...ps, ...upd });
          return;
        }
        const next = ids.map((id) => nextSpanStart(ps[id]?.spans ?? [], w)).filter((x): x is number => x !== null);
        if (!next.length) {
          if (nowMs - w < 90e3) goLive();
          else setPlaying(false); // past the last recording
          return;
        }
        w = Math.min(...next);
      }
      setWall(w);
      for (const id of ids) {
        const v = videos.current.get(id);
        const p = ps[id];
        if (!v || !p) continue;
        const m = wallToMedia(p.spans, w);
        if (m === null) {
          if (!v.paused) v.pause();
          continue;
        }
        const mediaRate = Math.min(rateRef.current, 16); // browsers cap media playback; beyond it the clock runs ahead and tiles re-seek
        if (v.playbackRate !== mediaRate) v.playbackRate = mediaRate;
        if (v.paused) {
          if (Math.abs(v.currentTime - m) > 0.3) v.currentTime = m;
          v.play().catch(() => {});
        } else if (!v.seeking && Math.abs(v.currentTime - m) > 1.5) {
          v.currentTime = m;
        }
      }
    }, 250);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [goLive, setPlaying]);

  // ---- user actions -----------------------------------------------------------
  const skipFor = (dir: 1 | -1): Skip | null => skipTarget(selTl?.spans ?? [], markers, wallRef.current ?? Date.now(), dir);
  /** Jump to the previous/next gap boundary or event of the selected camera (events get 3 s of pre-roll). */
  const skip = (dir: 1 | -1) => {
    const k = skipFor(dir);
    if (k) seek(k.kind === "event" ? k.at - 3000 : k.at);
  };
  const prevSkip = skipFor(-1);
  const nextSkip = skipFor(1);
  const skipLabels = { prev: prevSkip?.kind ?? "gap", next: nextSkip?.kind ?? "gap" };
  const step = useCallback((delta: number) => seek(stepTime(wallRef.current ?? Date.now(), delta, Date.now())), [seek]);
  const setSpan = (ms: number) => setSpanMsState(clampSpan(ms));
  /** The playhead: now while live, else the shared clock. */
  const getPosition = useCallback(() => (modeRef.current === "live" ? Date.now() : wallRef.current), []);

  const registerVideo = useCallback((id: string, v: HTMLVideoElement | null) => {
    if (v) videos.current.set(id, v);
    else videos.current.delete(id);
  }, []);

  const playUrl = (id: string) => {
    const p = plays[id];
    return p?.spans.length ? `/api/v1/cameras/${id}/playback.m3u8?from=${Math.round(p.from)}&to=${Math.round(p.to)}&v=${p.version}` : "";
  };
  const gapFor = (id: string) => {
    const p = plays[id];
    return mode === "playback" && wall !== null && !!p && wallToMedia(p.spans, wall) === null;
  };
  const startPosition = (id: string) => (wallRef.current === null ? 0 : (wallToMedia(playsRef.current[id]?.spans ?? [], wallRef.current) ?? 0));

  return {
    now,
    mode,
    live: mode === "live",
    wall,
    playing,
    rate,
    sound,
    view,
    spanMs,
    timelines: tls,
    plays,
    markers,
    error,
    notice,
    setRate,
    setSound,
    setPlaying,
    goLive,
    seek,
    enterPlayback,
    step,
    skip,
    skipLabels,
    setSpan,
    getPosition,
    registerVideo,
    playUrl,
    gapFor,
    startPosition,
  };
}
