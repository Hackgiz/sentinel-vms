// The live grid: a layout's tiles, each a VideoTile that starts when it is
// on screen (staggered, so 16 tiles don't all negotiate at once) and stops
// when scrolled away or the window is hidden. Cameras are dropped onto
// tiles from the camera list or dragged between tiles; double-click
// maximizes a tile.
import { useEffect, useRef, useState, type DragEvent, type ReactNode, type RefObject } from "react";
import type { Camera } from "../shared/api";
import VideoTile from "../shared/VideoTile";
import { desktop } from "./bridge";
import PTZOverlay from "./PTZOverlay";
import TalkButton from "./TalkButton";
import { useData } from "./data";
import { activityKind, reconnecting, type ActivityKind } from "./healthModel";
import { tileStyle, type GridLayout } from "./layout";
import { useSigned } from "./session";

export const cameraMime = "text/x-sentinel-camera";
const tileMime = "text/x-sentinel-tile";

/** Whether the page is visible (not minimized, not a background tab). */
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => typeof document === "undefined" || document.visibilityState !== "hidden");
  useEffect(() => {
    const on = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", on);
    return () => document.removeEventListener("visibilitychange", on);
  }, []);
  return visible;
}

function useInView(ref: RefObject<HTMLElement | null>): boolean {
  const [inView, setInView] = useState(true);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(([e]) => setInView(e.isIntersecting), { threshold: 0.05 });
    io.observe(el);
    return () => io.disconnect();
  }, [ref]);
  return inView;
}

/**
 * On/off for a tile's stream: on after a stagger of `index` steps once
 * wanted, off after a short grace once not (so a quick window switch
 * doesn't restart every stream).
 */
function useActive(want: boolean, index: number): boolean {
  const [active, setActive] = useState(false);
  useEffect(() => {
    const t = want ? setTimeout(() => setActive(true), Math.min(index, 24) * 120) : setTimeout(() => setActive(false), 4000);
    return () => clearTimeout(t);
  }, [want, index]);
  return active;
}

/** Latest event per camera from the existing stream, for the tile activity glyphs. `seq` restarts the fade. */
type Activity = Record<string, { kind: ActivityKind; seq: number }>;

function useCameraActivity(): Activity {
  const data = useData();
  const [activity, setActivity] = useState<Activity>({});
  useEffect(
    () =>
      data.subscribe((m) => {
        if (m.type !== "event.new") return;
        const { cameraId, kind } = m.data;
        setActivity((a) => ({ ...a, [cameraId]: { kind: activityKind(kind), seq: (a[cameraId]?.seq ?? 0) + 1 } }));
      }),
    [data],
  );
  return activity;
}

const glyphPaths: Record<ActivityKind, string> = {
  person: "M12 4a3 3 0 1 1 0 6 3 3 0 0 1 0-6zm-5 16v-5a5 5 0 0 1 10 0v5z",
  vehicle: "M5 11l1.6-4.2A2 2 0 0 1 8.5 5.5h7a2 2 0 0 1 1.9 1.3L19 11a2 2 0 0 1 1.5 1.9V17h-2v1.5h-2.5V17H8v1.5H5.5V17h-2v-4.1A2 2 0 0 1 5 11zm2.3-.5h9.4l-1-2.5H8.3z",
  animal: "M7 9a2 2 0 1 1 0 4 2 2 0 0 1 0-4zm10 0a2 2 0 1 1 0 4 2 2 0 0 1 0-4zM10 5a2 2 0 1 1 0 4 2 2 0 0 1 0-4zm4 0a2 2 0 1 1 0 4 2 2 0 0 1 0-4zm-2 6c3 0 5 2.5 5 4.5S15.5 19 12 19s-5-1.5-5-3.5S9 11 12 11z",
  motion: "M3 12c2-4 4-4 6 0s4 4 6 0 4-4 6 0v2c-2-4-4-4-6 0s-4 4-6 0-4-4-6 0z",
  other: "M12 7a5 5 0 1 1 0 10 5 5 0 0 1 0-10z",
};

/** A short-lived glyph in the title bar: pulses when its camera has an event, then fades (CSS only, ~5 s). */
function ActivityGlyph({ kind, seq }: { kind: ActivityKind; seq: number }) {
  return (
    <span key={seq} className="op-activity" title={`Just detected: ${kind === "other" ? "event" : kind}`} aria-hidden="true">
      <svg viewBox="0 0 24 24">
        <path d={glyphPaths[kind]} />
      </svg>
    </span>
  );
}

function LiveCell({
  camera,
  index,
  focused,
  selected,
  onDoubleClick,
  tools,
  activity,
}: {
  camera: Camera;
  index: number;
  activity?: { kind: ActivityKind; seq: number };
  focused?: boolean;
  /** The selected tile: PTZ controls (when the camera has PTZ) and keys apply to it. */
  selected?: boolean;
  onDoubleClick?: () => void;
  tools?: ReactNode;
}) {
  const { session, can } = useSigned();
  const data = useData();
  const ref = useRef<HTMLDivElement>(null);
  const inView = useInView(ref);
  const visible = usePageVisible();
  const active = useActive(inView && visible, index);
  return (
    <div ref={ref} className="op-cell-inner">
      <VideoTile client={session.client} camera={camera} route={session.route} webrtc={data.webrtcFor(camera)} focused={focused} active={active} onDoubleClick={onDoubleClick} titleExtra={activity && <ActivityGlyph kind={activity.kind} seq={activity.seq} />}>
        {tools}
        {reconnecting(camera) && <div className="op-reconnecting" role="status">reconnecting…</div>}
        {(focused || selected) && can("controlPtz") && <PTZOverlay client={session.client} cameraId={camera.id} keyboard />}
        {(focused || selected) && can("controlPtz") && camera.talkback && <TalkButton client={session.client} cameraId={camera.id} cameraName={camera.name} />}
      </VideoTile>
    </div>
  );
}

