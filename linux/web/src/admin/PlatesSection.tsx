// Sentinel Admin › Security › Plates (wave 16, plate reading): the switch
// and status, the watchlist with add and edit, and a plate's recent
// sightings. Reads need ViewLive; changes are hidden without ChangeSettings.
// Logic is in platesModel.ts. The Operator hosts it too (WAVE17) on its
// bearer client (detectionHost.ts).
import { useContext, useMemo, useState } from "react";
import type { LPRStatus, Plate } from "../detectionApi";
import AuthImage from "../shared/AuthImage";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { DetectionContext, detectionCtx, thumbnailPath, type SectionHost } from "./detectionHost";
import { ago } from "./peopleModel";
import { alertChoices, alertLabel, canToggle, filterPlates, lprStatusLine, normalizePlate, plateFormOf, plateInput, platePatch, plateProblem, sortPlates, type PlateForm } from "./platesModel";
import { Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";
import { useCan } from "./useCan";

export default function PlatesSection({ client = v2, can: hostCan }: SectionHost = {}) {
  const ctx = useMemo(() => detectionCtx(client), [client]);
  return (
    <DetectionContext.Provider value={ctx}>
      <Plates hostCan={hostCan} />
    </DetectionContext.Provider>
  );
}

function Plates({ hostCan }: { hostCan: SectionHost["can"] }) {
  const { client, api } = useContext(DetectionContext);
  const can = useCan(client, hostCan);
  const canEdit = can("changeSettings");
  const status = useLoad(() => api.lpr.status(), [api]);
  const plates = useLoad(() => api.plates.list(), [api]);
  const cameras = useLoad(() => client.cameras.list(), [client]);
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<Plate | "new" | null>(null);
  const [deleting, setDeleting] = useState<Plate | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const camName = useMemo(() => {
    const m = new Map((cameras.data ?? []).map((c) => [c.id, c.name]));
    return (id: string | undefined) => (id ? m.get(id) : undefined);
  }, [cameras.data]);
  const all = useMemo(() => sortPlates(plates.data ?? []), [plates.data]);
  const list = useMemo(() => filterPlates(all, q), [all, q]);
  const now = Date.now();
  const reloadAll = () => {
    void status.reload();
    void plates.reload();
  };

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Plates</h1>
          <div className="sub">Read license plates on vehicle events and watch for the ones you list</div>
        </div>
        {canEdit && (
          <button className="btn primary" onClick={() => setEditing("new")}>
            + Add plate
          </button>
        )}
      </div>
      <ErrorBanner error={status.error ?? plates.error} onRetry={reloadAll} />
      {status.data && <LPRSwitch status={status.data} canEdit={canEdit} onSaved={status.setData} />}

      <div className="card table-wrap">
        {all.length > 8 && (
          <input type="search" className="plt-filter" placeholder="Filter by plate or name" aria-label="Filter plates" value={q} onChange={(e) => setQ(e.target.value)} />
        )}
        {plates.data === null ? (
          plates.error ? null : <Loading />
        ) : all.length === 0 ? (
          <Empty icon="▭" title="The watchlist is empty">
            {canEdit ? "Add a plate to name it in alarms, ignore it, or raise a Critical alarm when it's read." : "An administrator adds plates here."}
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Plate</th>
                <th>Name</th>
                <th>When read</th>
                <th>Last seen</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((p) => (
                <tr key={p.id}>
                  <td>
                    <button type="button" className="linkish plt-plate mono" onClick={() => setOpen(p.id)}>
                      {p.plate}
                    </button>
                  </td>
                  <td>
                    {p.name || <span className="muted">—</span>}
                    {p.notes && <div className="muted small">{p.notes}</div>}
                  </td>
                  <td>
                    <span className={`badge ${p.alert === "always" ? "red" : p.alert === "never" ? "green" : "blue"}`}>{alertLabel(p.alert)}</span>
                  </td>
                  <td className="small">
                    {p.lastSeenAt ? `${ago(p.lastSeenAt, now)}${camName(p.lastSeenCameraId) ? ` · ${camName(p.lastSeenCameraId)}` : ""}` : "Never"}
                  </td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setOpen(p.id)}>
                      Sightings
                    </button>
                    {canEdit && (
                      <>
                        <button className="btn small" onClick={() => setEditing(p)}>
                          Edit
                        </button>
                        <button className="btn small danger" onClick={() => setDeleting(p)}>
                          Remove…
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {open && <Sightings plateId={open} camName={camName} onClose={() => setOpen(null)} />}
      {editing && (
        <PlateEditor
          plate={editing === "new" ? null : editing}
          existing={all}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reloadAll();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Remove ${deleting.plate}?`}
          confirmLabel="Remove plate"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await api.plates.remove(deleting.id);
            setDeleting(null);
            reloadAll();
          }}
        >
          <p>Sightings stay on their events; the plate is just no longer watched.</p>
        </Confirm>
      )}
    </>
  );
}

function LPRSwitch({ status, canEdit, onSaved }: { status: LPRStatus; canEdit: boolean; onSaved: (s: LPRStatus) => void }) {
  const { api } = useContext(DetectionContext);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <section className="card" aria-labelledby="plt-switch">
      <h2 id="plt-switch">Plate reading</h2>
      <ErrorBanner error={error} />
      {canEdit && (
        <label className="check">
          <input
            type="checkbox"
            checked={status.enabled}
            disabled={busy || !canToggle(status)}
            onChange={async (e) => {
              setBusy(true);
              setError(null);
              try {
                onSaved(await api.lpr.put(e.target.checked));
              } catch (err) {
                setError(err);
              } finally {
                setBusy(false);
              }
            }}
          />
          Read plates on Vehicle events
        </label>
      )}
      <p className={status.available ? "small" : "small warn-text"} role="status">
        {lprStatusLine(status)}
      </p>
      <p className="muted small">Plates are read on this server from the vehicle in each event's snapshot. Accuracy depends on the camera: a plate needs to be sharp and at least a few dozen pixels wide.</p>
    </section>
  );
}

function Sightings({ plateId, camName, onClose }: { plateId: string; camName: (id: string | undefined) => string | undefined; onClose: () => void }) {
  const { client, api, direct } = useContext(DetectionContext);
  const plate = useLoad(() => api.plates.get(plateId), [api, plateId]);
  const p = plate.data;
  const sightings = p?.sightings ?? [];
  return (
    <Modal
      wide
      title={p ? `${p.plate}${p.name ? ` · ${p.name}` : ""}` : "Plate"}
      onClose={onClose}
      actions={
        <button type="button" className="btn" onClick={onClose}>
          Close
        </button>
      }
    >
      <ErrorBanner error={plate.error} onRetry={plate.reload} />
      {!p ? (
        plate.error ? null : <Loading />
      ) : sightings.length === 0 ? (
        <p className="muted small">Not read on any event yet.</p>
      ) : (
        <>
          <p className="muted small">The newest {sightings.length} events with this plate.</p>
          <ul className="plt-sightings">
            {sightings.map((e) => (
              <li key={e.id}>
                {e.hasThumbnail ? <AuthImage client={client} path={thumbnailPath(e.id)} direct={direct} alt="" placeholder={<div className="event-ph">…</div>} /> : <div className="event-ph">No snapshot</div>}
                <div className="small">
                  <strong>{camName(e.cameraId) ?? "Camera"}</strong>
                  <div className="muted">{new Date(e.createdAt).toLocaleString()}</div>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
    </Modal>
  );
}

function PlateEditor({ plate, existing, onClose, onSaved }: { plate: Plate | null; existing: Plate[]; onClose: () => void; onSaved: () => void }) {
  const { api } = useContext(DetectionContext);
  const [f, setF] = useState<PlateForm>(() => plateFormOf(plate));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problem = plateProblem(f, existing, plate?.id);
  const set = <K extends keyof PlateForm>(k: K, v: PlateForm[K]) => setF((x) => ({ ...x, [k]: v }));
  const normalized = normalizePlate(f.plate);

  async function save() {
    if (problem) return;
    setBusy(true);
    setError(null);
    try {
      if (plate) {
        const patch = platePatch(plate, f);
        if (Object.keys(patch).length) await api.plates.update(plate.id, patch);
      } else {
        await api.plates.create(plateInput(f));
      }
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  return (
    <Modal
      title={plate ? `Edit ${plate.plate}` : "Add plate"}
      onClose={onClose}
      onSubmit={() => void save()}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !!problem}>
            {busy ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Plate</span>
        <input type="text" className="mono" value={f.plate} disabled={!!plate} maxLength={12} autoComplete="off" onChange={(e) => set("plate", e.target.value)} autoFocus={!plate} />
        <small>{plate ? "To change the plate, remove it and add the new one." : normalized && normalized !== f.plate ? `Saved as ${normalized} (letters and digits only).` : "Letters and digits; spaces and dashes are ignored."}</small>
      </label>
      <label className="field">
        <span>Name (optional)</span>
        <input type="text" value={f.name} maxLength={80} placeholder="Delivery van" onChange={(e) => set("name", e.target.value)} />
      </label>
      <label className="field">
        <span>When it is read</span>
        <select value={f.alert} onChange={(e) => set("alert", e.target.value as PlateForm["alert"])}>
          {alertChoices.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
        <small>{alertChoices.find((c) => c.value === f.alert)?.help} A plate of 6 or more characters also matches with one character misread.</small>
      </label>
      <label className="field">
        <span>Notes (optional)</span>
        <textarea value={f.notes} maxLength={500} rows={2} onChange={(e) => set("notes", e.target.value)} />
      </label>
      {problem && f.plate.trim() && <span className="field-problem">{problem}</span>}
    </Modal>
  );
}
