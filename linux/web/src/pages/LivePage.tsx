import { useCallback, useEffect, useState } from "react";
import { api, can, type Camera, type User } from "../api";
import { LiveVideo, resetWebRTCFallbacks } from "../shared/WebRTCVideo";
import { loadTransport, saveTransport, transports, useWebRTCEnabled, type Transport } from "../shared/webrtc";

const columnChoices = [1, 2, 3, 4];

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

export default function LivePage({ user }: { user: User }) {
  const { cameras, error } = useCameras();
  const [columns, setColumns] = useState(() => Number(localStorage.getItem("live.columns")) || 2);
  const [focused, setFocused] = useState<string | null>(null);
  const [transport, setTransport] = useState<Transport>(loadTransport);
  const webrtcEnabled = useWebRTCEnabled();
  const chooseTransport = (t: Transport) => {
    resetWebRTCFallbacks();
    saveTransport(t);
    setTransport(t);
  };

  useEffect(() => localStorage.setItem("live.columns", String(columns)), [columns]);

  if (cameras === null) return null;
  const online = cameras.filter((c) => c.online).length;
  const shown = focused ? cameras.filter((c) => c.id === focused) : cameras;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Live</h1>
          <div className="sub">
            {online}/{cameras.length} cameras online
          </div>
        </div>
        {webrtcEnabled && (
          <div className="seg" role="group" aria-label="Video transport" title="Auto: WebRTC (≈0.5 s delay), HLS when WebRTC can't play">
            {transports.map((t) => (
              <button key={t.value} className={t.value === transport ? "on" : ""} onClick={() => chooseTransport(t.value)}>
                {t.label}
              </button>
            ))}
          </div>
        )}
        {focused ? (
          <button className="btn" onClick={() => setFocused(null)}>
            ← All cameras
          </button>
        ) : (
          <div className="seg" role="group" aria-label="Grid size">
            {columnChoices.map((n) => (
              <button key={n} className={n === columns ? "on" : ""} onClick={() => setColumns(n)}>
                {n}×{n}
              </button>
            ))}
          </div>
        )}
      </div>
      {error && <div className="error">{error}</div>}
      {cameras.length === 0 ? (
        <div className="card empty">
          <div className="big">◉</div>
          <h2>No cameras yet</h2>
          <p>{can.manageCameras(user) ? <a href="#/cameras">Add your first camera</a> : "Ask an administrator to add cameras."}</p>
        </div>
      ) : (
        <div className="live-grid" style={{ gridTemplateColumns: `repeat(${focused ? 1 : columns}, minmax(0, 1fr))` }}>
          {shown.map((c) => (
            <CameraTile
              key={c.id}
              camera={c}
              focused={focused === c.id}
              onToggle={() => setFocused(focused === c.id ? null : c.id)}
              transport={transport}
              webrtcEnabled={webrtcEnabled}
            />
          ))}
        </div>
      )}
    </>
  );
}

function CameraTile({
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
    <div className={`tile ${focused ? "focused" : ""}`} onDoubleClick={onToggle} title="Double-click to focus">
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
      </div>
    </div>
  );
}
