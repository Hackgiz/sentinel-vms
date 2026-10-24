import { useCallback, useState } from "react";
import type { Camera } from "../api";
import { v2 } from "../admin/client";
import PTZOverlay from "../shared/PTZOverlay";
import { PhoneTileBadge } from "../shared/PhoneTileBadge";
import { isPhone, NEEDS_CLOUD, PHONE_NOT_STREAMING } from "../shared/phoneCameras";
import { LiveVideo } from "../shared/WebRTCVideo";
import type { Transport } from "../shared/webrtc";
import { tilePtz, usePtzState } from "./livePtz";

/**
 * A live camera tile (WebRTC, or HLS when WebRTC can't play) with name, status and an expand button.
 * PTZ cameras get a PTZ badge, and the PTZ controls while the tile is open (`ptzOpen`).
 */
export function CameraTile({
  camera,
  focused,
  onToggle,
  transport,
  webrtcEnabled,
  canPtz = false,
  ptzOpen = false,
  onPtz,
}: {
  camera: Camera;
  focused: boolean;
  onToggle: () => void;
  transport: Transport;
  webrtcEnabled: boolean | null;
  /** The user's `controlPtz` right. */
  canPtz?: boolean;
  /** Show the PTZ controls (focus view, or the tile selected in the grid). */
  ptzOpen?: boolean;
  /** The PTZ badge was clicked: select this tile so its controls open. */
  onPtz?: () => void;
}) {
  const [playing, setPlaying] = useState(false);
  const onPlaying = useCallback((p: boolean) => setPlaying(p), []);
  const [via, setVia] = useState<{ t: "webrtc" | "hls"; note?: string } | null>(null);
  const onTransport = useCallback((t: "webrtc" | "hls", note?: string) => setVia({ t, note }), []);
  const ptzState = usePtzState(v2, camera.id, canPtz && camera.online);
  const ptz = tilePtz({ state: ptzState, online: camera.online, canControl: canPtz, open: ptzOpen });
  return (
    <div className={`tile ${focused ? "focused" : ""}`} onDoubleClick={onToggle} title={focused ? "Double-click to return to the grid" : "Double-click to open the viewer"}>
      {camera.online && webrtcEnabled !== null && (
        <LiveVideo cameraId={camera.id} hlsURL={camera.liveURL} transport={transport} webrtcEnabled={webrtcEnabled} onPlaying={onPlaying} onTransport={onTransport} />
      )}
      {!playing && (
        <div className="placeholder">
          <div>
            <span className="big">{camera.online ? "◌" : "⊘"}</span>
            {camera.online ? "Connecting…" : isPhone(camera) ? (camera.status === NEEDS_CLOUD ? NEEDS_CLOUD : PHONE_NOT_STREAMING) : camera.status}
            {camera.online && via?.note && <small style={{ display: "block", marginTop: 4 }}>{via.note}</small>}
          </div>
        </div>
      )}
      <div className="overlay">
        <div className="grow">
          <div className="name">{camera.name}</div>
          {camera.location && <div className="loc">{camera.location}</div>}
        </div>
        {isPhone(camera) && <PhoneTileBadge />}
        {ptz.badge && (
          <button
            type="button"
            className="badge ptz"
            onClick={(e) => (e.stopPropagation(), onPtz?.())}
            onDoubleClick={(e) => e.stopPropagation()}
            title="Pan, tilt and zoom: show the controls"
            aria-label={`Show pan, tilt and zoom controls for ${camera.name}`}
          >
            PTZ
          </button>
        )}
        {camera.recording && camera.online && (
          <span className="badge rec">
            <span className="dot" /> REC
          </span>
        )}
        {playing && via && (
          <span className="badge rec" title={via.note ? `HLS: ${via.note}` : undefined}>
            {via.t === "webrtc" ? "WebRTC" : "HLS"}
          </span>
        )}
        <span className={`badge ${camera.online ? "green" : "red"}`}>{camera.online ? "Online" : "Offline"}</span>
        {!focused && (
          <button className="tile-expand" onClick={(e) => (e.stopPropagation(), onToggle())} onDoubleClick={(e) => e.stopPropagation()} title="Open viewer with timeline" aria-label={`Open ${camera.name} in the viewer`}>
            ⤢
          </button>
        )}
      </div>
      {ptz.controls && <PTZOverlay client={v2} cameraId={camera.id} keyboard online={camera.online} allowed={canPtz} initial={ptzState} className="dash-ptz" />}
    </div>
  );
}
