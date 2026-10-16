import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { Camera, Floorplan, FloorplanPin } from "../shared/api";
import { toNormalized, usePlanImage, withPin } from "../shared/floorplan";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading, Modal, useLiveReload } from "./ui";
import "./floorplans.css";

const maxImage = 8 * 1024 * 1024;

export default function FloorplansSection() {
  const plans = useLoad(() => v2.floorplans.list(), []);
  const cameras = useLoad(() => v2.cameras.list(), []);
  const [picked, setPicked] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const [renaming, setRenaming] = useState<Floorplan | null>(null);
  const [deleting, setDeleting] = useState<Floorplan | null>(null);
  useLiveReload(cameras.reload, ["config.changed"]);
  const list = plans.data ?? [];
  const plan = list.find((p) => p.id === picked) ?? list[0] ?? null;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Floorplans</h1>
          <div className="sub">Upload a site map and pin cameras on it. Operators open cameras, and see offline cameras and alarms, from the Map view.</div>
        </div>
        <button className="btn primary" onClick={() => setUploading(true)}>
          + Upload floorplan
        </button>
      </div>
      <ErrorBanner error={plans.error || cameras.error} onRetry={() => (plans.reload(), cameras.reload())} />
      {plans.data === null ? (
        plans.error ? null : <Loading />
      ) : list.length === 0 ? (
        <div className="card">
          <Empty icon="▣" title="No floorplans yet">
            Upload a PNG, JPEG or WebP of your site (up to 8 MB), then place your cameras on it.
          </Empty>
        </div>
      ) : (
        <>
          <div className="fp-tabs" role="tablist" aria-label="Floorplans">
            {list.map((p) => (
              <button key={p.id} role="tab" aria-selected={plan?.id === p.id} className={plan?.id === p.id ? "on" : ""} onClick={() => setPicked(p.id)}>
                {p.name}
              </button>
            ))}
          </div>
          {plan && (
            <PinEditor
              key={plan.id}
              plan={plan}
              cameras={cameras.data ?? []}
              onRename={() => setRenaming(plan)}
              onDelete={() => setDeleting(plan)}
              onSaved={(p) => plans.setData((cur) => (cur ?? []).map((x) => (x.id === p.id ? p : x)))}
            />
          )}
        </>
      )}
      {uploading && (
        <UploadDialog
          onClose={() => setUploading(false)}
          onUploaded={(p) => {
            setUploading(false);
            setPicked(p.id);
            void plans.reload();
          }}
        />
      )}
      {renaming && (
        <RenameDialog
          plan={renaming}
          onClose={() => setRenaming(null)}
          onSaved={() => {
            setRenaming(null);
            void plans.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete floorplan"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.floorplans.remove(deleting.id);
            setDeleting(null);
            setPicked(null);
            void plans.reload();
          }}
        >
          <p>The image and its {deleting.pins.length} pin{deleting.pins.length === 1 ? "" : "s"} are removed. The cameras stay.</p>
        </Confirm>
      )}
    </>
  );
}

