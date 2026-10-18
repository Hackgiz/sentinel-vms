import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { CameraZone } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { arrowHead, directionArrows, directionOf, lineDirections, linePoints, lineProblem, lineSeverities, severityOf, type LineDirection, type LineSeverity } from "../shared/lines";
import { closesPolygon, maxPoints, maxZones, minPoints, polygonProblem, roundPoint, svgPoints, toPoint, type Pt } from "../shared/zones";
import { v2 } from "./client";
import { Empty, ErrorBanner, Loading } from "./ui";
import "./zones.css";

// Zones & privacy: per camera, detection zones (motion/object detection only
// counts inside them) and privacy masks (blacked out of every snapshot,
// thumbnail and export). The backdrop is the camera's latest event picture;
// masked areas already show black there. Lines are two-point tripwires: a
// tracked person/vehicle/animal crossing one (in its direction) raises a
// "Line crossing" event and alarm.

type Kind = "detect" | "mask" | "line";
interface Draft {
  key: string;
  id?: string;
  kind: Kind;
  name: string;
  enabled: boolean;
  points: Pt[];
  /** Lines only: which way crossings count, relative to p0 -> p1. */
  direction: LineDirection;
  /** Lines only: the alarm a crossing raises ("Info" = event only). */
  severity: LineSeverity;
}

let nextKey = 1;
const kindNames: Record<Kind, string> = { detect: "Detection zone", mask: "Privacy mask", line: "Line" };
const fromZone = (z: CameraZone): Draft => ({ key: z.id, id: z.id, kind: z.kind, name: z.name, enabled: z.enabled, points: z.points as Pt[], direction: directionOf(z.meta), severity: severityOf(z.meta) });
const blank = (kind: Kind, points: Pt[], n: number): Draft => ({ key: `new-${nextKey++}`, kind, name: `${kindNames[kind]} ${n}`, enabled: true, points, direction: "both", severity: "Critical" });
const needed = (kind: Kind) => (kind === "line" ? linePoints : minPoints);
const problemOf = (z: Draft) => (z.kind === "line" ? lineProblem(z.points) : polygonProblem(z.points));

export default function ZonesSection() {
  const cameras = useLoad(() => v2.cameras.list(), []);
  const [picked, setPicked] = useState("");
  const list = cameras.data ?? [];
  const camera = list.find((c) => c.id === picked) ?? list[0] ?? null;
  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Zones &amp; privacy</h1>
          <div className="sub">
            Detection zones limit motion and person/vehicle detection to part of the picture. Privacy masks black out part of the picture in every snapshot, thumbnail and exported clip (exports of a camera with masks are re-encoded, so they take longer). Lines raise a line-crossing event and alarm when a person, vehicle or animal crosses them.
          </div>
        </div>
        <label className="zn-pick">
          <span className="muted small">Camera</span>
          <select value={camera?.id ?? ""} onChange={(e) => setPicked(e.target.value)} aria-label="Camera">
            {list.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </label>
      </div>
      <ErrorBanner error={cameras.error} onRetry={cameras.reload} />
      {cameras.data === null ? (
        cameras.error ? null : <Loading />
      ) : !camera ? (
        <div className="card">
          <Empty icon="▣" title="No cameras yet">
            Add a camera first, then draw its zones here.
          </Empty>
        </div>
      ) : (
        <ZoneEditor key={camera.id} cameraId={camera.id} cameraName={camera.name} />
      )}
    </>
  );
}

/** Latest event picture of the camera, as an object URL (null until/unless there is one). */
function useBackdrop(cameraId: string): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    let made: string | null = null;
    void (async () => {
      try {
        const page = await v2.events.page({ cameraId, limit: 25 });
        const ev = page.items.find((e) => e.hasThumbnail);
        if (!ev || ctl.signal.aborted) return;
        const blob = await v2.blob(`/api/v2/events/${encodeURIComponent(ev.id)}/thumbnail.jpg`, ctl.signal);
        if (ctl.signal.aborted) return;
        made = URL.createObjectURL(blob);
        setUrl(made);
      } catch {
        /* no picture: the editor works on a plain grid */
      }
    })();
    return () => {
      ctl.abort();
      if (made) URL.revokeObjectURL(made);
    };
  }, [cameraId]);
  return url;
}

