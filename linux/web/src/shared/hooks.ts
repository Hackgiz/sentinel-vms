// React glue for the shared clients.
import { useCallback, useEffect, useRef, useState, type DependencyList } from "react";
import type { ApiClient } from "./api";
import { EventStream, type StreamMessage, type StreamState, type Topic } from "./events";

/** Load something async; `reload()` again on demand. Keeps the last data while reloading. */
export function useLoad<T>(fn: () => Promise<T>, deps: DependencyList) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const seq = useRef(0);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const load = useCallback(fn, deps);
  const reload = useCallback(async () => {
    const n = ++seq.current;
    setLoading(true);
    try {
      const d = await load();
      if (n === seq.current) {
        setData(d);
        setError(null);
      }
    } catch (e) {
      if (n === seq.current) setError(e);
    } finally {
      if (n === seq.current) setLoading(false);
    }
  }, [load]);
  useEffect(() => {
    void reload();
  }, [reload]);
  return { data, error, loading, reload, setData };
}

/**
 * One event stream for a component tree. Uses the client's access token in
 * token mode, the session cookie otherwise.
 */
export function useEventStream(
  client: ApiClient,
  opts: { topics?: Topic[]; optionalTopics?: Topic[]; onMessage?: (m: StreamMessage) => void; onResync?: (reason: "lagged" | "reconnect") => void; enabled?: boolean },
) {
  const [state, setState] = useState<StreamState>("connecting");
  const [stream, setStream] = useState<EventStream | null>(null);
  const handlers = useRef(opts);
  handlers.current = opts;
  const topicsKey = (opts.topics ?? []).join(",");
  const optionalKey = (opts.optionalTopics ?? []).join(",");
  const enabled = opts.enabled ?? true;
  useEffect(() => {
    if (!enabled) return;
    const s = new EventStream({
      baseUrl: client.baseUrl,
      topics: topicsKey ? (topicsKey.split(",") as Topic[]) : undefined,
      optionalTopics: optionalKey ? (optionalKey.split(",") as Topic[]) : undefined,
      accessToken: async (force) => (force ? ((await client.refreshTokens())?.accessToken ?? null) : client.accessToken()),
      onMessage: (m) => handlers.current.onMessage?.(m),
      onResync: (r) => handlers.current.onResync?.(r),
      onState: setState,
    });
    s.start();
    setStream(s);
    return () => {
      s.stop();
      setStream(null);
    };
  }, [client, topicsKey, optionalKey, enabled]);
  return { state, stream };
}
