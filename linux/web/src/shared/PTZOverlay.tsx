// PTZ controls over a live tile: four direction arrows and zoom +/-
// (press and hold moves, release stops; arrow keys and +/- do the same while
// the tile is focused) and a presets menu. Shared by the Operator's camera
// grid and the dashboard's Live page. Renders nothing unless the camera's
// /ptz probe says it supports PTZ, the camera is online and the user may
// steer it (ptzVisible). A stop goes out on release, when the window loses
// focus or the page is hidden, when the camera goes offline, and on unmount.
import { useCallback, useEffect, useMemo, useState, type PointerEvent as ReactPointerEvent } from "react";
import { errorMessage, type ApiClient, type PTZPreset, type PTZState } from "./api";
import { cleanPresetName, isTypingTarget, keyDir, PtzHold, ptzVisible, type PtzDir } from "./ptz";
import "./ptz.css";

export interface PTZOverlayProps {
  client: ApiClient;
  cameraId: string;
  /** Arrow keys and +/- steer this camera while the overlay is shown. */
  keyboard: boolean;
  /** False while the camera is offline: the controls hide. */
  online?: boolean;
  /** The user's `controlPtz` right. */
  allowed?: boolean;
  /** A /ptz answer the host already has: shown at once, then refreshed. */
  initial?: PTZState | null;
  className?: string;
}

export default function PTZOverlay({ client, cameraId, keyboard, online = true, allowed = true, initial, className = "" }: PTZOverlayProps) {
  // null = unsupported or unknown
  const [presets, setPresets] = useState<PTZPreset[] | null>(() => (initial?.supported ? initial.presets : null));
  const [menu, setMenu] = useState(false);
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [lit, setLit] = useState<PtzDir | null>(null);
  const hasInitial = initial != null;

  useEffect(() => {
    let live = true;
    if (!hasInitial) setPresets(null);
    if (!allowed) return; // no right, no probe
    client.ptz
      .state(cameraId)
      .then((s) => live && setPresets(s.supported ? s.presets : null))
      .catch(() => live && setPresets(null));
    return () => {
      live = false;
    };
  }, [client, cameraId, hasInitial, allowed]);
  const visible = ptzVisible({ supported: presets !== null, online, canControl: allowed });

  const hold = useMemo(() => new PtzHold(client.ptz, cameraId, (e) => setNote(errorMessage(e)), setLit), [client, cameraId]);
  // Never leave the camera moving when the overlay goes away or switches camera.
  useEffect(() => () => hold.releaseAll(), [hold]);
  // ...or when it hides (camera offline, right withdrawn).
  useEffect(() => {
    if (!visible) hold.releaseAll();
  }, [visible, hold]);

  const press = useCallback((d: PtzDir) => hold.press(d), [hold]);
  const release = useCallback((d: PtzDir) => hold.release(d), [hold]);
  const send = useCallback((fn: () => Promise<unknown>) => hold.send(fn), [hold]);

  useEffect(() => {
    if (!visible) return;
    const releaseAll = () => hold.releaseAll();
    const hidden = () => document.visibilityState === "hidden" && hold.releaseAll();
    window.addEventListener("blur", releaseAll);
    document.addEventListener("visibilitychange", hidden);
    return () => {
      window.removeEventListener("blur", releaseAll);
      document.removeEventListener("visibilitychange", hidden);
    };
  }, [visible, hold]);

  useEffect(() => {
    if (!visible || !keyboard) return;
    // Capture phase, so a page's own arrow-key shortcuts (the dashboard's
    // timeline steps) see defaultPrevented and leave the key to PTZ.
    const down = (e: KeyboardEvent) => {
      const d = keyDir(e.key);
      if (!d || e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)) return;
      e.preventDefault();
      if (!e.repeat) press(d);
    };
    const up = (e: KeyboardEvent) => {
      const d = keyDir(e.key);
      if (d) release(d);
    };
    window.addEventListener("keydown", down, true);
    window.addEventListener("keyup", up, true);
    return () => {
      window.removeEventListener("keydown", down, true);
      window.removeEventListener("keyup", up, true);
    };
  }, [visible, keyboard, press, release]);

  if (!visible || presets === null) return null;


  const holdProps = (d: PtzDir) => ({
    onPointerDown: (e: ReactPointerEvent<HTMLButtonElement>) => {
      e.preventDefault();
      e.currentTarget.setPointerCapture?.(e.pointerId);
      press(d);
    },
    onPointerUp: () => release(d),
    onPointerCancel: () => release(d),
    onLostPointerCapture: () => release(d),
  });
  const pad = (d: PtzDir, label: string, glyph: string, cls = "") => (
    <button type="button" className={`btn small op-ptz-btn ${cls} ${lit === d ? "on" : ""}`} aria-label={label} title={label} {...holdProps(d)}>
      {glyph}
    </button>
  );

  async function savePreset() {
    const n = cleanPresetName(name);
    if (!n) return setNote("Give the preset a name (up to 64 characters).");
    try {
      const p = await client.ptz.savePreset(cameraId, n);
      setPresets((list) => [...(list ?? []), p]);
      setName("");
      setNote("");
    } catch (e) {
      setNote(errorMessage(e));
    }
  }
  async function removePreset(p: PTZPreset) {
    if (!window.confirm(`Delete preset "${p.name}"?`)) return;
    try {
      await client.ptz.removePreset(cameraId, p.token);
      setPresets((list) => (list ?? []).filter((x) => x.token !== p.token));
    } catch (e) {
      setNote(errorMessage(e));
    }
  }

  return (
    <div
      className={`op-ptz ${className}`.trim()}
      draggable={false}
      onDragStart={(e) => {
        e.preventDefault();
        e.stopPropagation();
      }}
      onDoubleClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.preventDefault()}
    >
      <div className="op-ptz-pad" role="group" aria-label="Pan, tilt and zoom">
        {pad("up", "Tilt up (↑)", "▲", "u")}
        {pad("left", "Pan left (←)", "◀", "l")}
        {pad("right", "Pan right (→)", "▶", "r")}
        {pad("down", "Tilt down (↓)", "▼", "d")}
        {pad("zoomIn", "Zoom in (+)", "+", "zi")}
        {pad("zoomOut", "Zoom out (−)", "−", "zo")}
      </div>
      <button type="button" className={`btn small ${menu ? "on" : ""}`} aria-expanded={menu} onClick={() => setMenu((m) => !m)}>
        Presets
      </button>
      {menu && (
        <div className="op-ptz-menu" role="menu">
          {presets.length === 0 && <div className="op-ptz-empty">No presets yet</div>}
          {presets.map((p) => (
            <div key={p.token} className="op-ptz-preset">
              <button type="button" className="btn small" role="menuitem" onClick={() => send(() => client.ptz.gotoPreset(cameraId, p.token))}>
                {p.name}
              </button>
              <button type="button" className="btn small danger" title={`Delete ${p.name}`} aria-label={`Delete ${p.name}`} onClick={() => void removePreset(p)}>
                ✕
              </button>
            </div>
          ))}
          <form
            className="op-ptz-save"
            onSubmit={(e) => {
              e.preventDefault();
              void savePreset();
            }}
          >
            <input type="text" value={name} maxLength={64} placeholder="Save position as…" onChange={(e) => setName(e.target.value)} />
            <button type="submit" className="btn small primary">
              Save
            </button>
          </form>
        </div>
      )}
      {note && (
        <div className="op-ptz-note" role="status" onClick={() => setNote("")}>
          {note}
        </div>
      )}
    </div>
  );
}
