import { useCallback, useEffect, useRef, useState } from "react";
import HlsVideo from "./HlsVideo";
import type { Transport } from "./webrtc";

/** Why a WebRTC attempt gave up. `permanent` = retrying won't help (codec, server off). */
export interface WebRTCFailure {
  reason: string;
  permanent: boolean;
}

const firstFrameTimeoutMs = 4000; // after signaling: no decoded video → give up
const gatherTimeoutMs = 1500; // send the offer with what ICE found by then
const maxReconnects = 3; // consecutive failed reconnects before giving up

/**
 * Low-latency live view over WebRTC (WHEP via Sentinel's /webrtc proxy, or
 * `whepUrl`: a video-token URL `{baseUrl}/webrtc/{camera}/whep`).
 * Receive-only, video-only (tiles are muted, and leaving audio out makes
 * MediaMTX refuse outright — instead of sending audio alone — when the
 * browser can't take the camera's video codec, e.g. H.265).
 */
export default function WebRTCVideo({
  cameraId,
  whepUrl,
  onPlaying,
  onFail,
}: {
  cameraId: string;
  /** WHEP endpoint; default the dashboard's cookie-authenticated `/webrtc/{camera}/whep`. Read when a session starts. */
  whepUrl?: string;
  onPlaying?: (playing: boolean) => void;
  onFail: (f: WebRTCFailure) => void;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const [attempt, setAttempt] = useState(0);
  const failures = useRef(0); // consecutive attempts that never played
  const onFailRef = useRef(onFail);
  onFailRef.current = onFail;
  const whepRef = useRef(whepUrl);
  whepRef.current = whepUrl;

  useEffect(() => {
    const video = ref.current;
    if (!video || typeof RTCPeerConnection === "undefined") {
      onFailRef.current({ reason: "This browser doesn't support WebRTC", permanent: true });
      return;
    }
    let closed = false;
    let played = false;
    let session = ""; // WHEP session URL (Location)
    let etag = "";
    let frameTimer: ReturnType<typeof setTimeout> | undefined;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let disconnectTimer: ReturnType<typeof setTimeout> | undefined;
    const pending: RTCIceCandidate[] = [];
    const endpoint = whepRef.current || `/webrtc/${encodeURIComponent(cameraId)}/whep`;
    // The session Location is a path on the server that answered the offer.
    const resolve = (loc: string) => (loc ? new URL(loc, new URL(endpoint, window.location.href)).toString() : "");

    const pc = new RTCPeerConnection({ iceServers: [] }); // no third-party STUN
    pc.addTransceiver("video", { direction: "recvonly" });

    const fail = (f: WebRTCFailure) => {
      if (closed) return;
      teardown();
      onPlaying?.(false);
      failures.current += 1;
      if (f.permanent || failures.current > maxReconnects) {
        onFailRef.current(f);
      } else {
        retryTimer = setTimeout(() => setAttempt((n) => n + 1), Math.min(1000 * 2 ** failures.current, 8000));
      }
    };

    const markPlaying = () => {
      if (closed || played || !video.videoWidth) return;
      played = true;
      failures.current = 0;
      clearTimeout(frameTimer);
      onPlaying?.(true);
    };
    const onVideoPlaying = () => markPlaying();
    const onResize = () => markPlaying();
    video.addEventListener("playing", onVideoPlaying);
    video.addEventListener("resize", onResize);

    pc.ontrack = (ev) => {
      if (ev.track.kind !== "video") return;
      video.srcObject = ev.streams[0] ?? new MediaStream([ev.track]);
      video.play().catch(() => {});
    };
    pc.onicecandidate = (ev) => {
      if (!ev.candidate || !ev.candidate.candidate) return;
      if (session) sendCandidates([ev.candidate]);
      else pending.push(ev.candidate);
    };
    pc.onconnectionstatechange = () => {
      const st = pc.connectionState;
      clearTimeout(disconnectTimer);
      if (st === "failed") {
        fail({ reason: played ? "Connection lost" : "Couldn't reach the server's WebRTC port", permanent: false });
      } else if (st === "disconnected") {
        // Often recovers by itself (Wi-Fi blip); give it a moment.
        disconnectTimer = setTimeout(() => fail({ reason: "Connection lost", permanent: false }), 3000);
      } else if (st === "closed" && played) {
        fail({ reason: "Connection closed", permanent: false });
      }
    };

    const sendCandidates = (cands: RTCIceCandidate[]) => {
      const frag = sdpFragment(pc.localDescription?.sdp ?? "", cands);
      if (!frag || !session) return;
      fetch(session, {
        method: "PATCH",
        credentials: "same-origin",
        headers: { "Content-Type": "application/trickle-ice-sdpfrag", "If-Match": etag || "*" },
        body: frag,
      }).catch(() => {});
    };

    const teardown = () => {
      if (closed) return;
      closed = true;
      clearTimeout(frameTimer);
      clearTimeout(disconnectTimer);
      pc.ontrack = pc.onicecandidate = pc.onconnectionstatechange = null;
      if (session) {
        // keepalive: still sent when the page is closing. Sent before
        // close() so MediaMTX usually still has the session (a 404 is harmless).
        fetch(session, { method: "DELETE", credentials: "same-origin", keepalive: true }).catch(() => {});
      }
      pc.close();
    };

    (async () => {
      try {
        await pc.setLocalDescription(await pc.createOffer());
        await waitForGathering(pc, gatherTimeoutMs);
        if (closed) return;
        const res = await fetch(endpoint, {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/sdp" },
          body: pc.localDescription?.sdp ?? "",
        });
        const text = await res.text();
        if (closed) {
          // Unmounted mid-request: clean up the session the server just made.
          const loc = resolve(res.headers.get("Location") ?? "");
          if (res.status === 201 && loc) fetch(loc, { method: "DELETE", credentials: "same-origin", keepalive: true }).catch(() => {});
          return;
        }
        if (res.status !== 201) {
          fail(classifyRefusal(res.status, text));
          return;
        }
        session = resolve(res.headers.get("Location") ?? "");
        etag = res.headers.get("ETag") ?? "";
        await pc.setRemoteDescription({ type: "answer", sdp: text });
        if (pending.length) sendCandidates(pending.splice(0));
        frameTimer = setTimeout(async () => {
          if (closed || played) return;
          // Last check: frames decoded but no event yet?
          if ((await framesDecoded(pc)) > 0 && video.videoWidth) return markPlaying();
          fail({
            reason: pc.connectionState === "connected" ? "No video over WebRTC (the browser may not decode this camera's codec)" : "Couldn't reach the server's WebRTC port",
            // Connected but nothing decodes = codec; retrying won't fix it.
            permanent: pc.connectionState === "connected",
          });
        }, firstFrameTimeoutMs);
      } catch (e) {
        fail({ reason: (e as Error).message || "WebRTC failed", permanent: false });
      }
    })();

    return () => {
      clearTimeout(retryTimer);
      video.removeEventListener("playing", onVideoPlaying);
      video.removeEventListener("resize", onResize);
      teardown();
      video.srcObject = null;
    };
  }, [cameraId, attempt, onPlaying]);

  return <video ref={ref} muted playsInline autoPlay />;
}

function classifyRefusal(status: number, body: string): WebRTCFailure {
  let msg = "";
  try {
    const err = JSON.parse(body)?.error;
    msg = typeof err === "string" ? err : (err?.message ?? "");
  } catch {
    msg = body.slice(0, 200);
  }
  if (status === 401) return { reason: msg || "Signed out", permanent: true };
  if (status === 503) return { reason: msg || "WebRTC is off on this server", permanent: true };
  if (status === 400 && /codec/i.test(msg)) return { reason: "This browser can't play the camera's codec over WebRTC", permanent: true };
  if (status === 400 || status === 415 || status === 403) return { reason: msg || `WebRTC refused (${status})`, permanent: true };
  // 404 no stream yet (camera starting), 429, 5xx: try again.
  return { reason: msg || `WebRTC unavailable (${status})`, permanent: false };
}

function waitForGathering(pc: RTCPeerConnection, ms: number): Promise<void> {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(t);
      pc.removeEventListener("icegatheringstatechange", check);
      resolve();
    };
    const check = () => pc.iceGatheringState === "complete" && done();
    const t = setTimeout(done, ms);
    pc.addEventListener("icegatheringstatechange", check);
  });
}

