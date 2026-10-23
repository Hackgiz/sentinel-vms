// WAVE22 addendum: "Find streams" for a saved camera. Asks the camera's
// recording server for its streams (ONVIF, else guessed addresses checked
// with RTSP DESCRIBE), shows a picker with the recommended second stream,
// and offers to make a too-small second stream 1280×720.
import { useCallback, useState } from "react";
import { ApiError, errorMessage, type FoundStream, type StreamDiscovery, type StreamsDiscoverRequest } from "../shared/api";
import { v2 } from "./client";

export type FindState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "resizing"; result: StreamDiscovery }
  | { kind: "error"; message: string; authFailed: boolean; result?: StreamDiscovery }
  | { kind: "done"; result: StreamDiscovery };

/** The size a too-small second stream is raised to. */
export const RESIZE_TO = { width: 1280, height: 720 } as const;

/** Streams worth offering as the second one: everything but the main stream. */
export function candidateStreams(result: StreamDiscovery, mainUrl: string): FoundStream[] {
  return result.streams.filter((s) => s.role !== "main" && s.url !== mainUrl.trim());
}

export function findError(e: unknown): { message: string; authFailed: boolean } {
  const authFailed = e instanceof ApiError && e.code === "camera_auth";
  if (authFailed) return { authFailed, message: "The camera rejected the username or password. Check them above, then try again." };
  return { authFailed, message: errorMessage(e) };
}

/** Discovery and resize for one saved camera. `login` is the editor's typed login, if any. */
export function useFindStreams(cameraId: string | undefined, login: () => StreamsDiscoverRequest) {
  const [state, setState] = useState<FindState>({ kind: "idle" });
  const find = useCallback(async () => {
    if (!cameraId) return;
    setState({ kind: "loading" });
    try {
      setState({ kind: "done", result: await v2.cameras.discoverStreams(cameraId, login()) });
    } catch (e) {
      setState({ kind: "error", ...findError(e) });
    }
  }, [cameraId, login]);
  const resize = useCallback(
    async (result: StreamDiscovery) => {
      const small = result.tooSmallSub;
      if (!cameraId || !small) return;
      setState({ kind: "resizing", result });
      try {
        await v2.cameras.resizeStream(cameraId, { encoderToken: small.encoderToken, ...RESIZE_TO });
      } catch (e) {
        setState({ kind: "error", ...findError(e), result });
        return;
      }
      await find(); // refill with what the camera reports now
    },
    [cameraId, find],
  );
  const close = useCallback(() => setState({ kind: "idle" }), []);
  return { state, find, resize, close };
}

export function StreamPickerView({
  state,
  mainUrl,
  current,
  onUse,
  onResize,
  onRetry,
  onClose,
}: {
  state: FindState;
  mainUrl: string;
  /** The second stream currently in the form. */
  current: string;
  onUse: (url: string) => void;
  onResize: (result: StreamDiscovery) => void;
  onRetry: () => void;
  onClose: () => void;
}) {
  if (state.kind === "idle") return null;
  if (state.kind === "loading") {
    return (
      <div className="stream-picker" role="status">
        <p className="muted small">Looking for this camera's streams… This can take up to half a minute.</p>
      </div>
    );
  }
  const result = state.result;
  const cands = result ? candidateStreams(result, mainUrl) : [];
  const rec = result?.recommendedSub ?? null;
  const small = result?.tooSmallSub ?? null;
  return (
    <div className="stream-picker">
      {state.kind === "error" && (
        <div className="notice warn" role="alert">
          {state.message}{" "}
          <button type="button" className="btn small" onClick={onRetry}>
            Try again
          </button>
        </div>
      )}
      {result && small && (
        <div className="notice">
          This camera's second stream is only {small.width}×{small.height}.{" "}
          <button type="button" className="btn small primary" disabled={state.kind === "resizing"} onClick={() => onResize(result)}>
            {state.kind === "resizing" ? "Changing…" : `Make it ${RESIZE_TO.width}×${RESIZE_TO.height}`}
          </button>
        </div>
      )}
      {result && cands.length === 0 && <p className="muted small">No second stream found. If you know its address, type it above.</p>}
      {cands.length > 0 && (
        <ul className="stream-list">
          {cands.map((s) => (
            <li key={s.url} className={s.url === current ? "on" : undefined}>
              <span className="stream-what">
                <strong>{s.label}</strong>
                {s.url === rec && <span className="badge green">Recommended</span>}
                {s.source === "guess" && <span className="badge amber">Guessed</span>}
                {s.url === current && <span className="badge blue">In use</span>}
                <span className="mono">{s.url}</span>
              </span>
              <button type="button" className="btn small" disabled={s.url === current} onClick={() => onUse(s.url)}>
                Use
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="stream-actions">
        {rec && rec !== current && (
          <button type="button" className="btn small primary" onClick={() => onUse(rec)}>
            Use recommended
          </button>
        )}
        <button type="button" className="btn small" onClick={onClose}>
          Close
        </button>
      </div>
    </div>
  );
}
