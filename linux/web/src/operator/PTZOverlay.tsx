// PTZ controls over a live tile: four direction arrows and zoom +/-
// (press and hold moves, release stops; arrow keys and +/- do the same while
// the tile is focused) and a presets menu. Renders nothing unless the
// camera's /ptz probe says it supports PTZ.
import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { errorMessage, type ApiClient, type PTZPreset } from "../shared/api";
import { cleanPresetName, isStill, isTypingTarget, keyDir, velocityOf, type PtzDir } from "./ptz";

export default function PTZOverlay({ client, cameraId, keyboard }: { client: ApiClient; cameraId: string; keyboard: boolean }) {
  const [presets, setPresets] = useState<PTZPreset[] | null>(null); // null = unsupported or unknown
  const [menu, setMenu] = useState(false);
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const held = useRef(new Set<PtzDir>());
  const [lit, setLit] = useState<PtzDir | null>(null);
  // Commands are sent one after another so a stop can never overtake a move.
  const queue = useRef<Promise<unknown>>(Promise.resolve());

  const send = useCallback(
    (fn: () => Promise<unknown>) => {
      queue.current = queue.current.then(fn).catch((e) => setNote(errorMessage(e)));
    },
    [],
  );

  useEffect(() => {
    let live = true;
    setPresets(null);
    client.ptz
      .state(cameraId)
      .then((s) => live && setPresets(s.supported ? s.presets : null))
      .catch(() => live && setPresets(null));
    return () => {
      live = false;
    };
  }, [client, cameraId]);
  const supported = presets !== null;

  const apply = useCallback(() => {
    const v = velocityOf(held.current);
    setLit([...held.current].at(-1) ?? null);
    send(() => (isStill(v) ? client.ptz.stop(cameraId) : client.ptz.move(cameraId, v.pan, v.tilt, v.zoom)));
  }, [client, cameraId, send]);
  const press = useCallback(
    (d: PtzDir) => {
      if (held.current.has(d)) return;
      held.current.add(d);
      apply();
    },
    [apply],
  );
  const release = useCallback(
    (d: PtzDir) => {
      if (!held.current.delete(d)) return;
      apply();
    },
    [apply],
  );
  const releaseAll = useCallback(() => {
    if (held.current.size === 0) return;
    held.current.clear();
    apply();
  }, [apply]);

  useEffect(() => {
    if (!supported || !keyboard) return;
    const down = (e: KeyboardEvent) => {
      const d = keyDir(e.key);
      if (!d || e.repeat || e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)) return;
      e.preventDefault();
      press(d);
    };
    const up = (e: KeyboardEvent) => {
      const d = keyDir(e.key);
      if (d) release(d);
    };
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    window.addEventListener("blur", releaseAll);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
      window.removeEventListener("blur", releaseAll);
    };
  }, [supported, keyboard, press, release, releaseAll]);

  // Never leave the camera moving when the overlay goes away.
  useEffect(() => releaseAll, [releaseAll]);

  if (!supported) return null;

  const hold = (d: PtzDir) => ({
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
    <button type="button" className={`btn small op-ptz-btn ${cls} ${lit === d ? "on" : ""}`} aria-label={label} title={label} {...hold(d)}>
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
      className="op-ptz"
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