function PinEditor({ plan, cameras, onRename, onDelete, onSaved }: { plan: Floorplan; cameras: Camera[]; onRename: () => void; onDelete: () => void; onSaved: (p: Floorplan) => void }) {
  const [pins, setPins] = useState<FloorplanPin[]>(plan.pins);
  const [saved, setSaved] = useState<FloorplanPin[]>(plan.pins);
  const [placing, setPlacing] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [query, setQuery] = useState("");
  const box = useRef<HTMLDivElement>(null);
  const image = usePlanImage((signal) => v2.floorplans.image(plan.id, signal), `${plan.id}:${plan.updatedAt}`);
  const dirty = JSON.stringify(pins) !== JSON.stringify(saved);
  const name = useMemo(() => new Map(cameras.map((c) => [c.id, c.name])), [cameras]);
  const pinned = useMemo(() => new Set(pins.map((p) => p.cameraId)), [pins]);
  const shown = cameras.filter((c) => c.name.toLowerCase().includes(query.trim().toLowerCase()));
  const pin = pins.find((p) => p.cameraId === selected);

  // Leaving with unsaved pins asks first.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const rect = () => box.current!.getBoundingClientRect();

  function place(e: React.MouseEvent) {
    if (!placing || !box.current) return;
    const { x, y } = toNormalized(e.clientX, e.clientY, rect());
    setPins((ps) => withPin(ps, placing, { x, y }));
    setSelected(placing);
    setPlacing(null);
  }

  function drag(e: ReactPointerEvent, cameraId: string) {
    if (e.button !== 0) return;
    e.stopPropagation();
    e.preventDefault();
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    setSelected(cameraId);
    setPlacing(null);
    const move = (ev: PointerEvent) => {
      const n = toNormalized(ev.clientX, ev.clientY, rect());
      setPins((ps) => withPin(ps, cameraId, n));
    };
    const up = (ev: PointerEvent) => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      // Dropped outside the plan: remove the pin.
      if (!toNormalized(ev.clientX, ev.clientY, rect()).inside) {
        setPins((ps) => withPin(ps, cameraId, null));
        setSelected(null);
      }
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  }

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const p = await v2.floorplans.setPins(plan.id, pins);
      setSaved(p.pins);
      setPins(p.pins);
      onSaved(p);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fp-editor">
      <div className="fp-bar">
        <strong className="grow">{plan.name}</strong>
        <span className="muted small">
          {pins.length} camera{pins.length === 1 ? "" : "s"} pinned
        </span>
        <button className="btn small" onClick={onRename}>
          Rename…
        </button>
        <button className="btn small danger" onClick={onDelete}>
          Delete…
        </button>
        <button className="btn small" disabled={!dirty || busy} onClick={() => (setPins(saved), setSelected(null))}>
          Discard
        </button>
        <button className="btn small primary" disabled={!dirty || busy} onClick={() => void save()}>
          {busy ? "Saving…" : "Save pins"}
        </button>
      </div>
      <ErrorBanner error={error} />
      <div className="fp-body">
        <aside className="fp-side card" aria-label="Cameras">
          <input type="search" placeholder="Find a camera" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Find a camera" />
          <div className="muted small fp-hint">
            {placing ? `Click the plan to place ${name.get(placing) ?? "the camera"}.` : "Pick a camera, then click the plan. Drag a pin to move it; drag it off the plan or right-click to remove it."}
          </div>
          <div className="fp-cams">
            {shown.map((c) => (
              <button
                key={c.id}
                type="button"
                className={`fp-cam ${placing === c.id ? "on" : ""} ${selected === c.id ? "sel" : ""}`}
                aria-pressed={placing === c.id}
                onClick={() => {
                  setPlacing(placing === c.id ? null : c.id);
                  if (pinned.has(c.id)) setSelected(c.id);
                }}
              >
                <span className="grow">{c.name}</span>
                {pinned.has(c.id) && <span className="badge blue">pinned</span>}
              </button>
            ))}
            {cameras.length === 0 && <div className="muted small">No cameras yet.</div>}
          </div>
          {pin && (
            <div className="fp-heading">
              <div className="field-label">{name.get(pin.cameraId) ?? "Camera"}: direction</div>
              <label className="fp-check">
                <input
                  type="checkbox"
                  checked={pin.heading >= 0}
                  onChange={(e) => setPins((ps) => ps.map((p) => (p.cameraId === pin.cameraId ? { ...p, heading: e.target.checked ? 0 : -1 } : p)))}
                />
                Show a view direction
              </label>
              {pin.heading >= 0 && (
                <div className="fp-dial">
                  <input
                    type="range"
                    min={0}
                    max={359}
                    value={pin.heading}
                    aria-label="Heading in degrees"
                    onChange={(e) => setPins((ps) => ps.map((p) => (p.cameraId === pin.cameraId ? { ...p, heading: Number(e.target.value) } : p)))}
                  />
                  <span className="mono">{Math.round(pin.heading)}°</span>
                </div>
              )}
              <button className="btn small danger" onClick={() => (setPins((ps) => withPin(ps, pin.cameraId, null)), setSelected(null))}>
                Remove pin
              </button>
            </div>
          )}
        </aside>
        <div className="fp-stage">
          <div ref={box} className={`fp-plan edit ${placing ? "placing" : ""}`} style={{ aspectRatio: `${plan.width} / ${plan.height}`, width: `min(100%, calc(72vh * ${plan.width / plan.height}))` }} onClick={place}>
            {image ? <img src={image} alt={`Floorplan ${plan.name}`} draggable={false} /> : <Loading label="Loading the plan…" />}
            {pins.map((p) => (
              <button
                key={p.cameraId}
                type="button"
                className={`fp-pin ${selected === p.cameraId ? "sel" : ""}`}
                style={{ left: `${p.x * 100}%`, top: `${p.y * 100}%` }}
                title={name.get(p.cameraId) ?? "Removed camera"}
                aria-label={`${name.get(p.cameraId) ?? "Camera"} pin`}
                onPointerDown={(e) => drag(e, p.cameraId)}
                onClick={(e) => e.stopPropagation()}
                onContextMenu={(e) => {
                  e.preventDefault();
                  setPins((ps) => withPin(ps, p.cameraId, null));
                  setSelected(null);
                }}
              >
                {p.heading >= 0 && <span className="fp-cone" style={{ transform: `rotate(${p.heading}deg)` }} />}
                <span className="fp-dot" />
                <span className="fp-label">{name.get(p.cameraId) ?? "Removed camera"}</span>
              </button>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

function UploadDialog({ onClose, onUploaded }: { onClose: () => void; onUploaded: (p: Floorplan) => void }) {
  const [name, setName] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const tooBig = !!file && file.size > maxImage;
  return (
    <Modal
      title="Upload floorplan"
      onClose={onClose}
      onSubmit={async () => {
        if (!file) return;
        setBusy(true);
        setError(null);
        try {
          onUploaded(await v2.floorplans.create(name.trim(), file));
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !file || tooBig || !name.trim()}>
            {busy ? "Uploading…" : "Upload"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} maxLength={100} required autoFocus placeholder="Ground floor" onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="field">
        <span>Image (PNG, JPEG or WebP, up to 8 MB)</span>
        <input
          type="file"
          accept="image/png,image/jpeg,image/webp"
          required
          onChange={(e) => {
            const f = e.target.files?.[0] ?? null;
            setFile(f);
            if (f && !name.trim()) setName(f.name.replace(/\.[^.]+$/, ""));
          }}
        />
        {tooBig && <span className="field-problem">That file is larger than 8 MB.</span>}
      </label>
    </Modal>
  );
}

function RenameDialog({ plan, onClose, onSaved }: { plan: Floorplan; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(plan.name);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      title={`Rename ${plan.name}`}
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          await v2.floorplans.update(plan.id, { name: name.trim() });
          onSaved();
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !name.trim()}>
            Save
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} maxLength={100} required autoFocus onChange={(e) => setName(e.target.value)} />
      </label>
    </Modal>
  );
}
