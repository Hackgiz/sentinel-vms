// Detection events, newest first, with filters: camera (asked of the
// server), kind, threat, and "with alarm" (applied here). New events
// arrive over the event stream.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage, type ApiClient, type Event } from "../shared/api";
import { useData } from "./data";
import { EventTags } from "../shared/KindIcon";
import { useSigned } from "./session";
import type { PlaybackRequest } from "./Workspace";

export interface EventFilter {
  cameraId: string;
  kind: string;
  threat: string;
  alarmsOnly: boolean;
}

export function matches(e: Event, f: EventFilter): boolean {
  if (f.cameraId && e.cameraId !== f.cameraId) return false;
  if (f.kind && e.kind !== f.kind) return false;
  if (f.threat && (e.threat ?? "none") !== f.threat) return false;
  if (f.alarmsOnly && !e.alarmId) return false;
  return true;
}

const pageSize = 100;

/** An event's snapshot through the API (an <img src> can't carry the bearer token). */
export function Thumb({ client, event }: { client: ApiClient; event: Event }) {
  const ref = useRef<HTMLDivElement>(null);
  const [url, setUrl] = useState<string | null>(null);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === "undefined") return setVisible(true);
    const io = new IntersectionObserver(([e]) => e.isIntersecting && setVisible(true), { rootMargin: "200px" });
    io.observe(el);
    return () => io.disconnect();
  }, []);
  useEffect(() => {
    if (!visible || !event.hasThumbnail) return;
    const ac = new AbortController();
    let objectUrl = "";
    client
      .blob(`/api/v2/events/${encodeURIComponent(event.id)}/thumbnail.jpg`, ac.signal)
      .then((b) => {
        objectUrl = URL.createObjectURL(b);
        setUrl(objectUrl);
      })
      .catch(() => {});
    return () => {
      ac.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [visible, client, event.id, event.hasThumbnail]);
  return (
    <div ref={ref} className="op-thumb">
      {url ? <img src={url} alt="" /> : <div className="event-ph">{event.hasThumbnail ? "…" : "No snapshot"}</div>}
    </div>
  );
}

export default function EventsList({ onPlayback }: { onPlayback: (r: PlaybackRequest) => void }) {
  const { session } = useSigned();
  const data = useData();
  const [filter, setFilter] = useState<EventFilter>({ cameraId: "", kind: "", threat: "", alarmsOnly: false });
  const [events, setEvents] = useState<Event[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const seq = useRef(0);

  const load = useCallback(
    async (more: boolean) => {
      const n = ++seq.current;
      setLoading(true);
      setError("");
      try {
        const page = await session.client.events.page({ cameraId: filter.cameraId || undefined, cursor: more ? cursor : undefined, limit: pageSize });
        if (n !== seq.current) return;
        setEvents((ev) => (more ? [...ev, ...page.items.filter((x) => !ev.some((y) => y.id === x.id))] : page.items));
        setCursor(page.nextCursor);
      } catch (e) {
        if (n === seq.current) setError(errorMessage(e));
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    // cursor is read when loading more only
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [session.client, filter.cameraId, cursor],
  );

  useEffect(() => {
    void load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.client, filter.cameraId]);

  useEffect(
    () =>
      data.subscribe((m) => {
        if (m.type === "event.new" || m.type === "event.updated") {
          const e = m.data;
          if (filter.cameraId && e.cameraId !== filter.cameraId) return;
          setEvents((ev) => (ev.some((x) => x.id === e.id) ? ev.map((x) => (x.id === e.id ? e : x)) : [e, ...ev]));
        } else if (m.type === "lagged") {
          void load(false);
        }
      }),
    [data, filter.cameraId, load],
  );

  const kinds = useMemo(() => [...new Set(events.map((e) => e.kind))].sort(), [events]);
  const shown = events.filter((e) => matches(e, filter));
  const name = (id: string) => data.camera(id)?.name ?? "Camera";

  return (
    <div className="op-events">
      <div className="op-toolbar">
        <select className="select-inline" aria-label="Camera" value={filter.cameraId} onChange={(e) => setFilter({ ...filter, cameraId: e.target.value })}>
          <option value="">All cameras</option>
          {(data.cameras ?? []).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
        <select className="select-inline" aria-label="Kind" value={filter.kind} onChange={(e) => setFilter({ ...filter, kind: e.target.value })}>
          <option value="">All kinds</option>
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <select className="select-inline" aria-label="Threat" value={filter.threat} onChange={(e) => setFilter({ ...filter, threat: e.target.value })}>
          <option value="">Any threat level</option>
          <option value="high">High</option>
          <option value="elevated">Elevated</option>
          <option value="low">Low</option>
          <option value="none">None</option>
        </select>
        <label className="check op-inline-check">
          <input type="checkbox" checked={filter.alarmsOnly} onChange={(e) => setFilter({ ...filter, alarmsOnly: e.target.checked })} />
          With an alarm
        </label>
        <div className="grow" />
        <span className="muted small">
          {shown.length} of {events.length} loaded
        </span>
      </div>
      {error && <div className="error">{error}</div>}
      {shown.length === 0 && !loading && <div className="empty">No events match.</div>}
      <div className="event-grid">
        {shown.map((e) => (
          <button key={e.id} className={`event-card kind-${e.kind.toLowerCase()}`} onClick={() => onPlayback({ cameraId: e.cameraId, at: e.createdAt - 5000 })} title="Play the recording">
            <Thumb client={session.client} event={e} />
            <div className="event-meta">
              <strong>{e.label || e.kind}</strong>
              <EventTags event={e} />
              {e.threat && e.threat !== "none" && <span className={`badge ${e.threat === "high" ? "red" : "amber"} op-ml`}>{e.threat}</span>}
              {e.alarmId && <span className="badge red op-ml">Alarm</span>}
              <div className="muted small">
                {name(e.cameraId)} · {new Date(e.createdAt).toLocaleString()}
              </div>
              {e.description && <div className="event-desc">{e.description}</div>}
            </div>
          </button>
        ))}
      </div>
      {cursor && (
        <div className="load-more">
          <button className="btn" disabled={loading} onClick={() => void load(true)}>
            {loading ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </div>
  );
}
