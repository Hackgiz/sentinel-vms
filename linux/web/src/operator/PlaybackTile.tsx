// One camera in synchronized playback: the day's playback playlist from the
// recording server that has the footage at the playhead (a video token with
// the `playback` right for that server), played by hls.js but never on its
// own: the playback view seeks, plays and pauses it to the shared clock.
import Hls from "hls.js";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { errorMessage, type Camera } from "../shared/api";
import MaskOverlay from "../shared/MaskOverlay";
import { playbackUrl, swapToken, useVideoToken, videoBase } from "../shared/video";
import { useSigned } from "./session";

export interface PlayerHandle {
  video: HTMLVideoElement;
  /** Recording server whose playlist is loaded. */
  recorderId: string;
}

export default function PlaybackTile({
  camera,
  recorderId,
  from,
  to,
  version,
  gap,
  muted,
  onPlayer,
  children,
}: {
  camera: Pick<Camera, "id" | "name"> & Partial<Pick<Camera, "masks" | "status">>;
  /** Which recording server's playlist to load (null: nothing recorded near the playhead). */
  recorderId: string | null;
  from: number;
  to: number;
  /** Bumped to re-read the playlist (today's footage grows). */
  version: number;
  gap: boolean;
  muted: boolean;
  onPlayer: (cameraId: string, p: PlayerHandle | null) => void;
  /** Tile tools (maximize, remove), shown on hover. */
  children?: ReactNode;
}) {
  const { session } = useSigned();
  const ref = useRef<HTMLVideoElement>(null);
  const { token, error } = useVideoToken(session.client, recorderId ? camera.id : null, ["playback"], recorderId ?? undefined);
  const [failed, setFailed] = useState("");
  const latest = useRef("");
  latest.current = token?.token ?? "";
  const base = token ? videoBase(token, session.route, session.client.baseUrl) : "";
  // The player restarts when where the footage comes from changes, not when the token rotates.
  const sourceKey = token ? `${swapToken(base, "-")}|${recorderId}|${from}|${to}|${version}` : "";
  const onPlayerRef = useRef(onPlayer);
  onPlayerRef.current = onPlayer;
  const baseRef = useRef(base);
  baseRef.current = base;

  useEffect(() => {
    const video = ref.current;
    if (!video || !sourceKey || !recorderId) return;
    setFailed("");
    const url = playbackUrl(baseRef.current, camera.id, from, to);
    const rewrite = (u: string) => (latest.current ? swapToken(u, latest.current) : u);
    let hls: Hls | null = null;
    let done = false;
    const ready = () => {
      if (done) return;
      done = true;
      onPlayerRef.current(camera.id, { video, recorderId });
    };
    if (Hls.isSupported()) {
      hls = new Hls({
        autoStartLoad: true,
        startPosition: -1,
        maxBufferLength: 20,
        backBufferLength: 30,
        xhrSetup: (xhr, u) => {
          const next = rewrite(u);
          if (next !== u) xhr.open("GET", next, true);
        },
      });
      hls.on(Hls.Events.MANIFEST_PARSED, ready);
      hls.on(Hls.Events.ERROR, (_e, d) => {
        if (!d.fatal) return;
        if (d.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls?.recoverMediaError();
          return;
        }
        setFailed(d.response?.code === 403 ? "Not allowed to play this camera." : "Couldn't load the recording.");
      });
      hls.loadSource(url);
      hls.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = url;
      video.addEventListener("loadedmetadata", ready, { once: true });
    }
    return () => {
      done = true;
      onPlayerRef.current(camera.id, null);
      hls?.destroy();
      video.removeAttribute("src");
      video.load();
    };
  }, [sourceKey, camera.id, recorderId, from, to]);

  let status = "";
  if (error) status = errorMessage(error);
  else if (failed) status = failed;
  else if (!recorderId || gap) status = "No recording at this time";
  else if (!token) status = "Loading…";

  return (
    <div className="tile op-pb-tile">
      <video ref={ref} muted={muted} playsInline preload="auto" />
      <MaskOverlay camera={camera} />
      {status && (
        <div className="placeholder op-pb-placeholder" role="status">
          {status}
        </div>
      )}
      <div className="overlay">
        <div className="grow">
          <div className="name">{camera.name}</div>
        </div>
      </div>
      {children}
    </div>
  );
}
