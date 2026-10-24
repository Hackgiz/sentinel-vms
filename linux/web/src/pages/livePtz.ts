// PTZ on the dashboard's Live page: which tiles get the PTZ badge and which
// show the controls (shared/PTZOverlay.tsx), and the per-camera /ptz probe.
import { useEffect, useState } from "react";
import type { ApiClient, PTZState } from "../shared/api";
import { ptzVisible } from "../shared/ptz";

export interface TilePtz {
  /** The small PTZ badge in the title bar (click: select the tile, which opens the controls). */
  badge: boolean;
  /** The D-pad, zoom and presets over the tile. */
  controls: boolean;
}

/**
 * A tile's PTZ: nothing unless the camera supports PTZ, is online and the
 * user has `controlPtz`; then the controls while the tile is open (the
 * focus view, or the tile clicked in the grid), otherwise the badge.
 */
export function tilePtz(o: { state: PTZState | null; online: boolean; canControl: boolean; open: boolean }): TilePtz {
  const ok = ptzVisible({ supported: !!o.state?.supported, online: o.online, canControl: o.canControl });
  return { badge: ok && !o.open, controls: ok && o.open };
}

// Supported answers are kept for the page's life, so the badge does not
// flicker while tiles remount (grid ↔ focus). Unsupported or failed probes
// are asked again next time.
const known = new Map<string, PTZState>();

/** The camera's /ptz answer, probed while `enabled` (online and allowed); null until known. */
export function usePtzState(client: ApiClient, cameraId: string, enabled: boolean): PTZState | null {
  const [state, setState] = useState<PTZState | null>(() => known.get(cameraId) ?? null);
  useEffect(() => {
    if (!enabled) return;
    const hit = known.get(cameraId);
    if (hit) {
      setState(hit);
      return;
    }
    let live = true;
    setState(null);
    client.ptz
      .state(cameraId)
      .then((s) => {
        if (s.supported) known.set(cameraId, s);
        if (live) setState(s);
      })
      .catch(() => live && setState(null));
    return () => {
      live = false;
    };
  }, [client, cameraId, enabled]);
  return enabled ? state : null;
}
