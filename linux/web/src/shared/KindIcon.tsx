// The alarm kind icon (wave 16: Watchlist) and the person/plate chips on an
// event card. Both render nothing for older events and kinds.
import { alarmKindIcon, eventTags } from "./alarmKind";

export function KindIcon({ kind }: { kind: string | undefined }) {
  const k = alarmKindIcon(kind);
  if (!k) return null;
  return (
    <span className={`kind-icon kind-icon-${k.label.toLowerCase()}`} role="img" aria-label={k.label} title={k.label}>
      {k.icon}
    </span>
  );
}

export function EventTags({ event }: { event: { personName?: string; plate?: string } }) {
  const tags = eventTags(event);
  if (!tags.length) return null;
  return (
    <span className="event-tags">
      {tags.map((t) => (
        <span key={t.kind} className={`event-tag ${t.kind}`} title={t.kind === "person" ? "Recognized person" : "Plate read"}>
          {t.kind === "person" ? "☺ " : ""}
          {t.text}
        </span>
      ))}
    </span>
  );
}
