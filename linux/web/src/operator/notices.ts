// A tiny notice bus for the banner strip (Banners.tsx): the data layer and
// the workspaces emit what the operator should hear about (recording server
// offline / back, a camera dropping, the AI daily limit, an export
// finished); the strip shows them one line each until dismissed. No toasts
// library, nothing here renders, nothing here is React. The same notice
// (same key and text) within `dedupeMs` is dropped, so the stream and a
// poll that both saw a transition raise it once.

export type NoticeKind = "info" | "success" | "warn" | "error";
/** Older emitters say "level"; it means the same thing as kind. */
export type NoticeLevel = NoticeKind;

export interface NoticeInput {
  kind?: NoticeKind;
  level?: NoticeKind;
  text: string;
  /** A notice with the same key replaces the earlier one (offline -> back). */
  key?: string;
}

export interface Notice extends NoticeInput {
  kind: NoticeKind;
  id: number;
  at: number;
}

/** At most this many lines show at once; older ones drop off. */
export const maxNotices = 4;

/** Success and info lines fade on their own after this long; warnings stay. */
export const autoDismissMs = 8000;

export const dedupeMs = 30_000;

let nextId = 1;
const listeners = new Set<(n: Notice) => void>();
const lastSeen = new Map<string, number>();

/**
 * Emit a notice (from anywhere: data layer, another workspace); null
 * (nothing to say) is ignored. Returns whether it was delivered.
 */
export function emitNotice(n: NoticeInput | null, now = Date.now()): boolean {
  if (!n) return false;
  const kind = n.kind ?? n.level ?? "info";
  const seenKey = `${n.key ?? ""}|${n.text}`;
  const prev = lastSeen.get(seenKey);
  if (prev !== undefined && now - prev < dedupeMs) return false;
  lastSeen.set(seenKey, now);
  const full: Notice = { ...n, kind, level: kind, id: nextId++, at: now };
  for (const fn of [...listeners]) fn(full);
  return true;
}

/** One-shot notice with nothing to replace (the text is its own identity). */
export function notify(kind: NoticeKind, text: string): void {
  emitNotice({ kind, text });
}

export function subscribeNotices(fn: (n: Notice) => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

/** Add a notice to the list: a keyed one replaces its predecessor, the list is capped (newest last). */
export function addNotice(list: Notice[], n: Notice): Notice[] {
  const rest = n.key ? list.filter((x) => x.key !== n.key) : list;
  return [...rest, n].slice(-maxNotices);
}

export const dismissNotice = (list: Notice[], id: number): Notice[] => list.filter((n) => n.id !== id);

export const autoDismisses = (n: Notice) => n.kind === "success" || n.kind === "info";

/**
 * The notice for a recording server changing state, or null when nothing
 * changed or the old state is unknown (the first report on connect).
 */
export function recorderNotice(name: string, wasOnline: boolean | undefined, online: boolean, id: string): NoticeInput | null {
  if (wasOnline === undefined || wasOnline === online) return null;
  return online
    ? { kind: "success", key: `recorder:${id}`, text: `Recording server ${name} is back online.` }
    : { kind: "warn", key: `recorder:${id}`, text: `Recording server ${name} is offline. Its cameras are unavailable until it reconnects.` };
}

/** Forget dedupe state (tests). */
export function resetNotices() {
  lastSeen.clear();
}
