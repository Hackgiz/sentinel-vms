import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, type Camera, type DetectionEvent } from "../api";
import { when } from "./AlarmsPage";

const KINDS = ["Person", "Vehicle", "Animal", "Motion"];

export default function EventsPage() {
  const [events, setEvents] = useState<DetectionEvent[]>([]);
  const [kind, setKind] = useState("");
  const [query, setQuery] = useState("");
  const [asking, setAsking] = useState(false);
  const [answer, setAnswer] = useState<{ text: string; events?: DetectionEvent[] } | null>(null);
  const [aiOn, setAIOn] = useState(false);
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [camera, setCamera] = useState("");
  const [error, setError] = useState("");
  const [zoom, setZoom] = useState<DetectionEvent | null>(null);

  const load = useCallback(() => {
    api.events(camera).then(setEvents).catch((e) => setError(e.message));
  }, [camera]);
  useEffect(() => {
    api.cameras().then(setCameras).catch(() => {});
    api
      .ai()
      .then((a) => setAIOn(a.enabled && a.hasKey))
      .catch(() => {});
  }, []);

  async function ask(e?: FormEvent) {
    e?.preventDefault();
    if (!query.trim()) return;
    setAsking(true);
    setError("");
    try {
      const r = await api.aiSearch(query.trim());
      setAnswer({ text: r.answer, events: r.events });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setAsking(false);
    }
  }

  async function digest() {
    setAsking(true);
    setError("");
    try {
      const r = await api.aiDigest("today");
      setAnswer({ text: r.digest });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setAsking(false);
    }
  }
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const name = (id: string) => cameras.find((c) => c.id === id)?.name ?? "Camera";
  const shown = kind ? events.filter((e) => e.kind === kind) : events;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Events</h1>
          <div className="sub">People, vehicles, animals and motion on your cameras, newest first. Kept for 30 days.</div>
        </div>
        <select value={camera} onChange={(e) => setCamera(e.target.value)} className="select-inline">
          <option value="">All cameras</option>
          {cameras.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </div>
      {error && <div className="error">{error}</div>}
      {aiOn && (
        <form className="ai-bar" onSubmit={ask}>
          <input type="text" value={query} onChange={(e) => setQuery(e.target.value)} placeholder='Ask about your footage — "a person in a red jacket", "white van yesterday"' />
          <button className="btn primary" disabled={asking || !query.trim()}>
            {asking ? "Thinking…" : "Search"}
          </button>
          <button type="button" className="btn" disabled={asking} onClick={digest}>
            What happened today?
          </button>
        </form>
      )}
      {answer && (
        <div className="card ai-answer">
          <div className="ai-text">{answer.text}</div>
          {answer.events && answer.events.length > 0 && (
            <div className="event-grid" style={{ marginTop: 12 }}>
              {answer.events.map((e) => (
                <EventCard key={e.id} e={e} name={name(e.cameraID)} onOpen={() => e.hasThumbnail && setZoom(e)} />
              ))}
            </div>
          )}
          <button className="btn small" style={{ marginTop: 10 }} onClick={() => setAnswer(null)}>
            Close
          </button>
        </div>
      )}
      <div className="seg kind-filter">
        <button className={kind === "" ? "on" : ""} onClick={() => setKind("")}>
          All
        </button>
        {KINDS.map((k) => (
          <button key={k} className={kind === k ? "on" : ""} onClick={() => setKind(k)}>
            {k}
          </button>
        ))}
      </div>
      {shown.length === 0 ? (
        <div className="card">
          <div className="empty">
            <div className="big">◌</div>
            {kind ? `No ${kind.toLowerCase()} events yet.` : "No motion yet."} Detection runs on every camera with motion sensitivity turned on (Cameras → Edit).
          </div>
        </div>
      ) : (
        <div className="event-grid">
          {shown.map((e) => (
            <EventCard key={e.id} e={e} name={name(e.cameraID)} onOpen={() => e.hasThumbnail && setZoom(e)} />
          ))}
        </div>
      )}
      {zoom && (
        <div className="modal-back" onMouseDown={() => setZoom(null)}>
          <div className="modal wide">
            <h2>
              {zoom.kind} · {name(zoom.cameraID)}
            </h2>
            <img src={`/api/v1/events/${zoom.id}/thumbnail.jpg`} alt="" style={{ width: "100%", borderRadius: 8 }} />
            {zoom.description && <p className="ai-text">{zoom.description}</p>}
            {zoom.threat && zoom.threat !== "none" && (
              <p>
                <span className={`badge ${zoom.threat === "high" || zoom.threat === "elevated" ? "red" : "amber"}`}>{zoom.threat} threat</span> {zoom.reason}
              </p>
            )}
            <p className="mono">
              {new Date(zoom.createdAt * 1000).toLocaleString()}
              {zoom.label && ` · ${zoom.label} ${Math.round(zoom.score * 100)}%`}
              {zoom.tags && ` · ${zoom.tags}`}
            </p>
          </div>
        </div>
      )}
    </>
  );
}

function EventCard({ e, name, onOpen }: { e: DetectionEvent; name: string; onOpen: () => void }) {
  return (
    <button className={`event-card kind-${e.kind.toLowerCase()}`} onClick={onOpen}>
      {e.hasThumbnail ? <img src={`/api/v1/events/${e.id}/thumbnail.jpg`} alt="" loading="lazy" /> : <div className="event-ph">No snapshot</div>}
      <div className="event-meta">
        <strong>{e.kind}</strong> · {name}
        {(e.threat === "elevated" || e.threat === "high") && <span className="badge red" style={{ marginLeft: 6 }}>{e.threat}</span>}
        {e.description && <div className="event-desc">{e.description}</div>}
        <div className="mono">{when(e.createdAt)}</div>
      </div>
    </button>
  );
}
