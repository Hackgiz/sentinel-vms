import { useEffect, useRef, useState } from "react";
import Hls from "hls.js";
import { api, formatBytes, type Camera, type ClipExport, type Evidence, type Timeline } from "../api";
import { fmtDuration, recordedBetween } from "../playback";
import "./PlaybackPage.css";

// Pieces of the former Playback page, now used by the Live page's synced
// playback: the recording player tile and the clip/evidence export panel.

const MAX_CLIP = 30 * 60e3;

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

export function PlaybackTile({
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

export function ExportPanel({
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
