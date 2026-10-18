// Export options for the operator export forms (Exports workspace and long
// export): a watermark checkbox and a "pixelate regions…" step that opens a
// one-shot dialog on the same canvas the admin zone editor draws on (the
// zn-canvas styles, the shared normalized-point helpers). Regions are drawn
// as rectangles by dragging.
import { useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import { Modal } from "../admin/ui";
import { toPoint, type Pt } from "../shared/zones";
import { addRegion, maxRegions, optionsReencode, optionsSummary, rectFrom, removeRegion, type ExportOptions, type Region } from "./exportOptionsModel";
import { useSigned } from "./session";
import "../admin/floorplans.css";
import "../admin/zones.css";

export default function ExportOptionsFields({
  value,
  onChange,
  cameraId,
  cameraName,
  disabled,
}: {
  value: ExportOptions;
  onChange: (o: ExportOptions) => void;
  /** The camera to pick regions on; null when there is not exactly one. */
  cameraId: string | null;
  cameraName?: string;
  disabled?: boolean;
}) {
  const [picking, setPicking] = useState(false);
  const n = value.blurRegions.length;
  return (
    <>
      <label className="check" title="Burn who exported the clip, when, and the case id into the video. Re-encodes the clip.">
        <input type="checkbox" checked={value.watermark} disabled={disabled} onChange={(e) => onChange({ ...value, watermark: e.target.checked })} />
        Watermark
      </label>
      <button
        type="button"
        className="btn small"
        disabled={disabled || !cameraId}
        title={cameraId ? "Draw regions to pixelate in the exported video" : "Pick exactly one camera to draw regions on"}
        onClick={() => setPicking(true)}
      >
        {n ? `Pixelate regions… (${n})` : "Pixelate regions…"}
      </button>
      {picking && cameraId && (
        <RegionPickerDialog
          cameraId={cameraId}
          cameraName={cameraName ?? "camera"}
          initial={value.blurRegions}
          onCancel={() => setPicking(false)}
          onApply={(blurRegions) => {
            onChange({ ...value, blurRegions });
            setPicking(false);
          }}
        />
      )}
    </>
  );
}

/** One line under the form saying what the options do ("" with none). */
export function ExportOptionsNote({ value }: { value: ExportOptions }) {
  if (!optionsReencode(value)) return null;
  return (
    <div className="muted small op-pad">
      {optionsSummary(value)}: the clip is re-encoded (it is no longer the camera's own video), so it takes longer.
    </div>
  );
}

/** Latest event picture of the camera as an object URL (null until/unless there is one). */
function useBackdrop(cameraId: string): string | null {
  const client = useSigned().session.client;
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    let made: string | null = null;
    void (async () => {
      try {
        const page = await client.events.page({ cameraId, limit: 25 });
        const ev = page.items.find((e) => e.hasThumbnail);
        if (!ev || ctl.signal.aborted) return;
        const blob = await client.blob(`/api/v2/events/${encodeURIComponent(ev.id)}/thumbnail.jpg`, ctl.signal);
        if (ctl.signal.aborted) return;
        made = URL.createObjectURL(blob);
        setUrl(made);
      } catch {
        /* no picture: the canvas is a plain grid */
      }
    })();
    return () => {
      ctl.abort();
      if (made) URL.revokeObjectURL(made);
    };
  }, [client, cameraId]);
  return url;
}

function RegionPickerDialog({
  cameraId,
  cameraName,
  initial,
  onCancel,
  onApply,
}: {
  cameraId: string;
  cameraName: string;
  initial: Region[];
  onCancel: () => void;
  onApply: (regions: Region[]) => void;
}) {
  const [regions, setRegions] = useState<Region[]>(initial);
  const [drag, setDrag] = useState<{ from: Pt; to: Pt } | null>(null);
  const [aspect, setAspect] = useState(16 / 9);
  const box = useRef<HTMLDivElement>(null);
  const image = useBackdrop(cameraId);

  const point = (e: ReactPointerEvent): Pt => toPoint(e.clientX, e.clientY, box.current?.getBoundingClientRect() ?? { left: 0, top: 0, width: 1, height: 1 });
  const rect = (r: { x0: number; y0: number; x1: number; y1: number }, key: string | number, cls: string) => (
    <rect key={key} className={`zn-poly mask ${cls}`} x={r.x0} y={r.y0} width={r.x1 - r.x0} height={r.y1 - r.y0} />
  );
  const live = drag ? rectFrom(drag.from, drag.to) : null;

  return (
    <Modal
      title={`Pixelate regions on ${cameraName}`}
      wide
      onClose={onCancel}
      onSubmit={() => onApply(regions)}
      actions={
        <>
          <button type="button" className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn primary">{regions.length ? `Use ${regions.length} region${regions.length > 1 ? "s" : ""}` : "Use no regions"}</button>
        </>
      }
    >
      <p className="small muted">
        Drag on the picture to draw each region (up to {maxRegions}). They are pixelated in the exported video, on top of the camera's privacy masks. The same regions apply for the whole range.
      </p>
      <div
        ref={box}
        className="fp-plan edit zn-canvas placing"
        style={{ aspectRatio: `${aspect}`, width: `min(100%, calc(60vh * ${aspect}))` }}
        onPointerDown={(e) => {
          e.currentTarget.setPointerCapture?.(e.pointerId);
          const p = point(e);
          setDrag({ from: p, to: p });
        }}
        onPointerMove={(e) => drag && setDrag({ from: drag.from, to: point(e) })}
        onPointerUp={(e) => {
          if (!drag) return;
          setRegions((rs) => addRegion(rs, rectFrom(drag.from, point(e))));
          setDrag(null);
        }}
        onPointerCancel={() => setDrag(null)}
        data-testid="region-canvas"
      >
        {image && <img src={image} alt={`Latest picture from ${cameraName}`} draggable={false} onLoad={(e) => e.currentTarget.naturalHeight > 0 && setAspect(e.currentTarget.naturalWidth / e.currentTarget.naturalHeight)} />}
        {!image && <div className="zn-nopic muted small">No recent picture from this camera. You can still draw regions on the grid.</div>}
        <svg className="zn-svg" viewBox="0 0 1 1" preserveAspectRatio="none" aria-hidden="true">
          {regions.map((r, i) => rect(r, i, ""))}
          {live && rect(live, "live", "drawing")}
        </svg>
      </div>
      {regions.length > 0 && (
        <div className="zn-list" style={{ marginTop: 8 }}>
          {regions.map((r, i) => (
            <div key={i} className="zn-row-top">
              <span className="zn-swatch mask" />
              <span className="small">
                Region {i + 1}: {Math.round((r.x1 - r.x0) * 100)}% × {Math.round((r.y1 - r.y0) * 100)}% of the picture
              </span>
              <button type="button" className="btn small danger" onClick={() => setRegions(removeRegion(regions, i))}>
                Remove
              </button>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}
