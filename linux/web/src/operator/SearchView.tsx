// Forensic search: filter the detection events (camera, time, kind, label
// text, threat) with facet counts, page through the results on a cursor, and
// click one to open playback at that moment. When AI is on, a question box
// translates plain English into the filter bar.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { emitNotice } from "./notices";
import { ApiError, errorMessage, type Event, type EventFacets } from "../shared/api";
import { useData } from "./data";
import { Thumb } from "./EventsList";
import { activeCount, emptySearch, fromFilter, playbackLeadMs, presets, toQuery, windowOf, type SearchState } from "./searchModel";
import { useSigned } from "./session";
import SimilarPanel, { FindSimilarButton } from "./SimilarPanel";
import { canFindSimilar } from "./similarModel";
import type { PlaybackRequest } from "./Workspace";

const pageSize = 60;

const toggle = (a: string[], v: string) => (a.includes(v) ? a.filter((x) => x !== v) : [...a, v]);

/** datetime-local value of a Unix-ms time ("" when open). */
function localInput(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms - new Date(ms).getTimezoneOffset() * 60e3);
  return d.toISOString().slice(0, 16);
}

export default function SearchView({ onPlayback }: { onPlayback: (r: PlaybackRequest) => void }) {
  const { session } = useSigned();
  const data = useData();
  const [state, setState] = useState<SearchState>(emptySearch);
  const [events, setEvents] = useState<Event[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [facets, setFacets] = useState<EventFacets | null>(null);
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [error, setError] = useState("");
  const [question, setQuestion] = useState("");
  const [asking, setAsking] = useState(false);
  const [understood, setUnderstood] = useState("");
  const [similarTo, setSimilarTo] = useState<Event | null>(null);
  const seq = useRef(0);
  const sentinel = useRef<HTMLDivElement>(null);
  const cursorRef = useRef<string | undefined>(undefined);
  cursorRef.current = cursor;
  const aiEnabled = facets?.aiEnabled ?? false;

  const run = useCallback(
    async (s: SearchState, more: boolean) => {
      const n = ++seq.current;
      setLoading(true);
      setError("");
      try {
        const q = toQuery(s, Date.now());
        const page = await session.client.events.search({ ...q, limit: pageSize, before: more ? cursorRef.current : undefined });
        if (n !== seq.current) return;
        setEvents((ev) => (more ? [...ev, ...page.items.filter((x) => !ev.some((y) => y.id === x.id))] : page.items));
        setCursor(page.nextCursor);
        setSearched(true);
      } catch (e) {
        if (n === seq.current) setError(errorMessage(e));
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    [session.client],
  );

  const loadFacets = useCallback(
    async (s: SearchState) => {
      const { from, to } = windowOf(s, Date.now());
      try {
        setFacets(await session.client.events.facets({ cameras: s.cameras.length ? s.cameras.join(",") : undefined, from: from || undefined, to: to || undefined }));
      } catch {
        /* chips just lose their counts */
      }
    },
    [session.client],
  );

  // Any change to the bar searches again (text is debounced).
  const key = JSON.stringify(state);
  useEffect(() => {
    const t = setTimeout(() => void run(state, false), 250);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, run]);
  const facetKey = JSON.stringify([state.range, state.from, state.to, state.cameras]);
  useEffect(() => {
    void loadFacets(state);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [facetKey, loadFacets]);

  // Infinite scroll: the sentinel under the grid asks for the next page.
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !cursor || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver(([e]) => e.isIntersecting && !loading && void run(state, true), { rootMargin: "400px" });
    io.observe(el);
    return () => io.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cursor, loading, run]);

  const ask = async () => {
    if (!question.trim() || asking) return;
    setAsking(true);
    setError("");
    try {
      const res = await session.client.events.ask(question.trim());
      const next = fromFilter(res.filter);
      seq.current++; // the answer is the first page; drop any search in flight
      setState(next);
      setEvents(res.items);
      setCursor(res.nextCursor);
      setSearched(true);
      setUnderstood(question.trim());
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) emitNotice({ kind: "warn", key: "ai-limit", text: "The AI daily limit has been reached. Searches by filter still work." });
      setError(e instanceof ApiError && e.status === 409 ? "AI is off. Turn it on in System → AI." : errorMessage(e));
    } finally {
      setAsking(false);
    }
  };

  const kindCounts = useMemo(() => new Map((facets?.kinds ?? []).map((c) => [c.value, c.count])), [facets]);
  // Selected kinds stay visible even when the current window has none of them.
  const kinds = useMemo(() => [...new Set([...(facets?.kinds ?? []).map((c) => c.value), ...state.kinds])], [facets, state.kinds]);
  const labels = (facets?.labels ?? []).slice(0, 12);
  const threatTotal = (facets?.threats ?? []).filter((c) => c.value === "elevated" || c.value === "high").reduce((n, c) => n + c.count, 0);
  const name = (id: string) => data.camera(id)?.name ?? "Camera";
  const set = (p: Partial<SearchState>) => setState((s) => ({ ...s, ...p }));
  const clear = () => (setState({ ...emptySearch, range: state.range }), setUnderstood(""));

  // "Find similar" swaps the results for that event's appearance matches;
  // Back returns to the filtered results as they were.
  if (similarTo)
    return (
      <div className="op-events op-search">
        <SimilarPanel query={similarTo} onPlayback={onPlayback} onClose={() => setSimilarTo(null)} />
      </div>
    );

  return (
    <div className="op-events op-search">
      {aiEnabled && (
        <form
          className="op-toolbar op-ask"
          onSubmit={(e) => {
            e.preventDefault();
            void ask();
          }}
        >
          <input
            type="search"
            className="grow"
            placeholder="Ask in plain English: a person in a red jacket at the front door last night"
            aria-label="Ask in plain English"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
          />
          <button className="btn primary" disabled={asking || !question.trim()}>
            {asking ? "Thinking…" : "Ask"}
          </button>
        </form>
      )}
      {understood && (
        <div className="muted small op-pad">
          Understood “{understood}” as the filters below. Adjust them to refine.
        </div>
      )}

      <div className="op-toolbar" role="group" aria-label="Time range">
        <div className="seg">
          {presets.map((p) => (
            <button key={p.id} className={state.range === p.id ? "on" : ""} aria-pressed={state.range === p.id} onClick={() => set({ range: p.id })}>
              {p.label}
            </button>
          ))}
          <button className={state.range === "custom" ? "on" : ""} aria-pressed={state.range === "custom"} onClick={() => set({ range: "custom", from: state.from || Date.now() - 24 * 3600e3 })}>
            Custom
          </button>
        </div>
        {state.range === "custom" && (
          <>
            <input
              type="datetime-local"
              aria-label="From"
              value={localInput(state.from)}
              onChange={(e) => set({ from: e.target.value ? new Date(e.target.value).getTime() : 0 })}
            />
            <span className="muted">to</span>
            <input type="datetime-local" aria-label="To" value={localInput(state.to)} onChange={(e) => set({ to: e.target.value ? new Date(e.target.value).getTime() : 0 })} />
          </>
        )}
        <details className="op-camsel">
          <summary className="btn small">{state.cameras.length ? `${state.cameras.length} camera${state.cameras.length > 1 ? "s" : ""}` : "All cameras"}</summary>
          <div className="op-menu op-camsel-list">
            {(data.cameras ?? []).map((c) => (
              <label key={c.id} className="check">
                <input type="checkbox" checked={state.cameras.includes(c.id)} onChange={() => set({ cameras: toggle(state.cameras, c.id) })} />
                {c.name}
              </label>
            ))}
            {state.cameras.length > 0 && (
              <button className="btn small" onClick={() => set({ cameras: [] })}>
                All cameras
              </button>
            )}
          </div>
        </details>
        <input type="search" className="op-search-q" placeholder="Label, description or tag" aria-label="Label, description or tag" value={state.q} onChange={(e) => set({ q: e.target.value })} />
        <button className={`btn small ${state.threatOnly ? "on" : ""}`} aria-pressed={state.threatOnly} onClick={() => set({ threatOnly: !state.threatOnly, threats: [] })}>
          Elevated or high threat{facets ? ` (${threatTotal})` : ""}
        </button>
        {activeCount(state) > 0 && (
          <button className="btn small" onClick={clear}>
            Clear filters
          </button>
        )}
      </div>

      <div className="op-toolbar op-chips" role="group" aria-label="Kind">
        {kinds.map((k) => (
          <button key={k} className={`btn small ${state.kinds.includes(k) ? "on" : ""}`} aria-pressed={state.kinds.includes(k)} onClick={() => set({ kinds: toggle(state.kinds, k) })}>
            {k} <span className="muted">{kindCounts.get(k) ?? 0}</span>
          </button>
        ))}
        {labels.map((l) => (
          <button key={l.value} className={`btn small ${state.labels.includes(l.value) ? "on" : ""}`} aria-pressed={state.labels.includes(l.value)} onClick={() => set({ labels: toggle(state.labels, l.value) })}>
            {l.value} <span className="muted">{l.count}</span>
          </button>
        ))}
        {state.threats.map((t) => (
          <button key={t} className="btn small on" onClick={() => set({ threats: state.threats.filter((x) => x !== t) })} title="Remove">
            threat: {t} ×
          </button>
        ))}
        {state.minScore > 0 && (
          <button className="btn small on" onClick={() => set({ minScore: 0 })} title="Remove">
            confidence ≥ {Math.round(state.minScore * 100)}% ×
          </button>
        )}
        {state.anomaly !== undefined && (
          <button className="btn small on" onClick={() => set({ anomaly: undefined })} title="Remove">
            {state.anomaly ? "anomalies only" : "no anomalies"} ×
          </button>
        )}
        <div className="grow" />
        <span className="muted small">{loading && !events.length ? "Searching…" : `${events.length}${cursor ? "+" : ""} result${events.length === 1 ? "" : "s"}`}</span>
      </div>

      {error && <div className="error">{error}</div>}
      {searched && !events.length && !loading && !error && <div className="empty">No events match these filters.</div>}
      <div className="event-grid">
        {events.map((e) => (
          <div key={e.id} className="op-card-wrap">
            <button className={`event-card kind-${e.kind.toLowerCase()}`} onClick={() => onPlayback({ cameraId: e.cameraId, at: e.createdAt - playbackLeadMs })} title="Play the recording">
              <Thumb client={session.client} event={e} />
              <div className="event-meta">
                <strong>{e.label || e.kind}</strong>
                {e.threat && e.threat !== "none" && <span className={`badge ${e.threat === "high" ? "red" : "amber"} op-ml`}>{e.threat}</span>}
                {e.anomaly && <span className="badge amber op-ml">anomaly</span>}
                <div className="muted small">
                  {name(e.cameraId)} · {new Date(e.createdAt).toLocaleString()}
                </div>
                {e.description && <div className="event-desc">{e.description}</div>}
              </div>
            </button>
            {canFindSimilar(e) && <FindSimilarButton onClick={() => setSimilarTo(e)} />}
          </div>
        ))}
      </div>
      <div ref={sentinel} className="load-more">
        {cursor && (
          <button className="btn" disabled={loading} onClick={() => void run(state, true)}>
            {loading ? "Loading…" : "Load more"}
          </button>
        )}
      </div>
    </div>
  );
}
