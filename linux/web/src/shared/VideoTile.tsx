import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { errorMessage, type ApiClient, type Camera } from "./api";
import { liveHlsUrl, swapToken, useVideoToken, videoBase, whepUrl, type VideoRoute } from "./video";
import { LiveVideo } from "./WebRTCVideo";
import type { Transport } from "./webrtc";

/**
 * One live camera on API v2: gets a video token (refreshed before it
 * expires), plays WebRTC over WHEP at `{base}/webrtc/{id}/whep` and falls
 * back to HLS at `{base}/live/{id}/index.m3u8` when WebRTC can't play.
 * A token refresh doesn't restart the stream: HLS requests are rewritten
 * to the newest token, and a WebRTC session outlives the token it began with.
 *
 * Styled by `.tile` (styles.css); put it in a `.live-grid` or size it yourself.
 */
export default function VideoTile({
  client,
  camera,
  transport = "auto",
  webrtc = true,
  route = "auto",
  focused,
  active = true,
  onDoubleClick,
  overlay,
  titleExtra,
  children,
}: {
  client: ApiClient;
  camera: Pick<Camera, "id" | "name"> & Partial<Pick<Camera, "location" | "status" | "recording">>;
  transport?: Transport;
  /** Whether the camera's recording server offers WebRTC (RecordingServer.capabilities.webrtc). */
  webrtc?: boolean;
  route?: VideoRoute;
  focused?: boolean;
  /**
   * false = hold the stream (tile scrolled away, window hidden): no token,
   * no player. Grids with many tiles start and stop tiles with it.
   */
  active?: boolean;
  onDoubleClick?: () => void;
  /** Replaces the default name/status overlay. */
  overlay?: ReactNode;
  /** Extra title-bar content, shown just before the REC badge. */
  titleExtra?: ReactNode;
  children?: ReactNode;
}) {
  const online = camera.status?.online ?? true;
  const { token, error } = useVideoToken(client, online && active ? camera.id : null);
  const [playing, setPlaying] = useState(false);
  const onPlaying = useCallback((p: boolean) => setPlaying(p), []);
  useEffect(() => {
    if (!active) setPlaying(false);
  }, [active]);
  const [via, setVia] = useState<{ t: "webrtc" | "hls"; note?: string } | null>(null);
  const onTransport = useCallback((t: "webrtc" | "hls", note?: string) => setVia({ t, note }), []);

  const base = token ? videoBase(token, route, client.baseUrl) : "";
  const latest = useRef(token?.token ?? "");
  latest.current = token?.token ?? "";
  const rewriteUrl = useCallback((u: string) => (latest.current ? swapToken(u, latest.current) : u), []);
  // The stream restarts only when where it comes from changes, not when the token rotates.
  const hlsKey = useMemo(() => (token ? `${camera.id}@${swapToken(base, "-")}` : ""), [token, camera.id, base]);

  let status = "";
  if (!online) status = camera.status?.message || "Offline";
  else if (!active) status = "Paused";
  else if (error) status = errorMessage(error);
  else if (!playing) status = "Connecting…";

  return (
    <div className={`tile ${focused ? "focused" : ""}`} onDoubleClick={onDoubleClick}>
      {online && active && token && (
        <LiveVideo
          cameraId={camera.id}
          hlsURL={liveHlsUrl(base, camera.id)}
          hlsKey={hlsKey}
          rewriteUrl={rewriteUrl}
          whepUrl={whepUrl(base, camera.id)}
          transport={transport}
          webrtcEnabled={webrtc}
          onPlaying={onPlaying}
          onTransport={onTransport}
        />
      )}
      {status && (
        <div className="placeholder" role="status">
          <div>
            <span className="big">{online && !error ? "◌" : "⊘"}</span>
            {status}
            {online && !error && via?.note && <small style={{ display: "block", marginTop: 4 }}>{via.note}</small>}
          </div>
        </div>
      )}
      {overlay ?? (
        <div className="overlay">
          <div className="grow">
            <div className="name">{camera.name}</div>
            {camera.location && <div className="loc">{camera.location}</div>}
          </div>
          {titleExtra}
          {camera.recording && online && (
            <span className="badge rec">
              <span className="dot" /> REC
            </span>
          )}
          {playing && via && (
            <span className="badge rec" title={via.note ? `HLS: ${via.note}` : undefined}>
              {via.t === "webrtc" ? "WebRTC" : "HLS"}
            </span>
          )}
          <span className={`badge ${online ? "green" : "red"}`}>{online ? "Online" : "Offline"}</span>
        </div>
      )}
      {children}
    </div>
  );
}
