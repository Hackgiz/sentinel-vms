import { useEffect, useRef, useState } from "react";
import Hls from "hls.js";

/**
 * Muted, auto-playing live HLS view. Safari plays HLS natively; everything
 * else uses hls.js. Retries quietly while the camera (or MediaMTX) comes up.
 *
 * `streamKey` (default: `src`) decides when the player restarts. A caller
 * whose URL changes without the stream changing (a refreshed video token
 * in the path) keeps the key stable and passes `rewriteUrl`, which hls.js
 * applies to every playlist and segment request, so playback continues.
 * Retries always use the latest `src`.
 */
export default function HlsVideo({
  src,
  onPlaying,
  streamKey,
  rewriteUrl,
}: {
  src: string;
  onPlaying?: (playing: boolean) => void;
  streamKey?: string;
  rewriteUrl?: (url: string) => string;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const [attempt, setAttempt] = useState(0);
  const srcRef = useRef(src);
  srcRef.current = src;
  const rewriteRef = useRef(rewriteUrl);
  rewriteRef.current = rewriteUrl;
  const key = streamKey ?? src;

  useEffect(() => {
    const video = ref.current;
    if (!video) return;
    const url = srcRef.current;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const scheduleRetry = () => {
      onPlaying?.(false);
      retry = setTimeout(() => setAttempt((n) => n + 1), 3000);
    };
    const playing = () => onPlaying?.(true);
    video.addEventListener("playing", playing);

    let hls: Hls | undefined;
    if (Hls.isSupported()) {
      hls = new Hls({
        liveSyncDurationCount: 2,
        maxLiveSyncPlaybackRate: 1.5,
        backBufferLength: 10,
        manifestLoadingMaxRetry: 2,
        xhrSetup: (xhr, u) => {
          const rw = rewriteRef.current;
          if (rw) {
            const next = rw(u);
            if (next !== u) xhr.open("GET", next, true);
          }
        },
      });
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (data.fatal) {
          hls?.destroy();
          scheduleRetry();
        }
      });
      hls.loadSource(url);
      hls.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = url;
      video.addEventListener("error", scheduleRetry, { once: true });
    }
    video.play().catch(() => {});

    return () => {
      clearTimeout(retry);
      video.removeEventListener("playing", playing);
      hls?.destroy();
      video.removeAttribute("src");
      video.load();
    };
  }, [key, attempt, onPlaying]);

  return <video ref={ref} muted playsInline autoPlay />;
}
