import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, can, type Camera, type User } from "../api";
import TimelineBar from "../operator/TimelineBar";
import { emptySelection, mergeSpans, setIn, setOut, type Selection } from "../operator/timelineMath";
import { markersFromV2 } from "../shared/time";
import { resetWebRTCFallbacks } from "../shared/WebRTCVideo";
import { CameraTile } from "./CameraTile";
import CloudBanner from "./CloudBanner"; // WAVE13(C)
import { ExportPanel, PlaybackTile } from "./playbackParts";
import { usePlaybackEngine, type Engine } from "../viewer/usePlaybackEngine";
import { dayBounds } from "../playback";
import { exportProblem, fmtStamp, lanesFor, parseViewerRoute, selectedCamera, viewerHash } from "../viewer/viewerMath";
import "../viewer/viewer.css";
import { loadTransport, saveTransport, transports, useWebRTCEnabled, type Transport } from "../shared/webrtc";
import { PageHeader } from "../shared/Cards"; // WAVE21

const columnChoices = [1, 2, 3, 4];
/** Most cameras played back in sync at once (each is its own HLS stream). */
const MAX_SYNC = 16;

function useCameras(intervalMs = 5000) {
  const [cameras, setCameras] = useState<Camera[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .cameras()
        .then((c) => alive && (setCameras(c), setError("")))
        .catch((e) => alive && setError(e.message));
    load();
    const t = setInterval(load, intervalMs);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [intervalMs]);
  return { cameras, error };
}

interface StartReq {
  at?: number;
  past?: boolean;
  n: number;
}

/** What an old or deep link asks for: #/live[/cam[/ms]], #/playback..., #/compare. */
function startFromRoute(hash: string): { camera: string | null; start: StartReq | null } {
  const r = parseViewerRoute(hash);
  const legacy = r.page === "playback" || r.page === "compare";
  return { camera: r.camera, start: r.t ? { at: r.t, n: 0 } : legacy ? { past: true, n: 0 } : null };
}

/**
 * The Live page: a camera grid with a timeline docked beneath it. Live mode
 * keeps every tile live and the playhead on now; Playback mode switches all
 * visible tiles to synced recordings at the playhead. Expanding a tile keeps
 * the same timeline and mode for that one camera.
 */