function ZoneEditor({ cameraId, cameraName }: { cameraId: string; cameraName: string }) {
  const loaded = useLoad(() => v2.zones.list(cameraId), [cameraId]);
  const [zones, setZones] = useState<Draft[]>([]);
  const [saved, setSaved] = useState<Draft[]>([]);
  const [drawing, setDrawing] = useState<{ kind: Kind; points: Pt[] } | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [aspect, setAspect] = useState(16 / 9);
  const box = useRef<HTMLDivElement>(null);
  const image = useBackdrop(cameraId);

  useEffect(() => {
    if (!loaded.data) return;
    const d = loaded.data.map(fromZone);
    setZones(d);
    setSaved(d);
  }, [loaded.data]);

  const strip = (d: Draft[]) =>
    JSON.stringify(
      d.map(({ id, kind, name, enabled, points, direction, severity }) => ({
        id,
        kind,
        name,
        enabled,
        points,
        direction: kind === "line" ? direction : undefined,
        severity: kind === "line" ? severity : undefined,
      })),
    );
  const dirty = strip(zones) !== strip(saved);
  const problems = useMemo(() => zones.map(problemOf), [zones]);
  const blocked = problems.some(Boolean);

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  function finish(d = drawing) {
    if (!d || d.points.length < needed(d.kind)) return;
    const n = zones.filter((z) => z.kind === d.kind).length + 1;
    const z = blank(d.kind, d.points.map(roundPoint), n);
    setZones((zs) => [...zs, z]);
    setSelected(z.key);
    setDrawing(null);
  }

  useEffect(() => {
    if (!drawing) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setDrawing(null);
      else if (e.key === "Enter") finish();
      else if (e.key === "Backspace") {
        e.preventDefault();
        setDrawing((d) => (d ? { ...d, points: d.points.slice(0, -1) } : d));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [drawing]);

  const point = (e: { clientX: number; clientY: number }) => toPoint(e.clientX, e.clientY, box.current!.getBoundingClientRect());

  function click(e: React.MouseEvent) {
    if (!drawing) return setSelected(null);
    const p = point(e);
    if (drawing.kind === "line") {
      // A line is done at its second point.
      const next = { ...drawing, points: [...drawing.points, p] };
      return next.points.length >= linePoints ? finish(next) : setDrawing(next);
    }
    if (closesPolygon(drawing.points, p)) return finish();
    if (drawing.points.length >= maxPoints) return;
    setDrawing({ ...drawing, points: [...drawing.points, p] });
  }

  function dragVertex(e: ReactPointerEvent, key: string, index: number) {
    if (e.button !== 0 || drawing) return;
    e.stopPropagation();
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    setSelected(key);
    const move = (ev: PointerEvent) => {
      const p = roundPoint(point(ev));
      setZones((zs) => zs.map((z) => (z.key === key ? { ...z, points: z.points.map((q, i) => (i === index ? p : q)) } : z)));
    };
    const up = () => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  }

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const out = await v2.zones.put(
        cameraId,
        zones.map((z) => ({
          ...(z.id ? { id: z.id } : {}),
          kind: z.kind,
          name: z.name.trim(),
          points: z.points,
          ...(z.kind === "line" ? { meta: { direction: z.direction, severity: z.severity } } : {}),
          enabled: z.enabled,
        })),
      );
      const d = out.map(fromZone);
      setZones(d);
      setSaved(d);
      setSelected(null);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  const patch = (key: string, p: Partial<Draft>) => setZones((zs) => zs.map((z) => (z.key === key ? { ...z, ...p } : z)));
  const full = zones.length >= maxZones;

  return (
    <div className="zn-editor">
      <ErrorBanner error={loaded.error} onRetry={loaded.reload} />
      <ErrorBanner error={error} />
      <div className="fp-bar">
        <strong className="grow">{cameraName}</strong>
        <button className="btn small" disabled={!!drawing || full} onClick={() => (setDrawing({ kind: "detect", points: [] }), setSelected(null))}>
          + Detection zone
        </button>
        <button className="btn small" disabled={!!drawing || full} onClick={() => (setDrawing({ kind: "mask", points: [] }), setSelected(null))}>
          + Privacy mask
        </button>
        <button className="btn small" disabled={!!drawing || full} onClick={() => (setDrawing({ kind: "line", points: [] }), setSelected(null))}>
          + Line
        </button>
        <button className="btn small" disabled={!dirty || busy} onClick={() => (setZones(saved), setSelected(null), setDrawing(null))}>
          Discard
        </button>
        <button className="btn small primary" disabled={!dirty || busy || blocked} onClick={() => void save()}>
          {busy ? "Saving…" : "Save zones"}
        </button>
      </div>
      <div className="fp-body">
        <aside className="fp-side card" aria-label="Zones">
          <div className="muted small fp-hint">
            {drawing?.kind === "line"
              ? `Click the picture where the line starts, then where it ends (${drawing.points.length}/${linePoints}). Esc cancels.`
              : drawing
              ? `Click the picture to add points (${drawing.points.length}/${maxPoints}). Click the first point or press Enter to finish; Backspace removes the last point; Esc cancels.`
              : "Add a zone, then click its corners on the picture. Drag a corner to move it. With no detection zones the whole picture is watched. A line counts crossings from its left side to its right side as you look from its first point to its second."}
          </div>
          {loaded.data === null && !loaded.error && <Loading />}
          <div className="zn-list">
            {zones.map((z, i) => (
              <div key={z.key} className={`zn-row ${selected === z.key ? "sel" : ""} ${z.kind}`} onClick={() => setSelected(z.key)}>
                <div className="zn-row-top">
                  <span className={`zn-swatch ${z.kind}`} aria-hidden="true" />
                  <input type="text" value={z.name} maxLength={100} aria-label="Zone name" onChange={(e) => patch(z.key, { name: e.target.value })} />
                </div>
                <div className="zn-row-bottom">
                  {z.kind === "line" ? (
                    <select value={z.direction} aria-label="Line direction" onChange={(e) => patch(z.key, { direction: e.target.value as LineDirection })}>
                      {lineDirections.map((d) => (
                        <option key={d.value} value={d.value}>
                          {d.label}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <select value={z.kind} aria-label="Zone kind" onChange={(e) => patch(z.key, { kind: e.target.value as Kind })}>
                      <option value="detect">Detection zone</option>
                      <option value="mask">Privacy mask</option>
                    </select>
                  )}
                  <label className="fp-check">
                    <input type="checkbox" checked={z.enabled} onChange={(e) => patch(z.key, { enabled: e.target.checked })} />
                    On
                  </label>
                  <button className="btn small danger" onClick={() => (setZones((zs) => zs.filter((x) => x.key !== z.key)), setSelected(null))}>
                    Delete
                  </button>
                </div>
                {z.kind === "line" && (
                  <div className="zn-row-bottom">
                    <select value={z.severity} aria-label="Line severity" onChange={(e) => patch(z.key, { severity: e.target.value as LineSeverity })}>
                      {lineSeverities.map((s) => (
                        <option key={s.value} value={s.value}>
                          {s.label}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
                {problems[i] && <div className="error small">{problems[i]}</div>}
              </div>
            ))}
            {zones.length === 0 && !drawing && loaded.data && <div className="muted small">No zones yet.</div>}
          </div>
        </aside>
        <div className="fp-stage">
          <div
            ref={box}
            className={`fp-plan edit zn-canvas ${drawing ? "placing" : ""}`}
            style={{ aspectRatio: `${aspect}`, width: `min(100%, calc(72vh * ${aspect}))` }}
            onClick={click}
          >
            {image && <img src={image} alt={`Latest picture from ${cameraName}`} draggable={false} onLoad={(e) => e.currentTarget.naturalHeight > 0 && setAspect(e.currentTarget.naturalWidth / e.currentTarget.naturalHeight)} />}
            {!image && <div className="zn-nopic muted small">No recent picture from this camera. You can still draw zones on the grid.</div>}
            <svg className="zn-svg" viewBox="0 0 1 1" preserveAspectRatio="none" aria-hidden="true">
              {zones.map((z) =>
                z.kind === "line" ? (
                  <LineShape key={z.key} z={z} selected={selected === z.key} aspect={aspect} />
                ) : (
                  <polygon key={z.key} className={`zn-poly ${z.kind} ${z.enabled ? "" : "off"} ${selected === z.key ? "sel" : ""}`} points={svgPoints(z.points)} />
                ),
              )}
              {drawing && drawing.points.length > 1 && <polyline className={`zn-poly ${drawing.kind} drawing`} points={svgPoints(drawing.points)} />}
            </svg>
            {zones.map((z) =>
              selected === z.key
                ? z.points.map((p, i) => (
                    <button key={`${z.key}-${i}`} type="button" className="zn-vertex" style={{ left: `${p[0] * 100}%`, top: `${p[1] * 100}%` }} aria-label={`${z.name} point ${i + 1}`} onPointerDown={(e) => dragVertex(e, z.key, i)} onClick={(e) => e.stopPropagation()} />
                  ))
                : null,
            )}
            {drawing?.points.map((p, i) => (
              <span key={i} className={`zn-vertex drawing ${i === 0 ? "first" : ""}`} style={{ left: `${p[0] * 100}%`, top: `${p[1] * 100}%` }} />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

/** A line and its direction arrows. Not rendered while the line is invalid. */
function LineShape({ z, selected, aspect }: { z: Draft; selected: boolean; aspect: number }) {
  if (z.points.length !== linePoints) return null;
  const [p0, p1] = z.points;
  const arrows = lineProblem(z.points) ? [] : directionArrows(p0, p1, z.direction, 0.09, aspect);
  const cls = `zn-poly line ${z.enabled ? "" : "off"} ${selected ? "sel" : ""}`;
  return (
    <g>
      <polyline className={cls} fill="none" points={svgPoints(z.points)} />
      {arrows.map((a, i) => (
        <g key={i}>
          <polyline className={`${cls} zn-arrow`} fill="none" points={svgPoints([a.from, a.to])} />
          <polygon className={`${cls} zn-arrow-head`} points={arrowHead(a.from, a.to, 0.03, aspect)} />
        </g>
      ))}
    </g>
  );
}
