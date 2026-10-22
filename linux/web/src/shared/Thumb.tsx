// An event's snapshot through the API client (moved from operator/EventsList.tsx
// in wave 21 so the server dashboard's Home can use it): loads when scrolled
// near, works with the bearer-token and the cookie client alike.
import { useEffect, useRef, useState } from "react";
import type { ApiClient, Event } from "./api";

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
