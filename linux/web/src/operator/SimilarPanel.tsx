// Appearance search results for one event ("Find similar", WAVE9(A)): the
// events showing the same-looking person or vehicle, best match first, as
// the usual event cards with a score badge. A banner explains when no
// appearance model is installed. One request per search; no polling (the
// index job's progress refreshes on demand).
import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import type { AppearanceStatus, Event, SimilarEventHit } from "../shared/api";
import { useData } from "./data";
import { EventTags } from "../shared/KindIcon";
import { kindTone } from "../shared/alarmKind";
import { Thumb } from "./EventsList";
import { eventPreRollMs } from "../shared/time";
import { useSigned } from "./session";
import { backfillLabel, defaultSimilar, scorePercent, scoreTone, similarError, similarRequest, similarWindows, unavailableText, type SimilarOptions } from "./similarModel";
import type { PlaybackRequest } from "./Workspace";

/** The "Find similar" button for an event card's corner (render it outside the card's own button). */
export function FindSimilarButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      className="btn small op-similar-btn"
      title="Find this person or vehicle elsewhere"
      onClick={(e) => {
        e.stopPropagation();
        onClick();
      }}
    >
      Find similar
    </button>
  );
}

export default function SimilarPanel({ query, onPlayback, onClose, compact }: { query: Event; onPlayback: (r: PlaybackRequest) => void; onClose: () => void; compact?: boolean }) {
  const { session, can } = useSigned();
  const client = session.client;
  const data = useData();
  const [opts, setOpts] = useState<SimilarOptions>(defaultSimilar);
  const [status, setStatus] = useState<AppearanceStatus | null>(null);
  const [hits, setHits] = useState<SimilarEventHit[] | null>(null);
  const [scanned, setScanned] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [banner, setBanner] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const name = (id: string) => data.camera(id)?.name ?? "Camera";

  const loadStatus = useCallback(async () => {
    try {
      const st = await client.appearance.status();
      setStatus(st);
      setBanner(unavailableText(st));
    } catch {
      /* the search itself reports problems */
    }
  }, [client]);

  const run = useCallback(
    async (o: SimilarOptions) => {
      const n = ++seq.current;
      setLoading(true);
      setError("");
      try {
        const res = await client.events.similar(query.id, similarRequest(query, o, Date.now()));
        if (n !== seq.current) return;
        setHits(res.items);
        setScanned(res.scanned);
        setBanner(null);
      } catch (e) {
        if (n !== seq.current) return;
        const f = similarError(e);
        setHits(null);
        if (f.banner) setBanner(f.text);
        else setError(f.text);
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    [client, query],
  );

  useEffect(() => {
    void loadStatus();
  }, [loadStatus]);
  const key = JSON.stringify(opts);
  useEffect(() => {
    void run(opts);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, run]);

  const backfill = async (start: boolean) => {
    setBusy(true);
    setError("");
    try {
      if (start) await client.appearance.startBackfill();
      else await client.appearance.cancelBackfill();
      await loadStatus();
    } catch (e) {
      setError(similarError(e).text);
    } finally {
      setBusy(false);
    }
  };

  const job = status?.backfill;
  const running = job?.state === "running";
  const set = (p: Partial<SimilarOptions>) => setOpts((o) => ({ ...o, ...p }));

  return (
    <div className={`op-similar ${compact ? "compact" : ""}`}>
      <div className="op-toolbar">
        <button className="btn small" onClick={onClose}>
          ← Back
        </button>
        <strong>Similar to this {(query.label || query.kind).toLowerCase()}</strong>
        <span className="muted small">
          {name(query.cameraId)} · {new Date(query.createdAt).toLocaleString()}
        </span>
      </div>

      {banner && (
        <div className="op-banner warn op-similar-banner" role="status">
          {banner}
        </div>
      )}

      {!banner && (
        <>
          <div className="op-similar-head">
            <div className="op-similar-query">
              <Thumb client={client} event={query} />
            </div>
            <div className="op-similar-controls">
              <div className="seg" role="group" aria-label="Time window">
                {similarWindows.map((w) => (
                  <button key={w.id} className={opts.window === w.id ? "on" : ""} aria-pressed={opts.window === w.id} onClick={() => set({ window: w.id })}>
                    {w.label}
                  </button>
                ))}
              </div>
              <label className="check op-inline-check">
                <input type="checkbox" checked={opts.sameCamera} onChange={(e) => set({ sameCamera: e.target.checked })} />
                This camera only
              </label>
              <label className="check op-inline-check">
                <input type="checkbox" checked={opts.anyKind} onChange={(e) => set({ anyKind: e.target.checked })} />
                Any kind
              </label>
              <span className="muted small">{loading ? "Searching…" : hits ? `${hits.length} match${hits.length === 1 ? "" : "es"} of ${scanned.toLocaleString("en-US")} indexed events` : ""}</span>
              {status?.available && (
                <div className="muted small op-similar-index">
                  {job ? backfillLabel(job) : `${(status.indexed ?? 0).toLocaleString("en-US")} events indexed`}
                  {can("changeSettings") && (
                    <>
                      {running ? (
                        <button className="btn small op-ml" disabled={busy} onClick={() => void backfill(false)}>
                          Cancel
                        </button>
                      ) : (
                        <button className="btn small op-ml" disabled={busy} onClick={() => void backfill(true)} title="Index the newest 50,000 events that have no appearance data yet">
                          Index recent events
                        </button>
                      )}
                      <button className="btn small op-ml" disabled={busy} onClick={() => void loadStatus()}>
                        Refresh
                      </button>
                    </>
                  )}
                </div>
              )}
            </div>
          </div>

          {error && <div className="error">{error}</div>}
          {hits && hits.length === 0 && !loading && !error && <div className="empty">No similar events in this window. Older events may not be indexed yet.</div>}
          <div className="event-grid">
            {(hits ?? []).map(({ event: e, score }) => (
              <button key={e.id} className="event-card toned" style={{ "--tone": kindTone(e.kind) } as CSSProperties} onClick={() => onPlayback({ cameraId: e.cameraId, at: e.createdAt - eventPreRollMs })} title="Play the recording">
                <Thumb client={client} event={e} />
                <div className="event-meta">
                  <strong>{e.label || e.kind}</strong>
                  <EventTags event={e} />
                  <span className={`badge ${scoreTone(score)} op-ml`} title="Appearance similarity">
                    {scorePercent(score)}% match
                  </span>
                  <div className="muted small">
                    {name(e.cameraId)} · {new Date(e.createdAt).toLocaleString()}
                  </div>
                  {e.description && <div className="event-desc">{e.description}</div>}
                </div>
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
