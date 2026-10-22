// Incidents: case files that tie alarms, events, evidence and notes together.
// A list on the left; on the right the chronological items, an add-note box,
// status buttons and, when AI is on, Generate report into an editable
// textarea (saved only when the operator says so).
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorMessage, type Incident, type IncidentItem, type IncidentSeverity, type IncidentStatus } from "../shared/api";
import { fmtAgo } from "../shared/time";
import { Modal } from "../admin/ui";
import { incidentSevClass, incidentStatusClass, sevClass, stateClass } from "./alarmModel";
import { useData } from "./data";
import { Thumb } from "./EventsList";
import { playbackLeadMs } from "./searchModel";
import { useSigned } from "./session";
import { PageHeader } from "../shared/Cards";
import type { PlaybackRequest } from "./Workspace";

const statuses: { id: IncidentStatus; label: string }[] = [
  { id: "open", label: "Open" },
  { id: "investigating", label: "Investigating" },
  { id: "closed", label: "Closed" },
];
const severities: IncidentSeverity[] = ["low", "medium", "high", "critical"];

export default function IncidentsView({ onPlayback, onOpenAlarm }: { onPlayback: (r: PlaybackRequest) => void; onOpenAlarm?: (alarmId: string) => void }) {
  const { session, can } = useSigned();
  const client = session.client;
  const mayAct = can("acknowledgeAlarms");
  const [status, setStatus] = useState<IncidentStatus | "">("");
  const [q, setQ] = useState("");
  const [list, setList] = useState<Incident[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const seq = useRef(0);

  const load = useCallback(
    async (more: boolean) => {
      const n = ++seq.current;
      setLoading(true);
      setError("");
      try {
        const page = await client.incidents.page({ status: status || undefined, q: q.trim() || undefined, limit: 100, before: more ? cursor : undefined });
        if (n !== seq.current) return;
        setList((cur) => (more ? [...cur, ...page.items.filter((x) => !cur.some((y) => y.id === x.id))] : page.items));
        setCursor(page.nextCursor);
      } catch (e) {
        if (n === seq.current) setError(errorMessage(e));
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    // cursor is read when loading more only
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [client, status, q],
  );

  useEffect(() => {
    const t = setTimeout(() => void load(false), 200);
    return () => clearTimeout(t);
  }, [load]);

  const patchLocal = (i: Incident) => setList((cur) => cur.map((x) => (x.id === i.id ? i : x)));

  return (
    <div className="op-panel-col">
    <PageHeader title="Incidents" subtitle={loading && !list.length ? "Loading…" : `${list.length}${cursor ? "+" : ""} incident${list.length === 1 ? "" : "s"}${status ? ` · ${status}` : ""}`} />
    <div className="op-alarms op-incidents">
      <aside className="op-alarm-list" aria-label="Incidents">
        <div className="op-alarm-list-head">
          <div className="seg">
            <button className={status === "" ? "on" : ""} onClick={() => setStatus("")}>
              All
            </button>
            {statuses.map((s) => (
              <button key={s.id} className={status === s.id ? "on" : ""} onClick={() => setStatus(s.id)}>
                {s.label}
              </button>
            ))}
          </div>
          <div className="op-toolbar">
            <input type="search" className="grow" placeholder="Search incidents" aria-label="Search incidents" value={q} onChange={(e) => setQ(e.target.value)} />
            {mayAct && (
              <button className="btn small primary" onClick={() => setCreating(true)}>
                New
              </button>
            )}
          </div>
        </div>
        {error && <div className="error op-pad">{error}</div>}
        {!list.length && !loading && !error && <div className="empty">No incidents.</div>}
        {list.map((i) => (
          <button key={i.id} className={`op-alarm ${i.id === selected ? "on" : ""}`} onClick={() => setSelected(i.id)}>
            <div className="alarm-head">
              <span className={`badge ${incidentSevClass[i.severity] ?? "blue"}`}>{i.severity}</span>
              <strong className="grow">{i.title}</strong>
              <span className={`badge ${incidentStatusClass[i.status] ?? "blue"}`}>{i.status}</span>
            </div>
            <div className="muted small">
              {i.itemCount} item{i.itemCount === 1 ? "" : "s"} · {i.createdBy} · {fmtAgo(i.updatedAt)}
            </div>
          </button>
        ))}
        {cursor && (
          <div className="load-more">
            <button className="btn" disabled={loading} onClick={() => void load(true)}>
              Load more
            </button>
          </div>
        )}
      </aside>

      <section className="op-alarm-detail">
        {selected ? (
          <IncidentDetail key={selected} id={selected} onChanged={patchLocal} onPlayback={onPlayback} onOpenAlarm={onOpenAlarm} />
        ) : (
          <div className="empty">Select an incident{mayAct ? " or start a new one" : ""}.</div>
        )}
      </section>

      {creating && (
        <NewIncident
          onClose={() => setCreating(false)}
          onCreated={(i) => {
            setCreating(false);
            setList((cur) => [i, ...cur]);
            setSelected(i.id);
          }}
        />
      )}
    </div>
    </div>
  );
}

function NewIncident({ onClose, onCreated }: { onClose: () => void; onCreated: (i: Incident) => void }) {
  const { session } = useSigned();
  const [title, setTitle] = useState("");
  const [severity, setSeverity] = useState<IncidentSeverity>("medium");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Modal
      title="New incident"
      onClose={onClose}
      onSubmit={() => {
        if (!title.trim()) return;
        setBusy(true);
        session.client.incidents
          .create(title.trim(), severity)
          .then(onCreated)
          .catch((e) => {
            setError(errorMessage(e));
            setBusy(false);
          });
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !title.trim()}>
            Create
          </button>
        </>
      }
    >
      {error && <div className="error">{error}</div>}
      <label className="field">
        <span>Title</span>
        <input type="text" maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
      </label>
      <label className="field">
        <span>Severity</span>
        <select value={severity} onChange={(e) => setSeverity(e.target.value as IncidentSeverity)}>
          {severities.map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
      </label>
    </Modal>
  );
}

function IncidentDetail({ id, onChanged, onPlayback, onOpenAlarm }: { id: string; onChanged: (i: Incident) => void; onPlayback: (r: PlaybackRequest) => void; onOpenAlarm?: (alarmId: string) => void }) {
  const { session, can } = useSigned();
  const data = useData();
  const client = session.client;
  const mayAct = can("acknowledgeAlarms");
  const [incident, setIncident] = useState<Incident | null>(null);
  const [items, setItems] = useState<IncidentItem[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const [title, setTitle] = useState("");
  const [summary, setSummary] = useState("");
  const [aiEnabled, setAiEnabled] = useState(false);
  const [drafting, setDrafting] = useState(false);

  const reload = useCallback(async () => {
    try {
      const d = await client.incidents.get(id);
      setIncident(d.incident);
      setItems(d.items);
      setTitle(d.incident.title);
      setSummary(d.incident.summary);
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [client, id]);
  useEffect(() => void reload(), [reload]);
  useEffect(() => {
    client.events
      .facets({})
      .then((f) => setAiEnabled(f.aiEnabled))
      .catch(() => {});
  }, [client]);

  const apply = (i: Incident) => {
    setIncident(i);
    onChanged(i);
  };

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const patch = (body: Parameters<typeof client.incidents.update>[1]) =>
    run(async () => {
      const i = await client.incidents.update(id, body);
      apply(i);
      if (body.summary !== undefined) setSummary(i.summary);
    });

  const addNote = () =>
    run(async () => {
      await client.incidents.addItem(id, { kind: "note", note: note.trim() });
      setNote("");
      await reload();
    });

  const generate = async () => {
    if (incident?.summary && summary === incident.summary && !window.confirm("Replace the current summary with a new AI draft?")) return;
    setDrafting(true);
    setError("");
    try {
      const i = await client.incidents.report(id);
      apply(i);
      setSummary(i.summary);
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "AI is off. Turn it on in System → AI." : errorMessage(e));
    } finally {
      setDrafting(false);
    }
  };

  if (!incident) return error ? <div className="error">{error}</div> : <div className="muted">Loading…</div>;
  const dirtySummary = summary !== incident.summary;

  return (
    <>
      <div className="alarm-head">
        <span className={`badge ${incidentStatusClass[incident.status] ?? "blue"}`}>{incident.status}</span>
        {mayAct ? (
          <input className="op-title-input grow" aria-label="Title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} onBlur={() => title.trim() && title.trim() !== incident.title && void patch({ title: title.trim() })} />
        ) : (
          <h2 className="grow">{incident.title}</h2>
        )}
        {mayAct ? (
          <select className="select-inline" aria-label="Severity" value={incident.severity} onChange={(e) => void patch({ severity: e.target.value as IncidentSeverity })}>
            {severities.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        ) : (
          <span className={`badge ${incidentSevClass[incident.severity] ?? "blue"}`}>{incident.severity}</span>
        )}
      </div>
      <div className="muted small">
        Opened by {incident.createdBy} · {new Date(incident.createdAt).toLocaleString()} · updated {fmtAgo(incident.updatedAt)}
      </div>
      {error && <div className="error op-mt">{error}</div>}

      {mayAct && (
        <div className="alarm-actions" role="group" aria-label="Status">
          {statuses.map((s) => (
            <button key={s.id} className={`btn ${incident.status === s.id ? "on" : ""}`} aria-pressed={incident.status === s.id} disabled={busy || incident.status === s.id} onClick={() => void patch({ status: s.id })}>
              {s.label}
            </button>
          ))}
        </div>
      )}

      <div className="op-kicker op-mt">Timeline ({items.length})</div>
      {items.length === 0 && <div className="muted small">Nothing here yet. Add alarms from the Alarm Manager, or write a note below.</div>}
      <ol className="op-timeline">
        {items.map((it) => (
          <li key={it.id} className="op-tl-item">
            <div className="muted small">
              {new Date(it.at).toLocaleString()} · {it.kind}
              {mayAct && (
                <button
                  className="btn small op-ml"
                  disabled={busy}
                  title="Remove from the incident"
                  onClick={() =>
                    void run(async () => {
                      await client.incidents.removeItem(id, it.id);
                      await reload();
                    })
                  }
                >
                  Remove
                </button>
              )}
            </div>
            {it.restricted ? (
              <div className="muted">Restricted: this item belongs to a camera outside your access.</div>
            ) : it.missing ? (
              <div className="muted">No longer available.</div>
            ) : it.alarm ? (
              <div>
                <span className={`badge ${sevClass[it.alarm.severity] ?? "blue"}`}>{it.alarm.severity}</span> <strong>{it.alarm.title}</strong>
                {it.alarm.cameraName && <span className="muted"> · {it.alarm.cameraName}</span>} <span className={`badge ${stateClass[it.alarm.state] ?? "blue"}`}>{it.alarm.state}</span>
                <div className="alarm-actions">
                  {onOpenAlarm && (
                    <button className="btn small" onClick={() => onOpenAlarm(it.alarm!.id)}>
                      Open in Alarm Manager
                    </button>
                  )}
                  {it.alarm.cameraId && (
                    <button className="btn small" onClick={() => onPlayback({ cameraId: it.alarm!.cameraId!, at: it.alarm!.createdAt - 10_000 })}>
                      ▶ Play recording
                    </button>
                  )}
                </div>
              </div>
            ) : it.event ? (
              <button className="event-card op-tl-event" onClick={() => onPlayback({ cameraId: it.event!.cameraId, at: it.event!.createdAt - playbackLeadMs })} title="Play the recording">
                <Thumb client={client} event={it.event} />
                <div className="event-meta">
                  <strong>{it.event.label || it.event.kind}</strong>
                  <div className="muted small">{data.camera(it.event.cameraId)?.name ?? "Camera"}</div>
                  {it.event.description && <div className="event-desc">{it.event.description}</div>}
                </div>
              </button>
            ) : it.evidence ? (
              <div>
                🔒 Evidence case <strong>{it.evidence.caseId}</strong> · {it.evidence.cameraName} · {it.evidence.range}
                <div className="mono small">SHA-256 {it.evidence.sha256}</div>
              </div>
            ) : null}
            {it.note && <div className="op-pre">{it.note}</div>}
            <div className="muted small">added by {it.addedBy}</div>
          </li>
        ))}
      </ol>

      {mayAct && (
        <form
          className="op-toolbar"
          onSubmit={(e) => {
            e.preventDefault();
            if (note.trim()) void addNote();
          }}
        >
          <input type="text" className="grow" maxLength={2000} placeholder="Add a note to the timeline" aria-label="Add a note" value={note} onChange={(e) => setNote(e.target.value)} />
          <button className="btn" disabled={busy || !note.trim()}>
            Add note
          </button>
        </form>
      )}

      <div className="op-kicker op-mt">Report</div>
      {mayAct || incident.summary ? (
        <>
          <textarea className="op-report" rows={14} aria-label="Incident summary" readOnly={!mayAct} placeholder="Write the incident summary in markdown, or generate a draft from the timeline." value={summary} onChange={(e) => setSummary(e.target.value)} />
          {mayAct && (
            <div className="alarm-actions">
              {aiEnabled && (
                <button className="btn" disabled={drafting || busy} onClick={() => void generate()}>
                  {drafting ? "Drafting…" : "Generate report"} <span className="badge blue">AI</span>
                </button>
              )}
              <button className="btn primary" disabled={busy || !dirtySummary} onClick={() => void patch({ summary })}>
                Save
              </button>
              {dirtySummary && (
                <button className="btn" onClick={() => setSummary(incident.summary)}>
                  Revert
                </button>
              )}
            </div>
          )}
        </>
      ) : (
        <div className="muted small">No report written.</div>
      )}
    </>
  );
}
