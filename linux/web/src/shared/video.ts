// Video tokens and video-plane URLs (see "Video" in openapi.yaml).
//
// A video token is a short-lived (≤ 10 min) capability for one camera; it is
// a path prefix, `{baseUrl}` = `{recorderUrl}/v/{token}`, so players need no
// headers. VideoTokenSource keeps one fresh by asking again before expiry.

import { useEffect, useState } from "react";
import { ApiError, type ApiClient, type VideoRight, type VideoToken } from "./api";

export const liveHlsUrl = (base: string, cameraId: string) => `${base}/live/${encodeURIComponent(cameraId)}/index.m3u8`;
export const whepUrl = (base: string, cameraId: string) => `${base}/webrtc/${encodeURIComponent(cameraId)}/whep`;
export const playbackUrl = (base: string, cameraId: string, from: number, to: number) =>
  `${base}/api/v1/cameras/${encodeURIComponent(cameraId)}/playback.m3u8?from=${from}&to=${to}`;
export const segmentUrl = (base: string, cameraId: string, file: string) => `${base}/recordings/${encodeURIComponent(cameraId)}/${encodeURIComponent(file)}`;

const tokenSegment = /\/v\/[A-Za-z0-9._~-]+(?=\/|$)/;

/** Put `token` in place of the video token inside a video-plane URL (other URLs pass through). */
export function swapToken(url: string, token: string): string {
  return tokenSegment.test(url) ? url.replace(tokenSegment, `/v/${token}`) : url;
}

/**
 * Where a client should fetch video:
 * - `direct`: the recording server (`baseUrl`); needs it reachable and,
 *   for a browser on another origin, CORS on its video plane.
 * - `gateway`: through the management server (`gatewayUrl`), which works
 *   wherever the API does.
 * - `auto` (default): direct when that is the same origin as `apiBase`
 *   (or the page), else the gateway.
 */
export type VideoRoute = "auto" | "direct" | "gateway";

export function videoBase(t: VideoToken, route: VideoRoute = "auto", apiBase = ""): string {
  const origin = apiBase || (typeof location !== "undefined" ? location.origin : "");
  const gateway = /^https?:\/\//.test(t.gatewayUrl) ? t.gatewayUrl : `${apiBase}${t.gatewayUrl}`;
  if (route === "gateway") return gateway;
  if (route === "direct") return /^https?:\/\//.test(t.baseUrl) ? t.baseUrl : `${apiBase}${t.baseUrl}`;
  if (!/^https?:\/\//.test(t.baseUrl)) return `${apiBase}${t.baseUrl}`;
  try {
    if (new URL(t.baseUrl).origin === new URL(origin).origin) return t.baseUrl;
  } catch {
    /* fall through */
  }
  return gateway;
}

export interface VideoTokenSourceOptions {
  fetchToken: () => Promise<VideoToken>;
  onToken: (t: VideoToken) => void;
  onError?: (e: unknown) => void;
  /** Ask again this long before expiry (at least 20% of the lifetime). Default 60 s. */
  marginMs?: number;
  now?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (t: unknown) => void;
}

/** Keeps a video token fresh until stopped. Errors back off (5 s … 60 s); 403/404 stop. */
export class VideoTokenSource {
  private timer: unknown = null;
  private stopped = true;
  private failures = 0;
  constructor(private readonly o: VideoTokenSourceOptions) {}

  start() {
    if (!this.stopped) return;
    this.stopped = false;
    void this.fetch();
  }

  stop() {
    this.stopped = true;
    if (this.timer !== null) (this.o.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>)))(this.timer);
    this.timer = null;
  }

  /** Delay until the next refresh for a token issued now. */
  refreshDelay(t: VideoToken): number {
    const now = (this.o.now ?? Date.now)();
    const life = Math.max(0, t.expiresAt - now);
    const margin = Math.max(this.o.marginMs ?? 60_000, life * 0.2);
    return Math.max(1000, life - margin);
  }

  private schedule(ms: number) {
    const set = this.o.setTimer ?? ((fn, d) => setTimeout(fn, d));
    this.timer = set(() => {
      this.timer = null;
      void this.fetch();
    }, ms);
  }

  private async fetch() {
    if (this.stopped) return;
    try {
      const t = await this.o.fetchToken();
      if (this.stopped) return;
      this.failures = 0;
      this.o.onToken(t);
      this.schedule(this.refreshDelay(t));
    } catch (e) {
      if (this.stopped) return;
      this.o.onError?.(e);
      if (e instanceof ApiError && (e.status === 403 || e.status === 404 || e.status === 401)) return;
      this.failures++;
      this.schedule(Math.min(60_000, 5000 * 2 ** (this.failures - 1)));
    }
  }
}

/** React: a fresh video token for one camera, or the error that stops it. */
export function useVideoToken(client: ApiClient, cameraId: string | null, rights: VideoRight[] = ["live"], recorderId?: string) {
  const [token, setToken] = useState<VideoToken | null>(null);
  const [error, setError] = useState<unknown>(null);
  const rightsKey = rights.join(",");
  useEffect(() => {
    setToken(null);
    setError(null);
    if (!cameraId) return;
    const src = new VideoTokenSource({
      fetchToken: () => client.cameras.videoToken(cameraId, rightsKey.split(",") as VideoRight[], recorderId),
      onToken: (t) => {
        setToken(t);
        setError(null);
      },
      onError: setError,
    });
    src.start();
    return () => src.stop();
  }, [client, cameraId, rightsKey, recorderId]);
  return { token, error };
}