async function framesDecoded(pc: RTCPeerConnection): Promise<number> {
  try {
    let n = 0;
    (await pc.getStats()).forEach((r) => {
      if (r.type === "inbound-rtp" && r.kind === "video") n += r.framesDecoded ?? 0;
    });
    return n;
  } catch {
    return 0;
  }
}

/** Trickle-ICE fragment (RFC 8840) for candidates found after the offer went out. */
function sdpFragment(localSdp: string, cands: RTCIceCandidate[]): string {
  const ufrag = /a=ice-ufrag:(.*)\r?\n/.exec(localSdp)?.[1];
  const pwd = /a=ice-pwd:(.*)\r?\n/.exec(localSdp)?.[1];
  if (!ufrag || !pwd || !cands.length) return "";
  let frag = `a=ice-ufrag:${ufrag}\r\na=ice-pwd:${pwd}\r\n`;
  const mids = new Map<string, string[]>();
  for (const c of cands) {
    const mid = c.sdpMid ?? "0";
    mids.set(mid, [...(mids.get(mid) ?? []), c.candidate]);
  }
  for (const [mid, lines] of mids) {
    frag += `m=video 9 UDP/TLS/RTP/SAVPF 0\r\na=mid:${mid}\r\n`;
    for (const l of lines) frag += `a=${l}\r\n`;
  }
  return frag;
}

