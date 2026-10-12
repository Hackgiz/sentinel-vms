// WebRTC live-view helpers: server settings and the viewer's transport choice.
import { useEffect, useState } from "react";

export type Transport = "auto" | "webrtc" | "hls";
export const transports: { value: Transport; label: string }[] = [
  { value: "auto", label: "Auto" },
  { value: "webrtc", label: "WebRTC" },
  { value: "hls", label: "HLS" },
];

const transportKey = "live.transport";

export function loadTransport(): Transport {
  try {
    const v = localStorage.getItem(transportKey);
    if (v === "auto" || v === "webrtc" || v === "hls") return v;
  } catch {
    /* storage blocked */
  }
  return "auto";
}

export function saveTransport(t: Transport) {
  try {
    localStorage.setItem(transportKey, t);
  } catch {
    /* storage blocked */
  }
}

export interface WebRTCSettings {
  enabled: boolean;
  /** UDP + TCP port browsers send media to. */
  port: number;
  /** Extra addresses set in the dashboard. */
  additionalHosts: string[];
  /** Extra addresses from -webrtc-hosts / SENTINEL_WEBRTC_HOSTS. */
  configHosts: string[];
}

async function call(method: "GET" | "PUT", body?: unknown): Promise<WebRTCSettings> {
  const res = await fetch("/api/v1/webrtc", {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error ?? `Request failed (${res.status})`);
  return data as WebRTCSettings;
}

export const webrtcApi = {
  get: () => call("GET"),
  setHosts: (additionalHosts: string[]) => call("PUT", { additionalHosts }),
};

/** Whether the server offers WebRTC; null while loading. */
export function useWebRTCEnabled(): boolean | null {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  useEffect(() => {
    let alive = true;
    webrtcApi
      .get()
      .then((s) => alive && setEnabled(s.enabled))
      .catch(() => alive && setEnabled(false));
    return () => {
      alive = false;
    };
  }, []);
  return enabled;
}
