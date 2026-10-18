// Hold-to-talk over a live tile (two-way audio through the camera speaker,
// WAVE9 stream B). Rendered only for cameras whose payload says
// `talkback` and for users with the controlPtz permission (CameraGrid).
// Pressing opens the microphone and the talkback socket (frames spoken
// while it connects are queued); releasing mutes. The session stays open
// between presses until the server's 30 s idle limit closes it, so quick
// back-and-forth doesn't renegotiate the camera every time.
import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { ApiClient } from "../shared/api";
import { startCapture, talkbackUrl, TalkSession, type Capture } from "./talkback";

type Phase = "idle" | "connecting" | "live";

export default function TalkButton({ client, cameraId, cameraName }: { client: ApiClient; cameraId: string; cameraName: string }) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [held, setHeld] = useState(false);
  const [note, setNote] = useState("");
  const sess = useRef<{ session: TalkSession; capture: Capture | null } | null>(null);
  const heldRef = useRef(false);

  const teardown = useCallback(() => {
    const cur = sess.current;
    sess.current = null;
    cur?.capture?.stop();
    cur?.session.close();
    setPhase("idle");
  }, []);

  const begin = useCallback(async () => {
    setNote("");
    if (sess.current) {
      sess.current.session.setTalking(true);
      return;
    }
    const session = new TalkSession({
      url: talkbackUrl(client.baseUrl, cameraId),
      accessToken: () => client.accessToken(),
      onState: (s) => {
        if (s === "ready") setPhase("live");
      },
      onClose: (_code, message) => {
        if (sess.current?.session !== session) return;
        sess.current.capture?.stop();
        sess.current = null;
        setPhase("idle");
        if (message) setNote(message);
      },
    });
    const entry: { session: TalkSession; capture: Capture | null } = { session, capture: null };
    sess.current = entry;
    session.setTalking(heldRef.current);
    setPhase("connecting");
    try {
      const capture = await startCapture((f) => session.send(f));
      if (sess.current !== entry) {
        capture.stop(); // released and closed while the mic was opening
        return;
      }
      entry.capture = capture;
      await session.open();
    } catch (e) {
      if (sess.current === entry) teardown();
      setNote(e instanceof Error ? e.message : String(e));
    }
  }, [client, cameraId, teardown]);

  const press = useCallback(
    (e: ReactPointerEvent<HTMLButtonElement>) => {
      e.preventDefault();
      e.stopPropagation();
      e.currentTarget.setPointerCapture?.(e.pointerId);
      heldRef.current = true;
      setHeld(true);
      void begin();
    },
    [begin],
  );
  const release = useCallback(() => {
    if (!heldRef.current) return;
    heldRef.current = false;
    setHeld(false);
    sess.current?.session.setTalking(false);
  }, []);

  // A different camera, or the tile going away, hangs up.
  useEffect(() => teardown, [teardown, cameraId]);
  useEffect(() => {
    window.addEventListener("blur", release);
    return () => window.removeEventListener("blur", release);
  }, [release]);

  const label = held ? (phase === "live" ? "Talking…" : "Connecting…") : "Hold to talk";
  return (
    <div
      className="op-talk"
      draggable={false}
      onDragStart={(e) => {
        e.preventDefault();
        e.stopPropagation();
      }}
      onDoubleClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.preventDefault()}
    >
      {note && (
        <div className="op-talk-note" role="status" onClick={() => setNote("")}>
          {note}
        </div>
      )}
      <button
        type="button"
        className={`btn small op-talk-btn ${held ? "on" : ""} ${phase === "live" ? "live" : ""}`}
        aria-pressed={held}
        aria-label={`Hold to talk through ${cameraName}`}
        title={`Hold to talk through ${cameraName}'s speaker`}
        onPointerDown={press}
        onPointerUp={release}
        onPointerCancel={release}
        onLostPointerCapture={release}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zm-7 9h2a5 5 0 0 0 10 0h2a7 7 0 0 1-6 6.93V21h-2v-2.07A7 7 0 0 1 5 12z" />
        </svg>
        <span>{label}</span>
      </button>
    </div>
  );
}
