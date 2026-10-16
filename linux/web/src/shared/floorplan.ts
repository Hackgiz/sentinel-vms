// Floorplan helpers shared by the Admin pin editor and the Operator map.
import { useEffect, useState } from "react";
import type { Alarm, Camera, FloorplanPin } from "./api";

/** A pointer position as a 0..1 fraction of the image box; `inside` is false when it fell outside the box. */
export function toNormalized(clientX: number, clientY: number, box: { left: number; top: number; width: number; height: number }): { x: number; y: number; inside: boolean } {
  const rx = box.width > 0 ? (clientX - box.left) / box.width : 0;
  const ry = box.height > 0 ? (clientY - box.top) / box.height : 0;
  const clamp = (v: number) => Math.min(1, Math.max(0, v));
  return { x: clamp(rx), y: clamp(ry), inside: rx >= 0 && rx <= 1 && ry >= 0 && ry <= 1 };
}

/** Pins with one camera's pin added, moved or (to === null) removed. A camera has one pin per plan. */
export function withPin(pins: FloorplanPin[], cameraId: string, to: { x: number; y: number; heading?: number } | null): FloorplanPin[] {
  const rest = pins.filter((p) => p.cameraId !== cameraId);
  if (!to) return rest;
  const old = pins.find((p) => p.cameraId === cameraId);
  return [...rest, { cameraId, x: round(to.x), y: round(to.y), heading: to.heading ?? old?.heading ?? -1 }];
}

const round = (v: number) => Math.round(v * 10000) / 10000;

export type PinState = "ok" | "offline" | "alarm";

/** Alarm states that make a pin pulse (a snoozed alarm is parked on purpose). */
const pulsing = new Set(["New", "Acknowledged", "Investigating"]);

/** The newest pulsing alarm on a camera, if any. `alarms` is newest first. */
export function alarmOn(alarms: Alarm[], cameraId: string): Alarm | undefined {
  return alarms.find((a) => a.cameraId === cameraId && pulsing.has(a.state));
}

/** What a pin shows: an open alarm beats offline beats normal. */
export function pinState(camera: Camera | undefined, alarms: Alarm[], cameraId: string): PinState {
  if (alarmOn(alarms, cameraId)) return "alarm";
  if (!camera || !camera.status.online) return "offline";
  return "ok";
}

/** The plan's image as an object URL (fetched with the client's auth); null while loading or on failure. */
export function usePlanImage(fetchImage: (signal: AbortSignal) => Promise<Blob>, key: string): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    let made: string | null = null;
    setUrl(null);
    fetchImage(ctl.signal)
      .then((b) => {
        if (ctl.signal.aborted) return;
        made = URL.createObjectURL(b);
        setUrl(made);
      })
      .catch(() => {});
    return () => {
      ctl.abort();
      if (made) URL.revokeObjectURL(made);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return url;
}