export default function LivePage({ user }: { user: User }) {
  const { cameras, error } = useCameras();
  const [columns, setColumns] = useState(() => Number(localStorage.getItem("live.columns")) || 2);
  const initial = useMemo(() => startFromRoute(window.location.hash), []);
  const [focused, setFocusedState] = useState<string | null>(initial.camera);
  const [startReq, setStartReq] = useState<StartReq | null>(initial.start);
  const [transport, setTransport] = useState<Transport>(loadTransport);
  const [clicked, setClicked] = useState<string | null>(null); // last clicked tile
  const [selection, setSelection] = useState<Selection>(emptySelection);
  const [exporting, setExporting] = useState(false);
  const [exportMsg, setExportMsg] = useState("");
  const webrtcEnabled = useWebRTCEnabled();
  const chooseTransport = (t: Transport) => {
    resetWebRTCFallbacks();
    saveTransport(t);
    setTransport(t);
  };

  const focusCamera = focused && cameras ? (cameras.find((c) => c.id === focused) ?? cameras[0]) : undefined;
  const idsKey = (focusCamera ? [focusCamera] : (cameras ?? []).slice(0, MAX_SYNC)).map((c) => c.id).join(",");
  const ids = useMemo(() => (idsKey ? idsKey.split(",") : []), [idsKey]);
  const selectedId = selectedCamera(focusCamera?.id ?? null, clicked, ids);
  const engine = usePlaybackEngine(ids, selectedId);

  const setFocused = useCallback((id: string | null) => {
    setFocusedState(id);
    // replaceState does not fire hashchange, so the page keeps its state.
    history.replaceState(null, "", viewerHash("live", id));
  }, []);

  // Old links (#/playback, #/compare) land here: normalise the URL, then start playback.
  useEffect(() => {
    if (/^#\/(playback|compare)/.test(window.location.hash)) history.replaceState(null, "", viewerHash("live", initial.camera));
  }, [initial]);

  // Deep links and nav clicks while the page is open.
  useEffect(() => {
    const onHash = () => {
      const { camera, start } = startFromRoute(window.location.hash);
      setFocusedState(camera);
      if (start) {
        setStartReq({ ...start, n: Date.now() });
        history.replaceState(null, "", viewerHash("live", camera));
      }
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // Apply a pending playback request once the cameras are known.
  useEffect(() => {
    if (!startReq || !ids.length) return;
    setStartReq(null);
    if (startReq.at) engine.seek(startReq.at, { play: true });
    else engine.seek(Date.now() - 5 * 60e3, { play: true, orLive: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [startReq, ids]);

  // The shared playhead for the bar (now while live).
  const clock = useMemo(() => ({ get position() { return engine.getPosition(); } }), [engine.getPosition]);

  const markIn = () => {
    const t = engine.getPosition();
    if (t !== null) setSelection((x) => setIn(x, t));
  };
  const markOut = () => {
    const t = engine.getPosition();
    if (t !== null) setSelection((x) => setOut(x, t));
  };
  const clearSelection = () => setSelection(emptySelection);

  // Keyboard: space play/pause, ←/→ 1 s (Shift 10 s), [ ] in/out, L live.
  const keys = useRef({ engine, markIn, markOut });
  keys.current = { engine, markIn, markOut };
  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      const tag = (ev.target as HTMLElement)?.tagName;
      if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || tag === "BUTTON" || ev.ctrlKey || ev.metaKey || ev.altKey) return;
      const k = keys.current;
      if (ev.key === " " && !k.engine.live) {
        ev.preventDefault();
        k.engine.setPlaying(!k.engine.playing);
      } else if (ev.key === "ArrowLeft" || ev.key === "ArrowRight") {
        ev.preventDefault();
        const dir = ev.key === "ArrowRight" ? 1 : -1;
        if (!k.engine.live || dir < 0) k.engine.step(dir * (ev.shiftKey ? 10000 : 1000));
      } else if (ev.key === "[") k.markIn();
      else if (ev.key === "]") k.markOut();
      else if (ev.key === "l" || ev.key === "L") k.engine.goLive();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => localStorage.setItem("live.columns", String(columns)), [columns]);

  if (cameras === null) return null;
  const online = cameras.filter((c) => c.online).length;
  const timelineShown = cameras.length > 0;

  const slot = (c: Camera, index: number, expanded: boolean) => {
    const onToggle = () => setFocused(expanded ? null : c.id);
    const cell = (inner: React.ReactNode) => (
      <div key={c.id} className={`vw-cell ${selectedId === c.id && cameras.length > 1 ? "selected" : ""}`} onClick={() => setClicked(c.id)}>
        {inner}
      </div>
    );
    if (engine.live) {
      return cell(<CameraTile camera={c} focused={expanded} onToggle={onToggle} transport={transport} webrtcEnabled={webrtcEnabled} />);
    }
    if (index >= MAX_SYNC) {
      return cell(
        <div className="tile">
          <div className="placeholder">
            <div>
              <span className="big">▶</span>
              {c.name}
              <small style={{ display: "block", marginTop: 4 }}>Synced playback shows the first {MAX_SYNC} cameras. Expand this one to review it.</small>
            </div>
          </div>
        </div>,
      );
    }
    return cell(
      <div className="vw-slot" onDoubleClick={onToggle}>
        <PlaybackTile
          camera={c}
          url={engine.playUrl(c.id)}
          loading={!engine.plays[c.id] && !engine.timelines[c.id]}
          gap={engine.gapFor(c.id)}
          muted={!engine.sound || index > 0}
          startPosition={() => engine.startPosition(c.id)}
          onVideo={(v) => engine.registerVideo(c.id, v)}
        />
        <button className="tile-expand" onClick={onToggle} title={expanded ? "Back to all cameras" : "Expand this camera"} aria-label={`${expanded ? "Collapse" : "Expand"} ${c.name}`}>
          {expanded ? "⤡" : "⤢"}
        </button>
      </div>,
    );
  };

  // Lanes: the selected camera's recordings, plus one merged lane for the others.
  const selCam = cameras.find((c) => c.id === selectedId);
  const selTl = selectedId ? engine.timelines[selectedId] : undefined;
  const lanes = lanesFor(selectedId, ids, engine.timelines);
  const lane = selCam ? { name: selCam.name, spans: lanes.selected, pending: mergeSpans(selTl?.pending ?? []), markers: selTl ? markersFromV2(selCam.id, { ...selTl, events: selTl.events.map((e) => ({ ...e, createdAt: e.createdAt * 1000 })), alarms: selTl.alarms.map((a) => ({ ...a, createdAt: a.createdAt * 1000 })) }) : [] } : null;
  const range = selection.inAt !== null && selection.outAt !== null && selection.outAt > selection.inAt ? { start: selection.inAt, end: selection.outAt } : null;
  const blocker = range ? exportProblem(selection, lanes.selected, 30 * 60e3) : "Mark an in and an out point first.";
  const downloadClip = async () => {
    if (!selCam || !range) return;
    setExporting(true);
    setExportMsg("");
    try {
      const clip = await api.exportClip(selCam.id, range.start, range.end);
      const a = document.createElement("a");
      a.href = clip.url;
      a.download = clip.filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setExportMsg(`Saved ${clip.filename}`);
    } catch (e) {
      setExportMsg((e as Error).message);
    } finally {
      setExporting(false);
    }
  };

  return (
    <>
      <PageHeader
        title={focusCamera ? focusCamera.name : engine.live ? "Live" : "Playback"}
        subtitle={
          <>
            {focusCamera
              ? engine.live
                ? "Live view — use the timeline below to review recordings"
                : `Recorded footage · ${fmtStamp(engine.wall ?? engine.now)}`
              : `${online}/${cameras.length} cameras online`}
          </>
        }
      >
        <ModeToggle engine={engine} disabled={!timelineShown} />
        {webrtcEnabled && engine.live && (
          <div className="seg" role="group" aria-label="Video transport" title="Auto: WebRTC (≈0.5 s delay), HLS when WebRTC can't play">
            {transports.map((t) => (
              <button key={t.value} className={t.value === transport ? "on" : ""} onClick={() => chooseTransport(t.value)}>
                {t.label}
              </button>
            ))}
          </div>
        )}
        {focusCamera ? (
          <>
            {cameras.length > 1 && (
              <select value={focusCamera.id} onChange={(e) => setFocused(e.target.value)} aria-label="Camera" className="vw-camera">
                {cameras.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
            <button className="btn" onClick={() => setFocused(null)}>
              ← All cameras
            </button>
          </>
        ) : (
          <div className="seg" role="group" aria-label="Grid size">
            {columnChoices.map((n) => (
              <button key={n} className={n === columns ? "on" : ""} onClick={() => setColumns(n)}>
                {n}×{n}
              </button>
            ))}
          </div>
        )}
      </PageHeader>
      {error && <div className="error">{error}</div>}
      {engine.error && <div className="error">{engine.error}</div>}
      {engine.notice && <div className="notice">{engine.notice}</div>}
      {can.changeSettings(user) && <CloudBanner />} {/* WAVE13(C) */}
      {cameras.length === 0 ? (
        <div className="card empty">
          <div className="big">◉</div>
          <h2>No cameras yet</h2>
          <p>{can.manageCameras(user) ? <a href="#/cameras">Add your first camera</a> : "Ask an administrator to add cameras."}</p>
        </div>
      ) : (
        <>
          {focusCamera ? (
            <div className="vw-stage">
              {slot(focusCamera, 0, true)}
              <div className="vw-stamp" aria-live="polite">
                {engine.live ? (
                  <span className="vw-pill live">
                    <span className="dot" /> LIVE
                  </span>
                ) : (
                  <span className="vw-pill">PLAYBACK</span>
                )}
                <span className="mono">{fmtStamp(engine.live ? engine.now : (engine.wall ?? engine.now))}</span>
              </div>
            </div>
          ) : (
            <div className="live-grid" style={{ gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}>
              {cameras.map((c, i) => slot(c, i, false))}
            </div>
          )}
          <div className="vw-dock">
            <TimelineBar
              clock={clock}
              wall={engine.wall}
              playing={engine.playing}
              live={engine.live}
              rate={engine.rate}
              span={engine.spanMs}
              onSpan={engine.setSpan}
              lane={lane}
              anyLane={lanes.any}
              selection={selection}
              onMarkIn={markIn}
              onMarkOut={markOut}
              onClearSelection={clearSelection}
              onSeek={(t) => engine.seek(t)}
              onPlayPause={() => engine.setPlaying(!engine.playing)}
              onStep={engine.step}
              onGap={engine.skip}
              skipLabels={engine.skipLabels}
              onRate={engine.setRate}
              onLive={engine.goLive}
              sound={engine.sound}
              onSound={engine.setSound}
              exporter={can.exportEvidence(user) ? { blocker, progress: exporting ? 0 : null, message: exportMsg, onExport: downloadClip, onCancel: () => {} } : undefined}
            />
          </div>
          {selCam && !engine.live && engine.wall !== null && range && can.exportEvidence(user) && selTl && (
            <ExportPanel
              cams={[selCam]}
              day={dayBounds(engine.wall)[0]}
              wall={engine.wall}
              timelines={{ [selCam.id]: selTl }}
              start={selection.inAt}
              end={selection.outAt}
              setStart={(t) => setSelection((x) => ({ ...x, inAt: t }))}
              setEnd={(t) => setSelection((x) => ({ ...x, outAt: t }))}
            />
          )}
        </>
      )}
    </>
  );
}

/** Live | Playback: switches every visible tile together. */
function ModeToggle({ engine, disabled }: { engine: Engine; disabled: boolean }) {
  return (
    <div className="seg" role="group" aria-label="View mode" title="Live: every tile is live. Playback: every tile plays recordings in sync at the timeline's playhead.">
      <button className={engine.live ? "on" : ""} onClick={engine.goLive} disabled={disabled}>
        Live
      </button>
      <button className={!engine.live ? "on" : ""} onClick={engine.enterPlayback} disabled={disabled}>
        Playback
      </button>
    </div>
  );
}
