import { describe, expect, it, vi } from "vitest";
import { EventStream, streamUrl, type SocketLike, type StreamState } from "./events";

class FakeSocket implements SocketLike {
  readyState = 0;
  sent: string[] = [];
  closed: number | null = null;
  onopen: SocketLike["onopen"] = null;
  onmessage: SocketLike["onmessage"] = null;
  onclose: SocketLike["onclose"] = null;
  onerror: SocketLike["onerror"] = null;
  constructor(readonly url: string) {}
  send(d: string) {
    this.sent.push(d);
  }
  close(code = 1000) {
    this.closed = code;
  }
  // server side
  open() {
    this.readyState = 1;
    this.onopen?.({});
  }
  push(type: string, data: unknown = {}) {
    this.onmessage?.({ data: JSON.stringify({ type, at: 1, data }) });
  }
  drop(code = 1006) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

/** Manual timers so reconnect delays can be inspected and fired. */
function harness(extra: Partial<ConstructorParameters<typeof EventStream>[0]> = {}) {
  const sockets: FakeSocket[] = [];
  const timers: { fn: () => void; ms: number }[] = [];
  const states: StreamState[] = [];
  const resyncs: [string, number | undefined][] = [];
  const messages: string[] = [];
  const stream = new EventStream({
    baseUrl: "https://ms.lan:8090",
    topics: ["camera", "config", "servers"],
    createSocket: (url) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    },
    setTimer: (fn, ms) => (timers.push({ fn, ms }), timers.length),
    clearTimer: () => {},
    random: () => 0.5, // no jitter
    onState: (s) => states.push(s),
    onResync: (r, n) => resyncs.push([r, n]),
    onMessage: (m) => messages.push(m.type),
    ...extra,
  });
  const flush = () => new Promise((r) => setTimeout(r, 0));
  const fireTimer = async () => {
    timers.shift()!.fn();
    await flush();
  };
  return { stream, sockets, timers, states, resyncs, messages, flush, fireTimer };
}

describe("streamUrl", () => {
  it("turns an http(s) origin into a ws(s) URL with topics", () => {
    expect(streamUrl("https://ms.lan:8090", ["alarm", "camera"])).toBe("wss://ms.lan:8090/api/v2/events/stream?topics=alarm%2Ccamera");
    expect(streamUrl("http://10.0.0.2")).toBe("ws://10.0.0.2/api/v2/events/stream");
  });
});

describe("EventStream", () => {
  it("connects, authenticates with the first message in token mode, and goes live on hello", async () => {
    const h = harness({ accessToken: () => "tok-1" });
    h.stream.start();
    await h.flush();
    expect(h.sockets).toHaveLength(1);
    expect(h.sockets[0].url).toContain("topics=camera%2Cconfig%2Cservers");
    h.sockets[0].open();
    expect(JSON.parse(h.sockets[0].sent[0])).toEqual({ type: "auth", accessToken: "tok-1" });
    h.sockets[0].push("hello", { user: {}, topics: [], serverVersion: "2.0" });
    h.sockets[0].push("camera.status", { cameraId: "c1", online: false });
    expect(h.states.at(-1)).toBe("open");
    expect(h.messages).toEqual(["hello", "camera.status"]);
    expect(h.resyncs).toEqual([]); // the first hello is not a resync
  });

  it("sends nothing on open in cookie mode", async () => {
    const h = harness();
    h.stream.start();
    await h.flush();
    h.sockets[0].open();
    expect(h.sockets[0].sent).toEqual([]);
  });

  it("reconnects with growing backoff and asks for a resync after reconnecting", async () => {
    const h = harness({ minDelayMs: 1000, maxDelayMs: 8000 });
    h.stream.start();
    await h.flush();
    h.sockets[0].open();
    h.sockets[0].push("hello");
    h.sockets[0].drop();
    expect(h.states.at(-1)).toBe("reconnecting");
    // Fail three times in a row: 1 s, 2 s, 4 s, then capped.
    const delays: number[] = [];
    for (let i = 0; i < 4; i++) {
      delays.push(h.timers[0].ms);
      await h.fireTimer();
      h.sockets.at(-1)!.drop();
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000]);
    expect(h.timers[0].ms).toBe(8000);
    await h.fireTimer();
    h.sockets.at(-1)!.open();
    h.sockets.at(-1)!.push("hello");
    expect(h.resyncs).toEqual([["reconnect", undefined]]);
    // A successful hello resets the backoff.
    h.sockets.at(-1)!.drop();
    expect(h.timers[0].ms).toBe(1000);
  });

  it("asks for a resync when the server says the client lagged", async () => {
    const h = harness();
    h.stream.start();
    await h.flush();
    h.sockets[0].open();
    h.sockets[0].push("hello");
    h.sockets[0].push("lagged", { dropped: 42 });
    expect(h.resyncs).toEqual([["lagged", 42]]);
    expect(h.messages).toContain("lagged");
  });

  it("gets a fresh token after a 4401 close", async () => {
    const accessToken = vi.fn((force: boolean) => (force ? "fresh" : "stale"));
    const h = harness({ accessToken });
    h.stream.start();
    await h.flush();
    h.sockets[0].open();
    h.sockets[0].drop(4401);
    await h.fireTimer();
    expect(accessToken.mock.calls.map((c) => c[0])).toEqual([false, true]);
    h.sockets[1].open();
    expect(JSON.parse(h.sockets[1].sent[0]).accessToken).toBe("fresh");
  });

  it("stops for good on 4403 and when stopped", async () => {
    const h = harness();
    h.stream.start();
    await h.flush();
    h.sockets[0].drop(4403);
    expect(h.states.at(-1)).toBe("forbidden");
    expect(h.timers).toHaveLength(0);

    const h2 = harness();
    h2.stream.start();
    await h2.flush();
    h2.stream.stop();
    expect(h2.sockets[0].closed).toBe(1000);
    h2.sockets[0].drop();
    expect(h2.timers).toHaveLength(0);
    expect(h2.states.at(-1)).toBe("stopped");
  });

  it("ignores malformed frames", async () => {
    const h = harness();
    h.stream.start();
    await h.flush();
    h.sockets[0].onmessage?.({ data: "not json" });
    h.sockets[0].onmessage?.({ data: JSON.stringify({ nope: 1 }) });
    expect(h.messages).toEqual([]);
  });

  it("alternates optional topics until a hello, then keeps the variant that worked", async () => {
    const h = harness({ topics: ["camera", "config"], optionalTopics: ["servers"] });
    h.stream.start();
    await h.flush();
    expect(h.sockets[0].url).toContain("topics=camera%2Cconfig%2Cservers");
    h.sockets[0].drop(); // an older server refuses the unknown topic
    await h.fireTimer();
    expect(h.sockets[1].url).toContain("topics=camera%2Cconfig");
    expect(h.sockets[1].url).not.toContain("servers");
    h.sockets[1].open();
    h.sockets[1].push("hello");
    h.sockets[1].drop();
    await h.fireTimer();
    expect(h.sockets[2].url).not.toContain("servers");
  });
});
