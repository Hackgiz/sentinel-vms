import { useEffect, useRef, useState } from "react";
import Hls from "hls.js";

/**
 * Muted, auto-playing live HLS view. Safari plays HLS natively; everything
 * else uses hls.js. Retries quietly while the camera (or MediaMTX) comes up.
 */
export default function HlsVideo({ src, onPlaying }: { src: string; onPlaying?: (playing: boolean) => void }) {
  const ref = useRef<HTMLVideoElement>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const video = ref.current;
    if (!video) return;
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
      });
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (data.fatal) {
          hls?.destroy();
          scheduleRetry();
        }
      });
      hls.loadSource(src);
      hls.attachMedia(video);
    } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = src;
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
  }, [src, attempt, onPlaying]);

  return <video ref={ref} muted playsInline autoPlay />;
}
