// The Alarm Manager: a full-page alarm workspace. State chips with counts, a
// filter bar (severity, camera, owner, time, text), a sortable list of alarm
// cards (WAVE20; it was a table) with bulk acknowledge/close/assign, and a detail drawer with the events behind
// the alarm, jump-to-playback, the response log, AI triage (advice only; the
// operator clicks Apply) and "Add to incident…".
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Modal } from "../admin/ui";
import { ApiError, errorMessage, type Alarm, type AlarmState, type Event, type Incident, type IncidentSeverity } from "../shared/api";
import { fmtAgo } from "../shared/time";
import { EventTags, KindIcon } from "../shared/KindIcon";
import { AlarmCard, PageHeader } from "../shared/Cards";
import { Icon } from "../shared/Icons";
import {
  activeCount,
  allStates,
  bulkTargets,
  emptyAlarmFilter,
  nextSort,
  pickableIncidents,
  ranges,
  severities,
  sevClass,
  sortAlarms,
  stateClass,
  toAlarmQuery,
  toCountsQuery,
  toggleSelection,
  type AlarmFilter,
  type BulkAction,
  type Sort,
  type SortKey,
} from "./alarmModel";
import { nextStates } from "./AlarmQueue";
import { useData } from "./data";
import { Thumb } from "./EventsList";
import { playbackLeadMs } from "./searchModel";
import { useSigned } from "./session";
import SimilarPanel, { FindSimilarButton } from "./SimilarPanel";
import { canFindSimilar } from "./similarModel";
import type { PlaybackRequest } from "./Workspace";

const pageSize = 100;
const toggle = <T,>(a: T[], v: T) => (a.includes(v) ? a.filter((x) => x !== v) : [...a, v]);
const incidentSeverityOf: Record<string, IncidentSeverity> = { Critical: "critical", Warning: "medium", Info: "low" };

interface Triage {
  summary: string;
  suggestedSeverity: string;
  rationale: string;
}

