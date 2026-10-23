import { useCallback, useEffect, useState, type CSSProperties, type FormEvent } from "react";
import { api, type Camera, type DetectionEvent } from "../api";
import { when } from "./AlarmsPage";
import { Chip, EventCard, PageHeader } from "../shared/Cards"; // WAVE21
import { Icon } from "../shared/Icons";
import { eventKinds, kindGlyph, kindLabel, kindMatches, kindTone, tones } from "../shared/alarmKind";
import { eventPlaybackLink } from "../dashboardNav";

/** PARITY-342 §3: jump to the event's recording (Live page, playback mode, 5 s early). */
const playEvent = (e: DetectionEvent) => {
  window.location.hash = eventPlaybackLink(e);
};

/** A card opens the snapshot when there is one, else the recording. */
const openEvent = (e: DetectionEvent, zoom: (e: DetectionEvent) => void) => (e.hasThumbnail ? zoom(e) : playEvent(e));

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
  const shown = kind ? events.filter((e) => kindMatches(e.kind, kind)) : events;

  return (
    <>
      <PageHeader title="Events" subtitle="People, vehicles, animals and motion on your cameras, newest first. Kept for 30 days.">
        <select value={camera} onChange={(e) => setCamera(e.target.value)} className="select-inline">
          <option value="">All cameras</option>
          {cameras.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </PageHeader>
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
            <div className="op-event-grid">
              {answer.events.map((e) => (
                <DashEventCard key={e.id} e={e} name={name(e.cameraID)} onOpen={() => openEvent(e, setZoom)} />
              ))}
            </div>
          )}
          <button className="btn small" style={{ marginTop: 10 }} onClick={() => setAnswer(null)}>
            Close
          </button>
        </div>
      )}
      <div className="dash-kinds" role="group" aria-label="Kind">
        <button className={`op-filter-chip ${kind === "" ? "on" : ""}`} aria-pressed={kind === ""} onClick={() => setKind("")}>
          All
        </button>
        {eventKinds.map((k) => (
          <button key={k} className={`op-filter-chip ${kind === k ? "on" : ""}`} style={{ "--tone": kindTone(k) } as CSSProperties} aria-pressed={kind === k} onClick={() => setKind(k)}>
            <Icon name={kindGlyph(k)} size={12} />
            {kindLabel(k)}
          </button>
        ))}
      </div>
      {shown.length === 0 ? (
        <div className="card">
          <div className="empty">
            <div className="big">◌</div>
            {kind ? `No ${kindLabel(kind).toLowerCase()} events yet.` : "No motion yet."} Detection runs on every camera with motion sensitivity turned on (Cameras → Edit).
          </div>
        </div>
      ) : (
        <div className="op-event-grid">
          {shown.map((e) => (
            <DashEventCard key={e.id} e={e} name={name(e.cameraID)} onOpen={() => openEvent(e, setZoom)} />
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
            <div className="actions">
              <button className="btn primary" onClick={() => playEvent(zoom)}>
                ▶ Play recording
              </button>
              <button className="btn" onClick={() => setZoom(null)}>
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function DashEventCard({ e, name, onOpen }: { e: DetectionEvent; name: string; onOpen: () => void }) {
  const threat = e.threat === "elevated" || e.threat === "high";
  return (
    <EventCard
      kind={e.kind}
      time={when(e.createdAt)}
      title={name}
      badges={threat ? <Chip tone={tones.red}>{e.threat}</Chip> : undefined}
      description={e.description}
      onOpen={onOpen}
      openTitle={e.hasThumbnail ? "Show the snapshot" : "Play the recording"}
      media={
        e.hasThumbnail ? (
          <img className="dash-thumb" src={`/api/v1/events/${e.id}/thumbnail.jpg`} alt="" loading="lazy" />
        ) : (
          <span className="dash-thumb-ph" style={{ color: kindTone(e.kind) }}>
            <Icon name={kindGlyph(e.kind)} size={22} />
          </span>
        )
      }
    />
  );
}
