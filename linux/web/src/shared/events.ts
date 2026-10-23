// Live notifications from GET /api/v2/events/stream (WebSocket).
//
//   const stream = new EventStream({
//     topics: ["camera", "config", "servers"],
//     onMessage: (m) => { if (m.type === "camera.status") … },
//     onResync: () => reloadEverything(),     // after `lagged` or a reconnect
//   });
//   stream.start(); … stream.stop();
//
// Browser: the session cookie authenticates the upgrade. Token clients
// (Electron) pass `accessToken`; it is sent as the first message.

import type { Alarm, Event, User } from "./api";

/** Stream topics (`?topics=`). `servers` carries recorder.status. */
export type Topic = "alarm" | "event" | "camera" | "config" | "activity" | "servers";

/** StreamMessage with `data` typed per `type` (see StreamMessage in openapi.yaml). */
export type StreamMessage =
  | { type: "hello"; at: number; data: { user: User; topics: string[]; serverVersion: string } }
  | { type: "alarm.new" | "alarm.updated"; at: number; data: Alarm }
  | { type: "event.new" | "event.updated"; at: number; data: Event }
  | { type: "camera.status"; at: number; data: { cameraId: string; online: boolean } }
  | { type: "camera.activity"; at: number; data: { cameraId: string; kind: string } }
  | { type: "config.changed"; at: number; data: { cameraIds: string[] } }
  | { type: "recorder.status"; at: number; data: { recorderId: string; online: boolean } }
  | { type: "lagged"; at: number; data: { dropped: number } };

export type StreamMessageType = StreamMessage["type"];

export type StreamState = "connecting" | "open" | "reconnecting" | "stopped" | "forbidden";

/** Minimal WebSocket surface, so tests can pass a fake. */
export interface SocketLike {
  readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number; reason?: string }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send(data: string): void;
  close(code?: number, reason?: string): void;
}

export interface EventStreamOptions {
  /** Management server origin ("" = this page). Ignored when `url` is set. */
  baseUrl?: string;
  /** Full ws(s):// URL of the stream (overrides baseUrl). */
  url?: string;
  /** Omitted = the server default (all but activity). */
  topics?: Topic[];
  /**
   * Topics a server may not know yet (an unknown topic refuses the upgrade).
   * Until the first `hello`, failed attempts alternate between asking with
   * and without them; the variant that opens is kept.
   */
  optionalTopics?: Topic[];
  /** Token mode: the access token to send as the first message. `force` = the last one was refused (4401). */
  accessToken?: (force: boolean) => Promise<string | null> | string | null;
  onMessage?: (m: StreamMessage) => void;
  /**
   * State may have been missed: the server sent `lagged`, or the stream
   * reconnected after a drop. Re-read whatever the page shows.
   */
  onResync?: (reason: "lagged" | "reconnect", dropped?: number) => void;
  onState?: (s: StreamState) => void;
  /** Reconnect backoff: first delay, cap (ms). Jittered ±20%. */
  minDelayMs?: number;
  maxDelayMs?: number;
  /** Injected for tests. */
  createSocket?: (url: string) => SocketLike;
  random?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (t: unknown) => void;
}

/** ws(s):// URL of the stream for an http(s) origin ("" = this page's). */
export function streamUrl(baseUrl: string, topics?: Topic[]): string {
  const origin = baseUrl || (typeof location !== "undefined" ? location.origin : "http://localhost");
  const u = new URL("/api/v2/events/stream", origin);
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
  if (topics?.length) u.searchParams.set("topics", topics.join(","));
  return u.toString();
}

export class EventStream {
  private socket: SocketLike | null = null;
  private timer: unknown = null;
  private attempts = 0;
  private everOpened = false;
  private stopped = true;
  private generation = 0;
  private forceToken = false;
  private dropOptional = false;
  private readonly listeners = new Set<(m: StreamMessage) => void>();
  private readonly o: EventStreamOptions;

