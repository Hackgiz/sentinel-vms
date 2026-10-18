// Scheduled exports: "this camera, 22:00 to 06:00, every night". When a
// window ends the server files it into the evidence vault as a long export.
// The list shows each schedule's last run; the editor dialog adds or edits
// one. Loaded on entering the tab; no timers.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Confirm, Modal } from "../admin/ui";
import { errorMessage, type ExportSchedule } from "../shared/api";
import { useData } from "./data";
import { blankDraft, dayNames, draftOf, lastRunText, scheduleBlocker, scheduleText, type ScheduleDraft } from "./longExportModel";
import { notify } from "./notices";
import { useSigned } from "./session";

export default function ScheduledExports() {
  const { session } = useSigned();
  const client = session.client;
  const data = useData();
  const [list, setList] = useState<ExportSchedule[] | null>(null);
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<{ id?: string; draft: ScheduleDraft } | null>(null);
  const [deleting, setDeleting] = useState<ExportSchedule | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const load = useCallback(() => {
    client.exportSchedules
      .list()
      .then((l) => {
        if (!alive.current) return;
        setList(l);
        setError("");
      })
      .catch((e) => alive.current && setError(errorMessage(e)));
  }, [client]);
  useEffect(load, [load]);

  const cameras = useMemo(() => [...(data.cameras ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [data.cameras]);
  const nameOf = (id: string) => data.camera(id)?.name ?? "Camera";

  async function toggle(s: ExportSchedule) {
    try {
      await client.exportSchedules.update(s.id, { enabled: !s.enabled });
      load();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  return (
    <div className="op-events op-exports op-scheduled">
      <section className="op-export-create" aria-label="Scheduled exports">
        <h2>Scheduled exports</h2>
        <p className="muted small">
          When a window ends, the server exports its footage into the evidence vault. Times are the server's local time; a window that ends before it starts runs overnight. Windows that end while the server is down are not backfilled.
        </p>
        <div className="op-toolbar">
          <button className="btn primary" onClick={() => setEditing({ draft: blankDraft(cameras[0]?.id ?? "") })}>
            Add schedule
          </button>
        </div>
      </section>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <div className="card table-wrap">
        <table className="list">
          <thead>
            <tr>
              <th>Name</th>
              <th>Camera</th>
              <th>Window</th>
              <th>Last run</th>
              <th>Enabled</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list === null && !error && (
              <tr>
                <td colSpan={6} className="muted">
                  Loading…
                </td>
              </tr>
            )}
            {list?.length === 0 && (
              <tr>
                <td colSpan={6} className="muted">
                  No scheduled exports yet.
                </td>
              </tr>
            )}
            {list?.map((s) => (
              <tr key={s.id}>
                <td>
                  <strong>{s.name}</strong>
                </td>
                <td>{nameOf(s.cameraId)}</td>
                <td className="small">{scheduleText(s)}</td>
                <td className="small">{lastRunText(s)}</td>
                <td>
                  <label className="check">
                    <input type="checkbox" checked={s.enabled} onChange={() => void toggle(s)} aria-label={`${s.name} enabled`} />
                  </label>
                </td>
                <td className="op-export-actions">
                  <button className="btn small" onClick={() => setEditing({ id: s.id, draft: draftOf(s) })}>
                    Edit
                  </button>{" "}
                  <button className="btn small danger" onClick={() => setDeleting(s)}>
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editing && (
        <ScheduleDialog
          cameras={cameras}
          initial={editing.draft}
          isNew={!editing.id}
          onClose={() => setEditing(null)}
          onSave={async (d) => {
            if (editing.id) await client.exportSchedules.update(editing.id, d);
            else await client.exportSchedules.create(d);
            notify("success", editing.id ? "Schedule saved." : "Schedule added.");
            setEditing(null);
            load();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title="Delete this schedule?"
          confirmLabel="Delete schedule"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await client.exportSchedules.remove(deleting.id);
            setDeleting(null);
            load();
          }}
        >
          {deleting.name} will stop exporting. Evidence it already filed is kept.
        </Confirm>
      )}
    </div>
  );
}

function ScheduleDialog({
  cameras,
  initial,
  isNew,
  onClose,
  onSave,
}: {
  cameras: { id: string; name: string }[];
  initial: ScheduleDraft;
  isNew: boolean;
  onClose: () => void;
  onSave: (d: ScheduleDraft) => Promise<void>;
}) {
  const [d, setD] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const blocker = scheduleBlocker(d);
  const set = (p: Partial<ScheduleDraft>) => setD((x) => ({ ...x, ...p }));
  const toggleDay = (n: number) => set({ days: d.days.includes(n) ? d.days.filter((x) => x !== n) : [...d.days, n].sort((a, b) => a - b) });
  async function submit() {
    if (blocker || busy) return;
    setBusy(true);
    setError("");
    try {
      await onSave({ ...d, name: d.name.trim() });
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  }
  return (
    <Modal
      title={isNew ? "Add scheduled export" : "Edit scheduled export"}
      onClose={onClose}
      onSubmit={() => void submit()}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={!!blocker || busy} title={blocker || undefined}>
            Save
          </button>
        </>
      }
    >
      <label className="field">
        <span>Name</span>
        <input type="text" autoFocus maxLength={100} value={d.name} onChange={(e) => set({ name: e.target.value })} />
      </label>
      <label className="field">
        <span>Camera</span>
        <select value={d.cameraId} onChange={(e) => set({ cameraId: e.target.value })}>
          <option value="">Pick a camera</option>
          {cameras.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </label>
      <div className="field">
        <span>Days the window starts on</span>
        <div className="op-toolbar">
          {dayNames.map((n, i) => (
            <label key={n} className="check">
              <input type="checkbox" checked={d.days.includes(i)} onChange={() => toggleDay(i)} />
              {n}
            </label>
          ))}
        </div>
      </div>
      <div className="op-toolbar">
        <label className="field">
          <span>From</span>
          <input type="time" value={d.start} onChange={(e) => set({ start: e.target.value })} />
        </label>
        <label className="field">
          <span>To</span>
          <input type="time" value={d.end} onChange={(e) => set({ end: e.target.value })} />
        </label>
      </div>
      <small className="muted">An end time before the start runs overnight into the next day. Up to 12 hours; exported when the window ends.</small>
      <label className="check">
        <input type="checkbox" checked={d.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
        Enabled
      </label>
      {blocker && <small className="muted">{blocker}</small>}
      {error && <div className="error">{error}</div>}
    </Modal>
  );
}