export default function CameraGrid({
  layout,
  onAssign,
  onSwap,
  onClear,
  emptyHint = "Drag a camera here",
  selected,
  onSelect,
  renderCell,
  onMaximize,
}: {
  layout: GridLayout;
  /** Editable grids: a camera was dropped on tile i. */
  onAssign?: (index: number, cameraId: string) => void;
  onSwap?: (a: number, b: number) => void;
  onClear?: (index: number) => void;
  emptyHint?: string;
  /** The selected tile (accent border); the timeline bar follows its camera. */
  selected?: number | null;
  onSelect?: (index: number) => void;
  /** Replace a tile's content (Playback shows recorded video on the same grid). */
  renderCell?: (camera: Camera, index: number, tools: ReactNode) => ReactNode;
  onMaximize?: (index: number | null) => void;
}) {
  const { session } = useSigned();
  const data = useData();
  const [maximized, setMaximized] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const editable = !!onAssign;
  const activity = useCameraActivity();

  // A layout change (another view) leaves maximize.
  useEffect(() => setMaximized(null), [layout.columns, layout.rows]);
  useEffect(() => onMaximize?.(maximized), [maximized, onMaximize]);
  useEffect(() => {
    if (maximized === null) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setMaximized(null);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [maximized]);

  const drop = (i: number) => (e: DragEvent) => {
    e.preventDefault();
    setOver(null);
    const cam = e.dataTransfer.getData(cameraMime);
    const tile = e.dataTransfer.getData(tileMime);
    if (cam) onAssign?.(i, cam);
    else if (tile !== "") onSwap?.(Number(tile), i);
  };

  // Maximized: one tile spans the grid; the others stay mounted but hidden,
  // so their streams stop after the grace period and restore quickly.
  const max = maximized !== null && layout.tiles[maximized] ? maximized : null;
  return (
    <div className="op-grid" style={{ gridTemplateColumns: `repeat(${layout.columns}, minmax(0, 1fr))`, gridTemplateRows: `repeat(${layout.rows}, minmax(0, 1fr))` }}>
      {layout.tiles.map((t, i) => {
        const cam = t.cameraId ? data.camera(t.cameraId) : undefined;
        const style =
          max === null
            ? tileStyle(t)
            : max === i
              ? { gridColumn: `1 / span ${layout.columns}`, gridRow: `1 / span ${layout.rows}` }
              : { display: "none" };
        const dropProps = editable
          ? {
              onDragOver: (e: DragEvent) => {
                if (e.dataTransfer.types.includes(cameraMime) || e.dataTransfer.types.includes(tileMime)) {
                  e.preventDefault();
                  setOver(i);
                }
              },
              onDragLeave: () => setOver((o) => (o === i ? null : o)),
              onDrop: drop(i),
            }
          : {};
        return (
          <div
            key={`${i}:${t.cameraId ?? ""}`}
            className={`op-cell ${over === i ? "drop" : ""} ${selected === i ? "selected" : ""} ${reconnecting(cam) ? "offline" : ""}`}
            onMouseDown={() => onSelect?.(i)}
            style={style}
            draggable={editable && !!cam}
            onDragStart={(e) => {
              e.dataTransfer.setData(tileMime, String(i));
              e.dataTransfer.effectAllowed = "move";
            }}
            {...dropProps}
          >
            {cam ? (
              (() => {
                const tools = (
                  <div className="op-tile-tools">
                    {desktop && (
                      <button
                        className="btn small"
                        title="Open in a new window"
                        onClick={() => void desktop!.windows.open({ kind: "camera", siteId: session.site.id, cameraId: cam.id, title: cam.name })}
                      >
                        ⧉
                      </button>
                    )}
                    <button className="btn small" title={maximized === i ? "Back to the grid (Esc)" : "Maximize"} onClick={() => setMaximized((m) => (m === i ? null : i))}>
                      {maximized === i ? "▣" : "⛶"}
                    </button>
                    {onClear && (
                      <button className="btn small" title="Remove from the grid" onClick={() => onClear(i)}>
                        ✕
                      </button>
                    )}
                  </div>
                );
                return renderCell ? (
                  <div className="op-cell-inner" onDoubleClick={() => setMaximized((m) => (m === i ? null : i))}>
                    {renderCell(cam, i, tools)}
                  </div>
                ) : (
                  <LiveCell camera={cam} index={i} focused={maximized === i} selected={selected === i} onDoubleClick={() => setMaximized((m) => (m === i ? null : i))} tools={tools} activity={activity[cam.id]} />
                );
              })()
            ) : (
              <div className="tile op-empty">
                <div className="placeholder">
                  <div>
                    {t.cameraId ? (
                      <>
                        <span className="big">⊘</span>Camera not available
                      </>
                    ) : (
                      editable && emptyHint
                    )}
                  </div>
                </div>
                {t.cameraId && onClear && (
                  <div className="op-tile-tools">
                    <button className="btn small" title="Remove from the grid" onClick={() => onClear(i)}>
                      ✕
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