  constructor(options: EventStreamOptions = {}) {
    this.o = options;
    if (options.onMessage) this.listeners.add(options.onMessage);
  }

  /** Subscribe; returns an unsubscribe function. */
  subscribe(fn: (m: StreamMessage) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  start() {
    if (!this.stopped) return;
    this.stopped = false;
    this.attempts = 0;
    this.generation++;
    void this.connect();
  }

  stop() {
    this.stopped = true;
    this.clear();
    const s = this.socket;
    this.socket = null;
    if (s) {
      s.onopen = s.onmessage = s.onclose = s.onerror = null;
      try {
        s.close(1000, "client closed");
      } catch {
        /* already closed */
      }
    }
    this.setState("stopped");
  }

  /** The delay before reconnect attempt n (1-based). */
  delayFor(n: number): number {
    const min = this.o.minDelayMs ?? 1000;
    const max = this.o.maxDelayMs ?? 30_000;
    const base = Math.min(max, min * 2 ** (n - 1));
    const jitter = 0.8 + 0.4 * (this.o.random ?? Math.random)();
    return Math.round(base * jitter);
  }

  /** The topics this attempt asks for. */
  currentTopics(): Topic[] | undefined {
    const opt = this.o.optionalTopics ?? [];
    if (!opt.length) return this.o.topics;
    if (this.dropOptional) return this.o.topics?.filter((t) => !opt.includes(t));
    return [...new Set([...(this.o.topics ?? []), ...opt])];
  }

  private setState(s: StreamState) {
    this.o.onState?.(s);
  }

  private clear() {
    if (this.timer !== null) (this.o.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>)))(this.timer);
    this.timer = null;
  }

  private async connect() {
    if (this.stopped) return;
    // A stop()+start() while the token is awaited (React StrictMode's
    // double mount) must not leave this attempt opening a second socket.
    const gen = this.generation;
    this.setState(this.everOpened ? "reconnecting" : "connecting");
    let token: string | null = null;
    if (this.o.accessToken) {
      try {
        token = await this.o.accessToken(this.forceToken);
      } catch {
        token = null;
      }
      this.forceToken = false;
      if (this.stopped || gen !== this.generation) return;
    }
    const url = this.o.url ?? streamUrl(this.o.baseUrl ?? "", this.currentTopics());
    let s: SocketLike;
    try {
      s = this.o.createSocket ? this.o.createSocket(url) : (new WebSocket(url) as unknown as SocketLike);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.socket = s;
    let greeted = false;
    s.onopen = () => {
      if (token) s.send(JSON.stringify({ type: "auth", accessToken: token }));
    };
    s.onmessage = (ev) => {
      if (typeof ev.data !== "string") return;
      let m: StreamMessage;
      try {
        m = JSON.parse(ev.data) as StreamMessage;
      } catch {
        return;
      }
      if (!m || typeof m.type !== "string") return;
      if (m.type === "hello") {
        greeted = true;
        const reconnected = this.everOpened;
        this.everOpened = true;
        this.attempts = 0;
        this.setState("open");
        if (reconnected) this.o.onResync?.("reconnect");
      } else if (m.type === "lagged") {
        this.o.onResync?.("lagged", m.data?.dropped);
      }
      for (const fn of this.listeners) fn(m);
    };
    s.onerror = () => {
      /* onclose follows */
    };
    s.onclose = (ev) => {
      if (this.socket !== s) return;
      this.socket = null;
      if (this.stopped) return;
      if (ev.code === 4403) {
        // Not allowed to watch any more: don't hammer the server.
        this.stopped = true;
        this.setState("forbidden");
        return;
      }
      if (ev.code === 4401) this.forceToken = true;
      else if (!greeted && !this.everOpened && this.o.optionalTopics?.length) this.dropOptional = !this.dropOptional;
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect() {
    if (this.stopped) return;
    this.attempts++;
    this.setState("reconnecting");
    const set = this.o.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.timer = set(() => {
      this.timer = null;
      void this.connect();
    }, this.delayFor(this.attempts));
  }
}