export default function AlarmManagerView({ onPlayback, focusAlarmId, onFocusDone }: { onPlayback: (r: PlaybackRequest) => void; focusAlarmId?: string | null; onFocusDone?: () => void }) {
  const { session, can, me } = useSigned();
  const data = useData();
  const client = session.client;
  const mayAct = can("acknowledgeAlarms");

  const [filter, setFilter] = useState<AlarmFilter>(emptyAlarmFilter);
  const [sort, setSort] = useState<Sort>({ key: "time", dir: "desc" });
  const [alarms, setAlarms] = useState<Alarm[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [anchor, setAnchor] = useState<string | null>(null);
  const [drawer, setDrawer] = useState<Alarm | null>(null);
  const [busy, setBusy] = useState(false);
  const [assignee, setAssignee] = useState("");
  const [aiEnabled, setAiEnabled] = useState(false);
  const seq = useRef(0);
  const cursorRef = useRef<string | undefined>(undefined);
  cursorRef.current = cursor;
  const filterRef = useRef(filter);
  filterRef.current = filter;

  const run = useCallback(
    async (f: AlarmFilter, more: boolean) => {
      const n = ++seq.current;
      setLoading(true);
      setError("");
      try {
        const page = await client.alarms.page({ ...toAlarmQuery(f, Date.now()), limit: pageSize, before: more ? cursorRef.current : undefined });
        if (n !== seq.current) return;
        setAlarms((cur) => (more ? [...cur, ...page.items.filter((x) => !cur.some((y) => y.id === x.id))] : page.items));
        setCursor(page.nextCursor);
        setLoaded(true);
      } catch (e) {
        if (n === seq.current) setError(errorMessage(e));
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    [client],
  );

  const loadCounts = useCallback(
    async (f: AlarmFilter) => {
      try {
        setCounts((await client.alarms.counts(toCountsQuery(f, Date.now()))).counts);
      } catch {
        /* chips just lose their counts */
      }
    },
    [client],
  );

  // Any change to the bar loads again (text is debounced).
  const key = JSON.stringify(filter);
  useEffect(() => {
    const t = setTimeout(() => {
      setSelected(new Set());
      void run(filter, false);
    }, 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, run]);
  const countKey = JSON.stringify(toCountsQuery(filter, 0));
  useEffect(() => {
    void loadCounts(filter);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [countKey, loadCounts]);

  // Whether to offer Triage: the facets say whether AI is on.
  useEffect(() => {
    client.events
      .facets({})
      .then((f) => setAiEnabled(f.aiEnabled))
      .catch(() => {});
  }, [client]);

  // The event stream keeps the table current: an updated alarm is replaced in
  // place, a new one (or a resync) reloads the first page, both refresh the counts.
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const later = (reload: boolean) => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        if (reload) void run(filterRef.current, false);
        void loadCounts(filterRef.current);
      }, 600);
    };
    const off = data.subscribe((m) => {
      if (m.type === "alarm.updated") {
        setAlarms((cur) => cur.map((a) => (a.id === m.data.id ? m.data : a)));
        setDrawer((d) => (d && d.id === m.data.id ? m.data : d));
        later(false);
      } else if (m.type === "alarm.new" || m.type === "lagged") {
        later(true);
      }
    });
    return () => {
      off();
      clearTimeout(timer);
    };
  }, [data, run, loadCounts]);

  // Deep link from the live queue: show every state and open that alarm.
  useEffect(() => {
    if (!focusAlarmId) return;
    let alive = true;
    client.alarms
      .get(focusAlarmId)
      .then((a) => {
        if (!alive) return;
        setFilter({ ...emptyAlarmFilter, states: [] });
        setDrawer(a);
      })
      .catch((e) => alive && setError(errorMessage(e)))
      .finally(() => alive && onFocusDone?.());
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusAlarmId, client]);

  const rows = useMemo(() => sortAlarms(alarms, sort), [alarms, sort]);
  const order = useMemo(() => rows.map((a) => a.id), [rows]);
  const set = (p: Partial<AlarmFilter>) => setFilter((f) => ({ ...f, ...p }));
  const name = (id?: string) => (id ? (data.camera(id)?.name ?? "Camera") : "Site");

  const putAlarm = useCallback(
    (a: Alarm) => {
      data.putAlarm(a);
      setAlarms((cur) => cur.map((x) => (x.id === a.id ? a : x)));
      setDrawer((d) => (d && d.id === a.id ? a : d));
    },
    [data],
  );

  async function bulk(action: BulkAction) {
    const { ids, state } = bulkTargets(action, alarms, selected);
    if (!ids.length) return;
    setBusy(true);
    setError("");
    const results = await Promise.allSettled(ids.map((id) => client.alarms.setState(id, state)));
    results.forEach((r) => r.status === "fulfilled" && putAlarm(r.value));
    const failed = results.filter((r) => r.status === "rejected").length;
    if (failed) setError(`${failed} of ${ids.length} alarms could not be changed.`);
    setSelected(new Set());
    setBusy(false);
    void run(filter, false);
    void loadCounts(filter);
  }

  async function bulkAssign(owner: string) {
    const ids = [...selected];
    if (!ids.length) return;
    setBusy(true);
    setError("");
    const results = await Promise.allSettled(ids.map((id) => client.alarms.assign(id, owner)));
    results.forEach((r) => r.status === "fulfilled" && putAlarm(r.value));
    const failed = results.filter((r): r is PromiseRejectedResult => r.status === "rejected");
    if (failed.length) setError(errorMessage(failed[0].reason));
    setSelected(new Set());
    setBusy(false);
  }

  const sortButton = (k: SortKey, label: string) => (
    <button key={k} className={`btn small ${sort.key === k ? "on" : ""}`} aria-pressed={sort.key === k} onClick={() => setSort((s) => nextSort(s, k))} title={`Sort by ${label.toLowerCase()}`}>
      {label}
      {sort.key === k && <span aria-hidden="true">{sort.dir === "asc" ? " ▲" : " ▼"}</span>}
    </button>
  );

  // One click acts on the alarm (the drawer has the note, assign and the rest).
  async function quick(a: Alarm, state: AlarmState) {
    setBusy(true);
    setError("");
    try {
      putAlarm(await client.alarms.setState(a.id, state));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const eligibleAck = bulkTargets("acknowledge", alarms, selected).ids.length;
  const eligibleClose = bulkTargets("close", alarms, selected).ids.length;

  const newCount = data.alarms.filter((a) => a.state === "New").length;
  return (
    <div className="op-events op-am">
      <PageHeader title="Alarm Manager" subtitle={`${data.alarms.length} open · ${newCount} new`} dot={newCount > 0 ? "var(--red)" : data.alarms.length > 0 ? "var(--amber)" : "var(--green)"}>
        <span className="muted small">{loading && !alarms.length ? "Loading…" : `${alarms.length}${cursor ? "+" : ""} alarm${alarms.length === 1 ? "" : "s"} shown`}</span>
      </PageHeader>
      <div className="op-toolbar op-chips" role="group" aria-label="State">
        {allStates.map((s) => (
          <button key={s} className={`btn small ${filter.states.includes(s) ? "on" : ""}`} aria-pressed={filter.states.includes(s)} onClick={() => set({ states: toggle(filter.states, s) })}>
            {s} <span className="muted">{counts[s] ?? 0}</span>
          </button>
        ))}
        <button className="btn small" onClick={() => set({ states: [] })} title="Show every state">
          All states
        </button>
      </div>

      <div className="op-toolbar" role="group" aria-label="Filters">
        {severities.map((s) => (
          <button key={s} className={`btn small ${filter.severities.includes(s) ? "on" : ""}`} aria-pressed={filter.severities.includes(s)} onClick={() => set({ severities: toggle(filter.severities, s) })}>
            {s}
          </button>
        ))}
        <details className="op-camsel">
          <summary className="btn small">{filter.cameras.length ? `${filter.cameras.length} camera${filter.cameras.length > 1 ? "s" : ""}` : "All cameras"}</summary>
          <div className="op-menu op-camsel-list">
            {(data.cameras ?? []).map((c) => (
              <label key={c.id} className="check">
                <input type="checkbox" checked={filter.cameras.includes(c.id)} onChange={() => set({ cameras: toggle(filter.cameras, c.id) })} />
                {c.name}
              </label>
            ))}
            {filter.cameras.length > 0 && (
              <button className="btn small" onClick={() => set({ cameras: [] })}>
                All cameras
              </button>
            )}
          </div>
        </details>
        <select className="select-inline" aria-label="Owner" value={filter.owner === "" || filter.owner === "unassigned" || filter.owner === me.name ? filter.owner : "other"} onChange={(e) => set({ owner: e.target.value === "other" ? filter.owner : e.target.value })}>
          <option value="">Any owner</option>
          <option value="unassigned">Unassigned</option>
          <option value={me.name}>Mine</option>
          {filter.owner && filter.owner !== "unassigned" && filter.owner !== me.name && <option value="other">{filter.owner}</option>}
        </select>
        <select className="select-inline" aria-label="Time range" value={filter.range} onChange={(e) => set({ range: e.target.value as AlarmFilter["range"], from: filter.from || Date.now() - 24 * 3600e3 })}>
          {ranges.map((r) => (
            <option key={r.id} value={r.id}>
              {r.label}
            </option>
          ))}
          <option value="custom">Custom…</option>
        </select>
        {filter.range === "custom" && (
          <>
            <input type="datetime-local" aria-label="From" value={localInput(filter.from)} onChange={(e) => set({ from: e.target.value ? new Date(e.target.value).getTime() : 0 })} />
            <span className="muted">to</span>
            <input type="datetime-local" aria-label="To" value={localInput(filter.to)} onChange={(e) => set({ to: e.target.value ? new Date(e.target.value).getTime() : 0 })} />
          </>
        )}
        <input type="search" className="op-search-q" placeholder="Title or detail" aria-label="Title or detail" value={filter.q} onChange={(e) => set({ q: e.target.value })} />
        {activeCount(filter) > 0 && (
          <button className="btn small" onClick={() => set({ ...emptyAlarmFilter, states: filter.states })}>
            Clear filters
          </button>
        )}
      </div>

      {mayAct && selected.size > 0 && (
        <div className="op-toolbar op-bulk" role="group" aria-label="Bulk actions">
          <strong>{selected.size} selected</strong>
          <button className="btn small primary" disabled={busy || !eligibleAck} onClick={() => void bulk("acknowledge")}>
            Acknowledge{eligibleAck !== selected.size ? ` (${eligibleAck})` : ""}
          </button>
          <button className="btn small" disabled={busy || !eligibleClose} onClick={() => void bulk("close")}>
            Close{eligibleClose !== selected.size ? ` (${eligibleClose})` : ""}
          </button>
          <input type="text" className="op-search-q" placeholder="Assign to user…" aria-label="Assign to" value={assignee} onChange={(e) => setAssignee(e.target.value)} />
          <button className="btn small" disabled={busy || !assignee.trim()} onClick={() => void bulkAssign(assignee.trim())}>
            Assign
          </button>
          <button className="btn small" disabled={busy} onClick={() => void bulkAssign(me.name)}>
            Assign to me
          </button>
          <button className="btn small" onClick={() => setSelected(new Set())}>
            Clear selection
          </button>
        </div>
      )}

      {error && <div className="error">{error}</div>}
      {loaded && !alarms.length && !loading && !error && <div className="empty">No alarms match these filters.</div>}

      {alarms.length > 0 && (
        <>
          <div className="op-am-sort" role="group" aria-label="Sort">
            <label className="op-am-all">
              <input
                type="checkbox"
                aria-label="Select all shown"
                checked={selected.size > 0 && selected.size === rows.length}
                onChange={(e) => setSelected(e.target.checked ? new Set(order) : new Set())}
              />
              All
            </label>
            <span className="muted small">Sort</span>
            {sortButton("severity", "Severity")}
            {sortButton("state", "State")}
            {sortButton("camera", "Camera")}
            {sortButton("owner", "Owner")}
            {sortButton("time", "Raised")}
            {sortButton("events", "Events")}
          </div>
          <div className="op-card-list op-am-cards" role="list" aria-label="Alarms">
            {rows.map((a) => {
              const primary = nextStates(a.state).find((n) => n.primary);
              return (
                <div key={a.id} role="listitem">
                  <AlarmCard
                    alarm={a}
                    selected={drawer?.id === a.id}
                    attention={a.state === "New"}
                    onOpen={() => setDrawer(a)}
                    leading={
                      <input
                        type="checkbox"
                        aria-label={`Select ${a.title}`}
                        checked={selected.has(a.id)}
                        onChange={() => undefined}
                        onClick={(e) => {
                          e.stopPropagation();
                          setSelected((s) => toggleSelection(s, order, a.id, anchor, e.shiftKey));
                          setAnchor(a.id);
                        }}
                      />
                    }
                    meta={
                      <span title={new Date(a.createdAt).toLocaleString()}>
                        {a.cameraName || name(a.cameraId)} · {fmtAgo(a.createdAt)} · {a.eventCount} event{a.eventCount === 1 ? "" : "s"}
                        {a.owner && ` · ${a.owner}`}
                      </span>
                    }
                    actions={
                      <>
                        {mayAct && primary && (
                          <button className="btn small primary" disabled={busy} onClick={() => void quick(a, primary.state)}>
                            {primary.label}
                          </button>
                        )}
                        {a.cameraId && (
                          <button className="btn small" onClick={() => onPlayback({ cameraId: a.cameraId!, at: a.createdAt - 10_000 })}>
                            <Icon name="play" size={12} /> Play recording
                          </button>
                        )}
                        <button className="btn small" onClick={() => setDrawer(a)}>
                          Details
                        </button>
                      </>
                    }
                  />
                </div>
              );
            })}
          </div>
        </>
      )}
      {cursor && (
        <div className="load-more">
          <button className="btn" disabled={loading} onClick={() => void run(filter, true)}>
            {loading ? "Loading…" : "Load more"}
          </button>
        </div>
      )}

      {drawer && <AlarmDrawer key={drawer.id} alarm={drawer} aiEnabled={aiEnabled} onClose={() => setDrawer(null)} onChange={putAlarm} onPlayback={onPlayback} />}
    </div>
  );
}

/** datetime-local value of a Unix-ms time ("" when open). */
function localInput(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60e3);
  return d.toISOString().slice(0, 16);
}

function AlarmDrawer({ alarm, aiEnabled, onClose, onChange, onPlayback }: { alarm: Alarm; aiEnabled: boolean; onClose: () => void; onChange: (a: Alarm) => void; onPlayback: (r: PlaybackRequest) => void }) {
  const { session, can, me } = useSigned();
  const data = useData();
  const client = session.client;
  const mayAct = can("acknowledgeAlarms");
  const [events, setEvents] = useState<Event[] | null>(null);
  const [similarTo, setSimilarTo] = useState<Event | null>(null); // WAVE9(A): appearance search
  const [note, setNote] = useState("");
  const [owner, setOwner] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [triage, setTriage] = useState<Triage | null>(null);
  const [triaging, setTriaging] = useState(false);
  const [picker, setPicker] = useState(false);
  const [added, setAdded] = useState("");

  useEffect(() => {
    let alive = true;
    client.alarms
      .events(alarm.id)
      .then((e) => alive && setEvents(e))
      .catch(() => alive && setEvents([]));
    return () => {
      alive = false;
    };
  }, [client, alarm.id, alarm.eventCount]);
  useEffect(() => setSimilarTo(null), [alarm.id]);

  async function act(fn: () => Promise<Alarm>) {
    setBusy(true);
    setError("");
    try {
      onChange(await fn());
      setNote("");
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  async function runTriage() {
    setTriaging(true);
    setError("");
    try {
      setTriage(await client.alarms.triage(alarm.id));
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "AI is off. Turn it on in System → AI." : errorMessage(e));
    } finally {
      setTriaging(false);
    }
  }

  const camera = alarm.cameraId ? data.camera(alarm.cameraId) : undefined;
  const suggestion = triage?.suggestedSeverity;
  return (
    <aside className="op-drawer" aria-label={`Alarm: ${alarm.title}`}>
      <div className="alarm-head">
        <span className={`badge ${sevClass[alarm.severity] ?? "blue"}`}>{alarm.severity}</span>
        <h2 className="grow">
          <KindIcon kind={alarm.kind} />
          {alarm.title}
        </h2>
        <span className={`badge ${stateClass[alarm.state] ?? "blue"}`}>{alarm.state}</span>
        <button className="btn small" onClick={onClose} aria-label="Close details">
          ✕
        </button>
      </div>
      {alarm.detail && <div className="alarm-detail">{alarm.detail}</div>}
      <div className="muted small">
        {alarm.cameraName || camera?.name || "Site"} · {alarm.kind} · raised {new Date(alarm.createdAt).toLocaleString()} · owner {alarm.owner}
      </div>

      {alarm.instructions && alarm.instructions.length > 0 && (
        <div className="op-instructions" role="note">
          <div className="op-kicker">Instructions</div>
          {alarm.instructions.map((i, n) => (
            <div key={n} className="op-instruction">
              <strong>{i.rule}</strong>
              <div className="op-pre">{i.text}</div>
            </div>
          ))}
        </div>
      )}

      {error && <div className="error op-mt">{error}</div>}

      <div className="alarm-actions">
        {alarm.cameraId && (
          <button className="btn" onClick={() => onPlayback({ cameraId: alarm.cameraId!, at: alarm.createdAt - 10_000 })}>
            ▶ Play recording
          </button>
        )}
        {mayAct && aiEnabled && (
          <button className="btn" disabled={triaging} onClick={() => void runTriage()}>
            {triaging ? "Thinking…" : "Triage"} <span className="badge blue">AI</span>
          </button>
        )}
        {mayAct && (
          <button className="btn" onClick={() => setPicker(true)}>
            Add to incident…
          </button>
        )}
      </div>
      {added && <div className="notice op-mt">{added}</div>}

      {triage && (
        <div className="op-triage" role="note">
          <div className="op-kicker">
            AI triage <span className="badge blue">AI</span>
          </div>
          <p>{triage.summary}</p>
          {triage.rationale && <p className="muted small">{triage.rationale}</p>}
          {suggestion && (
            <div className="alarm-actions">
              <span>
                Suggested severity <span className={`badge ${sevClass[suggestion] ?? "blue"}`}>{suggestion}</span>
              </span>
              {suggestion === alarm.severity ? (
                <span className="muted small">already set</span>
              ) : (
                <button className="btn small primary" disabled={busy || !mayAct} onClick={() => void act(() => client.alarms.setSeverity(alarm.id, suggestion as "Critical" | "Warning" | "Info"))}>
                  Apply
                </button>
              )}
            </div>
          )}
        </div>
      )}

      {mayAct && (
        <>
          <label className="field op-mt">
            <span>Note (goes into the response log)</span>
            <input type="text" maxLength={500} value={note} onChange={(e) => setNote(e.target.value)} placeholder="What you saw or did" />
          </label>
          <div className="alarm-actions">
            {nextStates(alarm.state).map((n) => (
              <button key={n.state} className={`btn ${n.primary ? "primary" : ""}`} disabled={busy} onClick={() => void act(() => client.alarms.setState(alarm.id, n.state as AlarmState, note.trim() || undefined, n.state === "Snoozed" ? 15 : undefined))}>
                {n.label}
                {n.state === "Snoozed" && " 15 min"}
              </button>
            ))}
          </div>
          <div className="alarm-actions">
            <input type="text" className="op-search-q" placeholder="Assign to user…" aria-label="Assign to" value={owner} onChange={(e) => setOwner(e.target.value)} />
            <button className="btn small" disabled={busy || !owner.trim()} onClick={() => void act(() => client.alarms.assign(alarm.id, owner.trim()))}>
              Assign
            </button>
            <button className="btn small" disabled={busy} onClick={() => void act(() => client.alarms.assign(alarm.id, me.name))}>
              Assign to me
            </button>
          </div>
        </>
      )}

      <div className="op-kicker op-mt">Events{events ? ` (${events.length})` : ""}</div>
      {events && events.length === 0 && <div className="muted small">No detection events are attached to this alarm.</div>}
      {similarTo ? (
        <SimilarPanel query={similarTo} onPlayback={onPlayback} onClose={() => setSimilarTo(null)} compact />
      ) : (
        <div className="event-grid op-drawer-events">
          {(events ?? []).map((e) => (
            <div key={e.id} className="op-card-wrap">
              <button className={`event-card kind-${e.kind.toLowerCase()}`} onClick={() => onPlayback({ cameraId: e.cameraId, at: e.createdAt - playbackLeadMs })} title="Play the recording">
                <Thumb client={client} event={e} />
                <div className="event-meta">
                  <strong>{e.label || e.kind}</strong>
                  <EventTags event={e} />
                  {e.threat && e.threat !== "none" && <span className={`badge ${e.threat === "high" ? "red" : "amber"} op-ml`}>{e.threat}</span>}
                  <div className="muted small">{new Date(e.createdAt).toLocaleTimeString()}</div>
                  {e.description && <div className="event-desc">{e.description}</div>}
                </div>
              </button>
              {canFindSimilar(e) && <FindSimilarButton onClick={() => setSimilarTo(e)} />}
            </div>
          ))}
        </div>
      )}

      {alarm.responseLog.length > 0 && (
        <>
          <div className="op-kicker op-mt">Response log</div>
          <ul className="response-log">
            {alarm.responseLog.map((l, i) => (
              <li key={i}>{l}</li>
            ))}
          </ul>
        </>
      )}

      {picker && (
        <IncidentPicker
          alarm={alarm}
          onClose={() => setPicker(false)}
          onAdded={(title) => {
            setPicker(false);
            setAdded(`Added to incident “${title}”.`);
          }}
        />
      )}
    </aside>
  );
}

/** Pick an open incident to put an alarm in, or start a new one. */
function IncidentPicker({ alarm, onClose, onAdded }: { alarm: Alarm; onClose: () => void; onAdded: (incidentTitle: string) => void }) {
  const { session } = useSigned();
  const client = session.client;
  const [incidents, setIncidents] = useState<Incident[] | null>(null);
  const [title, setTitle] = useState(alarm.title + (alarm.cameraName ? ` · ${alarm.cameraName}` : ""));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    client.incidents
      .page({ limit: 200 })
      .then((p) => setIncidents(pickableIncidents(p.items)))
      .catch((e) => setError(errorMessage(e)));
  }, [client]);

  async function add(inc: Incident | null) {
    setBusy(true);
    setError("");
    try {
      const target = inc ?? (await client.incidents.create(title.trim(), incidentSeverityOf[alarm.severity] ?? "medium"));
      await client.incidents.addItem(target.id, { kind: "alarm", refId: alarm.id });
      onAdded(target.title);
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? "That alarm is already in the incident." : errorMessage(e));
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Add to incident"
      onClose={onClose}
      onSubmit={() => title.trim() && void add(null)}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !title.trim()}>
            Create new incident
          </button>
        </>
      }
    >
      {error && <div className="error">{error}</div>}
      {incidents === null && !error && <div className="muted">Loading…</div>}
      {incidents && incidents.length > 0 && (
        <div className="op-picker">
          <div className="op-kicker">Existing incidents</div>
          {incidents.map((i) => (
            <button type="button" key={i.id} className="op-alarm" disabled={busy} onClick={() => void add(i)}>
              <strong>{i.title}</strong>
              <div className="muted small">
                {i.status} · {i.itemCount} item{i.itemCount === 1 ? "" : "s"}
              </div>
            </button>
          ))}
        </div>
      )}
      <label className="field">
        <span>Or start a new one</span>
        <input type="text" maxLength={200} value={title} onChange={(e) => setTitle(e.target.value)} />
      </label>
    </Modal>
  );
}
