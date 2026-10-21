// Sentinel Admin › Security › People (wave 16, faces): the face recognition
// switch with its privacy note, the people gallery, one person's faces, and
// the strip of unassigned faces to enroll. Reads need ViewLive; every change
// (and the unassigned strip, which the server limits to ChangeSettings) is
// hidden without ChangeSettings. Logic is in peopleModel.ts. The Operator
// hosts it too (WAVE17) on its bearer client (detectionHost.ts).
import { useContext, useMemo, useState } from "react";
import type { Face, FacesStatus, Person } from "../detectionApi";
import AuthImage from "../shared/AuthImage";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { DetectionContext, detectionCtx, type SectionHost } from "./detectionHost";
import {
  DAYS_MAX,
  DAYS_MIN,
  PRIVACY_NOTE,
  alertChoices,
  alertLabel,
  canToggle,
  clampDays,
  facesStatusLine,
  lastSeen,
  personFormOf,
  personInput,
  personPatch,
  personProblem,
  sortPeople,
  type PersonForm,
} from "./peopleModel";
import { Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";
import { useCan } from "./useCan";

export default function PeopleSection({ client = v2, can: hostCan }: SectionHost = {}) {
  const ctx = useMemo(() => detectionCtx(client), [client]);
  return (
    <DetectionContext.Provider value={ctx}>
      <People hostCan={hostCan} />
    </DetectionContext.Provider>
  );
}

function People({ hostCan }: { hostCan: SectionHost["can"] }) {
  const { client, api } = useContext(DetectionContext);
  const can = useCan(client, hostCan);
  const canEdit = can("changeSettings");
  const status = useLoad(() => api.faces.status(), [api]);
  const people = useLoad(() => api.people.list(), [api]);
  const cameras = useLoad(() => client.cameras.list(), [client]);
  const unassigned = useLoad(() => (canEdit ? api.faces.unassigned(60) : Promise.resolve([] as Face[])), [api, canEdit]);
  const [editing, setEditing] = useState<{ person: Person | null; faceId?: string } | null>(null);
  const [deleting, setDeleting] = useState<Person | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState<Face | null>(null);
  const [error, setError] = useState<unknown>(null);
  const camName = useMemo(() => {
    const m = new Map((cameras.data ?? []).map((c) => [c.id, c.name]));
    return (id: string) => m.get(id);
  }, [cameras.data]);
  const list = useMemo(() => sortPeople(people.data ?? []), [people.data]);
  const now = Date.now();

  const reloadAll = () => {
    void status.reload();
    void people.reload();
    void unassigned.reload();
  };

  async function removeFace(f: Face) {
    setError(null);
    try {
      await api.faces.remove(f.id);
      reloadAll();
    } catch (e) {
      setError(e);
    }
  }

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>People</h1>
          <div className="sub">Recognize the people you add here on Person events, and decide what their alarms do</div>
        </div>
        {canEdit && (
          <button className="btn primary" onClick={() => setEditing({ person: null })}>
            + Add person
          </button>
        )}
      </div>
      <ErrorBanner error={status.error ?? people.error} onRetry={reloadAll} />
      <ErrorBanner error={error} />
      {status.data && <FacesSwitch status={status.data} canEdit={canEdit} onSaved={(s) => (status.setData(s), void unassigned.reload())} />}

      {canEdit && (unassigned.data?.length ?? 0) > 0 && (
        <section className="card" aria-labelledby="ppl-unassigned">
          <h2 id="ppl-unassigned">Unassigned faces</h2>
          <p className="muted small">Faces that matched nobody, newest first. Add one to a person to recognize them from now on.</p>
          <ul className="ppl-strip">
            {(unassigned.data ?? []).map((f) => (
              <li key={f.id} className="ppl-face">
                <FaceImg faceId={f.id} />
                <div className="muted small">
                  {camName(f.cameraId) ?? "Camera"} · {new Date(f.at).toLocaleString()}
                </div>
                <div className="ppl-face-actions">
                  <button type="button" className="btn small" disabled={list.length === 0} onClick={() => setAdding(f)} title={list.length === 0 ? "Add a person first" : undefined}>
                    Add to person
                  </button>
                  <button type="button" className="btn small" onClick={() => setEditing({ person: null, faceId: f.id })}>
                    New person
                  </button>
                  <button type="button" className="btn small danger" onClick={() => void removeFace(f)} aria-label="Delete this face">
                    ✕
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="card table-wrap">
        {people.data === null ? (
          people.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="☺" title="No people yet">
            {canEdit ? "Add a person, then add faces to them from the unassigned strip." : "An administrator adds people here."}
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>When seen</th>
                <th>Faces</th>
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
                    <button type="button" className="linkish" onClick={() => setOpen(p.id)}>
                      <strong>{p.name}</strong>
                    </button>
                    {p.notes && <div className="muted small">{p.notes}</div>}
                  </td>
                  <td>
                    <span className={`badge ${p.alert === "always" ? "red" : p.alert === "never" ? "green" : "blue"}`}>{alertLabel(p.alert)}</span>
                  </td>
                  <td className="mono">{p.faces.length}</td>
                  <td className="small">{lastSeen(p, camName, now)}</td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setOpen(p.id)}>
                      Faces
                    </button>
                    {canEdit && (
                      <>
                        <button className="btn small" onClick={() => setEditing({ person: p })}>
                          Edit
                        </button>
                        <button className="btn small danger" onClick={() => setDeleting(p)}>
                          Delete…
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

      {open && <PersonPanel personId={open} canEdit={canEdit} camName={camName} onClose={() => setOpen(null)} onChanged={reloadAll} />}
      {editing && (
        <PersonEditor
          person={editing.person}
          faceId={editing.faceId}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reloadAll();
          }}
        />
      )}
      {adding && (
        <AddToPerson
          face={adding}
          people={list}
          onClose={() => setAdding(null)}
          onSaved={() => {
            setAdding(null);
            reloadAll();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete person"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await api.people.remove(deleting.id);
            setDeleting(null);
            reloadAll();
          }}
        >
          <p>Their stored faces are deleted too, and they are no longer recognized. Past events keep the name.</p>
        </Confirm>
      )}
    </>
  );
}

/** A face crop: <img src> on the Admin's cookie, fetched through a bearer client. Exported for the tests. */
export function FaceImg({ faceId, alt = "Face" }: { faceId: string; alt?: string }) {
  const { client, api, direct } = useContext(DetectionContext);
  return <AuthImage client={client} path={api.faces.cropPath(faceId)} direct={direct} className="ppl-crop" alt={alt} width={96} height={96} />;
}

function FacesSwitch({ status, canEdit, onSaved }: { status: FacesStatus; canEdit: boolean; onSaved: (s: FacesStatus) => void }) {
  const { api } = useContext(DetectionContext);
  const [days, setDays] = useState(status.unassignedDays);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function save(body: { enabled?: boolean; unassignedDays?: number }) {
    setBusy(true);
    setError(null);
    try {
      onSaved(await api.faces.put(body));
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-labelledby="ppl-switch">
      <h2 id="ppl-switch">Face recognition</h2>
      <ErrorBanner error={error} />
      {canEdit ? (
        <label className="check">
          <input type="checkbox" checked={status.enabled} disabled={busy || !canToggle(status)} onChange={(e) => void save({ enabled: e.target.checked })} />
          Recognize faces on Person events
        </label>
      ) : null}
      <p className={status.available ? "small" : "small warn-text"} role="status">
        {facesStatusLine(status)}
      </p>
      <p className="muted small ppl-privacy">{PRIVACY_NOTE}</p>
      <label className="field ppl-days">
        <span>Keep unassigned faces for (days)</span>
        <span className="ppl-days-row">
          <input type="number" min={DAYS_MIN} max={DAYS_MAX} value={days} disabled={!canEdit || busy} onChange={(e) => setDays(clampDays(Number(e.target.value)))} />
          {canEdit && days !== status.unassignedDays && (
            <button type="button" className="btn small" disabled={busy} onClick={() => void save({ unassignedDays: days })}>
              Save
            </button>
          )}
        </span>
      </label>
    </section>
  );
}

function PersonPanel({ personId, canEdit, camName, onClose, onChanged }: { personId: string; canEdit: boolean; camName: (id: string) => string | undefined; onClose: () => void; onChanged: () => void }) {
  const { api } = useContext(DetectionContext);
  const person = useLoad(() => api.people.get(personId), [api, personId]);
  const [error, setError] = useState<unknown>(null);
  const p = person.data;

  async function removeFace(f: Face) {
    setError(null);
    try {
      await api.faces.remove(f.id);
      void person.reload();
      onChanged();
    } catch (e) {
      setError(e);
    }
  }

  return (
    <Modal
      wide
      title={p ? p.name : "Person"}
      onClose={onClose}
      actions={
        <button type="button" className="btn" onClick={onClose}>
          Close
        </button>
      }
    >
      <ErrorBanner error={person.error ?? error} onRetry={person.error ? person.reload : undefined} />
      {!p ? (
        person.error ? null : <Loading />
      ) : (
        <>
          <p className="muted small">
            {alertLabel(p.alert)} · last seen {lastSeen(p, camName, Date.now())}
            {p.notes ? ` · ${p.notes}` : ""}
          </p>
          {p.faces.length === 0 ? (
            <p className="muted small">No faces yet. Add one from the unassigned faces.</p>
          ) : (
            <ul className="ppl-strip ppl-grid">
              {p.faces.map((f) => (
                <li key={f.id} className="ppl-face">
                  <FaceImg faceId={f.id} />
                  <div className="muted small">
                    {camName(f.cameraId) ?? "Camera"} · {new Date(f.at).toLocaleDateString()}
                    {f.similarity !== undefined && ` · ${Math.round(f.similarity * 100)}%`}
                  </div>
                  {canEdit && (
                    <button type="button" className="btn small danger" onClick={() => void removeFace(f)}>
                      Delete face
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Modal>
  );
}

function PersonEditor({ person, faceId, onClose, onSaved }: { person: Person | null; faceId?: string; onClose: () => void; onSaved: () => void }) {
  const { api } = useContext(DetectionContext);
  const [f, setF] = useState<PersonForm>(() => personFormOf(person));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problem = personProblem(f);
  const set = <K extends keyof PersonForm>(k: K, v: PersonForm[K]) => setF((x) => ({ ...x, [k]: v }));

  async function save() {
    if (problem) return;
    setBusy(true);
    setError(null);
    try {
      if (person) {
        const patch = personPatch(person, f);
        if (Object.keys(patch).length) await api.people.update(person.id, patch);
      } else {
        await api.people.create({ ...personInput(f), faceId });
      }
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  return (
    <Modal
      title={person ? `Edit ${person.name}` : faceId ? "New person from this face" : "Add person"}
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
      {faceId && <FaceImg faceId={faceId} alt="The face to enroll" />}
      <label className="field">
        <span>Name</span>
        <input type="text" value={f.name} maxLength={80} onChange={(e) => set("name", e.target.value)} autoFocus />
      </label>
      <label className="field">
        <span>When they are seen</span>
        <select value={f.alert} onChange={(e) => set("alert", e.target.value as PersonForm["alert"])}>
          {alertChoices.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
        <small>{alertChoices.find((c) => c.value === f.alert)?.help}</small>
      </label>
      <label className="field">
        <span>Notes (optional)</span>
        <textarea value={f.notes} maxLength={500} rows={2} onChange={(e) => set("notes", e.target.value)} />
      </label>
      {problem && f.name.trim() && <span className="field-problem">{problem}</span>}
      {person && f.name.trim() !== person.name && <p className="muted small">Renaming also updates the name on their past events.</p>}
    </Modal>
  );
}

function AddToPerson({ face, people, onClose, onSaved }: { face: Face; people: Person[]; onClose: () => void; onSaved: () => void }) {
  const { api } = useContext(DetectionContext);
  const [personId, setPersonId] = useState(people[0]?.id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <Modal
      title="Add face to person"
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          await api.people.enroll(personId, face.id);
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
          <button className="btn primary" disabled={busy || !personId}>
            {busy ? "Adding…" : "Add"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <FaceImg faceId={face.id} />
      <label className="field">
        <span>Person</span>
        <select value={personId} onChange={(e) => setPersonId(e.target.value)}>
          {people.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <small>The face's event is labelled with their name too.</small>
      </label>
    </Modal>
  );
}
