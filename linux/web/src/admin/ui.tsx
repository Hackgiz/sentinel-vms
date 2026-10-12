// Small building blocks for the Sentinel Admin pages, in the dashboard's own style.
import { createContext, useContext, useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { errorMessage } from "../shared/api";
import type { StreamMessage, StreamState } from "../shared/events";

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="scan-status" role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" />
      {label}
    </div>
  );
}

export function ErrorBanner({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (!error) return null;
  return (
    <div className="error admin-error" role="alert">
      <span className="grow">{errorMessage(error)}</span>
      {onRetry && (
        <button type="button" className="btn small" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  );
}

export function Empty({ icon, title, children }: { icon: string; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="big" aria-hidden="true">
        {icon}
      </div>
      <h2>{title}</h2>
      {children && <p>{children}</p>}
    </div>
  );
}

/** Modal dialog: Escape and a click on the backdrop close it; focus moves inside. */
export function Modal({
  title,
  onClose,
  onSubmit,
  wide,
  children,
  actions,
}: {
  title: string;
  onClose: () => void;
  onSubmit?: (e: FormEvent) => void;
  wide?: boolean;
  children: ReactNode;
  actions: ReactNode;
}) {
  const id = useId();
  const ref = useRef<HTMLFormElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    const el = ref.current;
    if (el && !el.contains(document.activeElement)) {
      const first = el.querySelector<HTMLElement>("[autofocus], input:not([disabled]), select, textarea, button");
      first?.focus();
    }
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close.current();
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      prev?.focus?.();
    };
  }, []);
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <form
        ref={ref}
        className={`modal ${wide ? "wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={id}
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit?.(e);
        }}
      >
        <h2 id={id}>{title}</h2>
        {children}
        <div className="actions">{actions}</div>
      </form>
    </div>
  );
}

/** "Are you sure?" for destructive actions. `run` may throw; the error shows in the dialog. */
export function Confirm({
  title,
  children,
  confirmLabel,
  onCancel,
  run,
}: {
  title: string;
  children: ReactNode;
  confirmLabel: string;
  onCancel: () => void;
  run: () => Promise<unknown>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <Modal
      title={title}
      onClose={onCancel}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          await run();
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onCancel} autoFocus>
            Cancel
          </button>
          <button className="btn danger" disabled={busy}>
            {busy ? "Working…" : confirmLabel}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <div className="confirm-body">{children}</div>
    </Modal>
  );
}

export function OnlineBadge({ online, on = "Online", off = "Offline" }: { online: boolean; on?: string; off?: string }) {
  return (
    <span className={`badge ${online ? "green" : "red"}`}>
      <span className="dot" />
      {online ? on : off}
    </span>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn small"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // Clipboard blocked (plain http): select-and-copy fallback.
          const ta = document.createElement("textarea");
          ta.value = text;
          document.body.appendChild(ta);
          ta.select();
          document.execCommand("copy");
          ta.remove();
        }
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}

/** Checkbox chips for picking several things (cameras, groups). */
export function ChipPicker<T extends { id: string; name: string }>({
  items,
  selected,
  onChange,
  empty,
  label,
}: {
  items: T[];
  selected: string[];
  onChange: (ids: string[]) => void;
  empty: string;
  label: string;
}) {
  const [q, setQ] = useState("");
  const shown = q ? items.filter((i) => i.name.toLowerCase().includes(q.toLowerCase())) : items;
  if (items.length === 0) return <span className="muted small">{empty}</span>;
  return (
    <fieldset className="picker">
      <legend className="sr-only">{label}</legend>
      {items.length > 8 && (
        <input type="text" className="picker-filter" placeholder="Filter…" aria-label={`Filter ${label.toLowerCase()}`} value={q} onChange={(e) => setQ(e.target.value)} />
      )}
      <div className="chip-grid">
        {shown.map((i) => {
          const on = selected.includes(i.id);
          return (
            <label key={i.id} className={`chip ${on ? "on" : ""}`}>
              <input type="checkbox" checked={on} onChange={() => onChange(on ? selected.filter((x) => x !== i.id) : [...selected, i.id])} />
              {i.name}
            </label>
          );
        })}
      </div>
      <div className="picker-foot muted small">
        {selected.length} selected
        {items.length > 1 && (
          <>
            {" · "}
            <button type="button" className="linkish" onClick={() => onChange(items.map((i) => i.id))}>
              All
            </button>
            {" · "}
            <button type="button" className="linkish" onClick={() => onChange([])}>
              None
            </button>
          </>
        )}
      </div>
    </fieldset>
  );
}

// ------------------------------------------------------------ live events ---

export type LiveMessage = StreamMessage | { type: "resync"; at: number };

interface LiveContext {
  subscribe: (fn: (m: LiveMessage) => void) => () => void;
  state: StreamState;
}

export const Live = createContext<LiveContext>({ subscribe: () => () => {}, state: "stopped" });

/** Re-run `reload` (debounced) when a message of one of `types` arrives, or after a resync. */
export function useLiveReload(reload: () => void, types: LiveMessage["type"][], delayMs = 400) {
  const { subscribe } = useContext(Live);
  const fn = useRef(reload);
  fn.current = reload;
  const key = types.join(",");
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | undefined;
    const wanted = new Set(key.split(","));
    const off = subscribe((m) => {
      if (m.type !== "resync" && !wanted.has(m.type)) return;
      clearTimeout(t);
      t = setTimeout(() => fn.current(), delayMs);
    });
    return () => {
      clearTimeout(t);
      off();
    };
  }, [subscribe, key, delayMs]);
}

/** Subscribe to raw live messages. */
export function useLiveMessages(handler: (m: LiveMessage) => void) {
  const { subscribe } = useContext(Live);
  const fn = useRef(handler);
  fn.current = handler;
  useEffect(() => subscribe((m) => fn.current(m)), [subscribe]);
}