// Cameras whose WebRTC attempt failed for good this page session (Auto mode
// then goes straight to HLS for them). Cleared when the viewer changes mode.
const hlsOnly = new Map<string, string>();
export function resetWebRTCFallbacks() {
  hlsOnly.clear();
}

/**
 * The Live page's video: WebRTC when chosen (or Auto), HLS otherwise, and
 * HLS automatically whenever WebRTC can't play this camera in Auto mode.
 */
export function LiveVideo({
  cameraId,
  hlsURL,
  whepUrl,
  hlsKey,
  rewriteUrl,
  transport,
  webrtcEnabled,
  onPlaying,
  onTransport,
}: {
  cameraId: string;
  hlsURL: string;
  /** WHEP endpoint (video-token URL); default the cookie-authenticated proxy. */
  whepUrl?: string;
  /** Restart key for HLS when its URL changes only by token (see HlsVideo). */
  hlsKey?: string;
  rewriteUrl?: (url: string) => string;
  transport: Transport;
  webrtcEnabled: boolean;
  onPlaying?: (playing: boolean) => void;
  /** What is in use, plus why WebRTC was dropped or is failing (if it is). */
  onTransport?: (t: "webrtc" | "hls", note?: string) => void;
}) {
  const [fallback, setFallback] = useState<string | null>(() => (transport === "auto" ? hlsOnly.get(cameraId) ?? null : null));
  const [error, setError] = useState(""); // forced-WebRTC failure, shown while waiting to retry
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    setFallback(transport === "auto" ? hlsOnly.get(cameraId) ?? null : null);
    setError("");
  }, [cameraId, transport]);

  useEffect(() => {
    if (!error) return;
    const t = setTimeout(() => {
      setError("");
      setRetry((n) => n + 1);
    }, 10000);
    return () => clearTimeout(t);
  }, [error]);

  const useWebRTC = webrtcEnabled && transport !== "hls" && fallback === null;
  useEffect(() => {
    onTransport?.(useWebRTC ? "webrtc" : "hls", (useWebRTC ? error : fallback) || undefined);
  }, [useWebRTC, fallback, error, onTransport]);

  const onFail = useCallback(
    (f: WebRTCFailure) => {
      if (transport === "auto") {
        hlsOnly.set(cameraId, f.reason);
        setFallback(f.reason);
      } else {
        setError(f.reason); // WebRTC forced: say why, try again shortly
      }
    },
    [cameraId, transport],
  );

  if (!useWebRTC) return <HlsVideo src={hlsURL} streamKey={hlsKey} rewriteUrl={rewriteUrl} onPlaying={onPlaying} />;
  if (error) return null;
  return <WebRTCVideo key={`${cameraId}-${retry}`} cameraId={cameraId} whepUrl={whepUrl} onPlaying={onPlaying} onFail={onFail} />;
}
