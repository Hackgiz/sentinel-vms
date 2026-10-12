// The live grid: a layout's tiles, each a VideoTile that starts when it is
// on screen (staggered, so 16 tiles don't all negotiate at once) and stops
// when scrolled away or the window is hidden. Cameras are dropped onto
// tiles from the camera list or dragged between tiles; double-click
// maximizes a tile.
import { useEffect, useRef, useState, type DragEvent, type ReactNode, type RefObject } from "react";
import type { Camera } from "../shared/api";
import VideoTile from "../shared/VideoTile";
import { desktop } from "./bridge";
import { useData } from "./data";
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

function LiveCell({
  camera,
  index,
  focused,
  onDoubleClick,
  tools,
}: {
  camera: Camera;
  index: number;
  focused?: boolean;
  onDoubleClick?: () => void;
  tools?: ReactNode;
}) {
  const { session } = useSigned();
  const data = useData();
  const ref = useRef<HTMLDivElement>(null);
  const inView = useInView(ref);
  const visible = usePageVisible();
  const active = useActive(inView && visible, index);
  return (
    <div ref={ref} className="op-cell-inner">
      <VideoTile client={session.client} camera={camera} route={session.route} webrtc={data.webrtcFor(camera)} focused={focused} active={active} onDoubleClick={onDoubleClick}>
        {tools}
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
            className={`op-cell ${over === i ? "drop" : ""} ${selected === i ? "selected" : ""}`}
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
                  <LiveCell camera={cam} index={i} focused={maximized === i} onDoubleClick={() => setMaximized((m) => (m === i ? null : i))} tools={tools} />
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
