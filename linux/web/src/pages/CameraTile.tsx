import { useCallback, useState } from "react";
import type { Camera } from "../api";
import { LiveVideo } from "../shared/WebRTCVideo";
import type { Transport } from "../shared/webrtc";

/** A live camera tile (WebRTC, or HLS when WebRTC can't play) with name, status and an expand button. */
export function CameraTile({
  camera,
  focused,
  onToggle,
  transport,
  webrtcEnabled,
}: {
  camera: Camera;
  focused: boolean;
  onToggle: () => void;
  transport: Transport;
  webrtcEnabled: boolean | null;
}) {
  const [playing, setPlaying] = useState(false);
  const onPlaying = useCallback((p: boolean) => setPlaying(p), []);
  const [via, setVia] = useState<{ t: "webrtc" | "hls"; note?: string } | null>(null);
  const onTransport = useCallback((t: "webrtc" | "hls", note?: string) => setVia({ t, note }), []);
  return (
    <div className={`tile ${focused ? "focused" : ""}`} onDoubleClick={onToggle} title={focused ? "Double-click to return to the grid" : "Double-click to open the viewer"}>
      {camera.online && webrtcEnabled !== null && (
        <LiveVideo cameraId={camera.id} hlsURL={camera.liveURL} transport={transport} webrtcEnabled={webrtcEnabled} onPlaying={onPlaying} onTransport={onTransport} />
      )}
      {!playing && (
        <div className="placeholder">
          <div>
            <span className="big">{camera.online ? "◌" : "⊘"}</span>
            {camera.online ? "Connecting…" : camera.status}
            {camera.online && via?.note && <small style={{ display: "block", marginTop: 4 }}>{via.note}</small>}
          </div>
        </div>
      )}
      <div className="overlay">
        <div className="grow">
          <div className="name">{camera.name}</div>
          {camera.location && <div className="loc">{camera.location}</div>}
        </div>
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
    </div>
  );
}
