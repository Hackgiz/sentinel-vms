import { describe, expect, it } from "vitest";
import {
  Downsampler,
  TALK_FRAME,
  TALK_QUEUE_FRAMES,
  TalkSession,
  WORKLET_NAME,
  WORKLET_SOURCE,
  talkCloseMessage,
  talkbackUrl,
  toPcm16,
  type TalkSocket,
} from "./talkback";

function sine(freq: number, rate: number, n: number, amp = 0.5): Float32Array {
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / rate);
  return out;
}

function rms(x: ArrayLike<number>, from = 0): number {
  let s = 0;
  for (let i = from; i < x.length; i++) s += x[i] * x[i];
  return Math.sqrt(s / (x.length - from));
}

describe("toPcm16", () => {
  it("maps −1..1 onto the 16-bit range and clamps", () => {
    expect(toPcm16(0)).toBe(0);
    expect(toPcm16(1)).toBe(32767);
    expect(toPcm16(-1)).toBe(-32768);
    expect(toPcm16(2)).toBe(32767);
    expect(toPcm16(-3)).toBe(-32768);
    expect(toPcm16(0.5)).toBe(16384);
    expect(toPcm16(-0.5)).toBe(-16384);
  });
});

describe("Downsampler", () => {
  it("turns 48 kHz into 8 kHz (one in six) and keeps a voice-band tone", () => {
    const d = new Downsampler(48000);
    const out = d.process(sine(440, 48000, 48000));
    expect(out.length).toBe(8000);
    // A box average of 6 barely touches 440 Hz.
    expect(rms(out, 10) / (0.5 * 32767 * Math.SQRT1_2)).toBeGreaterThan(0.95);
  });

  it("handles 44.1 kHz with a fractional step", () => {
    const d = new Downsampler(44100);
    const out = d.process(new Float32Array(44100).fill(0.25));
    expect(out.length).toBe(8000);
    expect(new Set(out)).toEqual(new Set([toPcm16(0.25)])); // DC is exact
  });

  it("damps tones above the new Nyquist", () => {
    // 16 kHz at 48 kHz: exactly two cycles per box of six, averaged away.
    expect(rms(new Downsampler(48000).process(sine(16000, 48000, 4800)))).toBeLessThan(2);
    // 7 kHz (would alias to 1 kHz): the box's response there is about −17 dB.
    const out = new Downsampler(48000).process(sine(7000, 48000, 48000));
    expect(rms(out) / (0.5 * 32767 * Math.SQRT1_2)).toBeLessThan(0.2);
  });

  it("is streaming: chunk boundaries don't change the output", () => {
    const input = sine(300, 44100, 4410);
    const whole = new Downsampler(44100).process(input);
    const d = new Downsampler(44100);
    const parts: number[] = [];
    for (let i = 0; i < input.length; ) {
      const n = Math.min(1 + (i % 129), input.length - i);
      parts.push(...d.process(input.subarray(i, i + n)));
      i += n;
    }
    expect(parts).toEqual([...whole]);
  });

  it("passes 8 kHz through and refuses upsampling", () => {
    const x = Float32Array.from([0, 0.5, -0.5, 1]);
    expect([...new Downsampler(8000).process(x)]).toEqual([0, 16384, -16384, 32767]);
    expect(() => new Downsampler(4000)).toThrow();
  });
});

describe("worklet source", () => {
  // Evaluate the module with fake worklet globals and drive it like the browser does (128-frame quanta).
  function loadWorklet(rate: number) {
    let Proc: (new (o: unknown) => { process(i: Float32Array[][]): boolean; port: { posted: Int16Array[] } }) | null = null;
    let name = "";
    class FakeProcessor {
      port = {
        posted: [] as Int16Array[],
        postMessage(m: Int16Array) {
          this.posted.push(m);
        },
      };
    }
    new Function("AudioWorkletProcessor", "registerProcessor", "sampleRate", WORKLET_SOURCE)(
      FakeProcessor,
      (n: string, c: typeof Proc) => {
        name = n;
        Proc = c;
      },
      rate,
    );
    expect(name).toBe(WORKLET_NAME);
    return new Proc!({ processorOptions: { outRate: 8000, frame: TALK_FRAME } });
  }

  it("matches Downsampler exactly and posts 20 ms frames", () => {
    for (const rate of [48000, 44100]) {
      const p = loadWorklet(rate);
      const input = sine(523, rate, rate / 2); // 0.5 s
      for (let i = 0; i < input.length; i += 128) {
        expect(p.process([[input.subarray(i, Math.min(i + 128, input.length))]])).toBe(true);
      }
      const want = new Downsampler(rate).process(input);
      const frames = p.port.posted;
      expect(frames.length).toBe(Math.floor(want.length / TALK_FRAME));
      expect(frames.every((f) => f.length === TALK_FRAME)).toBe(true);
      const got = frames.flatMap((f) => [...f]);
      expect(got).toEqual([...want.subarray(0, got.length)]);
    }
  });

  it("survives a quantum with no input", () => {
    const p = loadWorklet(48000);
    expect(p.process([[]])).toBe(true);
    expect(p.process([])).toBe(true);
  });
});

describe("talkbackUrl", () => {
  it("builds the ws(s) URL", () => {
    expect(talkbackUrl("https://ms.lan:8090", "cam 1")).toBe("wss://ms.lan:8090/api/v2/cameras/cam%201/talkback");
    expect(talkbackUrl("http://10.0.0.2:8090/", "c")).toBe("ws://10.0.0.2:8090/api/v2/cameras/c/talkback");
    expect(talkbackUrl("http://x", "c", 16000)).toBe("ws://x/api/v2/cameras/c/talkback?rate=16000");
  });
});

describe("talkCloseMessage", () => {
  it("stays quiet for normal ends and explains the rest", () => {
    expect(talkCloseMessage(1000)).toBeNull();
    expect(talkCloseMessage(4408)).toBeNull();
    expect(talkCloseMessage(4409, "op is already talking through this camera")).toBe("op is already talking through this camera");
    expect(talkCloseMessage(4410)).toMatch(/limit/);
    expect(talkCloseMessage(4502, "RTSP 453")).toMatch(/RTSP 453/);
    expect(talkCloseMessage(1006)).toMatch(/disconnected/);
  });
});

class FakeTalkSocket implements TalkSocket {
  binaryType = "blob";
  readyState = 0;
  sent: (string | Int16Array)[] = [];
  closedWith: number | null = null;
  onopen: TalkSocket["onopen"] = null;
  onmessage: TalkSocket["onmessage"] = null;
  onclose: TalkSocket["onclose"] = null;
  onerror: TalkSocket["onerror"] = null;
  constructor(readonly url: string) {}
  send(d: string | ArrayBufferLike | ArrayBufferView) {
    this.sent.push(typeof d === "string" ? d : (d as Int16Array));
  }
  close(code = 1000) {
    this.closedWith = code;
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.({});
  }
  push(m: unknown) {
    this.onmessage?.({ data: JSON.stringify(m) });
  }
  drop(code: number, reason = "") {
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }
}

function session(token: string | null) {
  const sockets: FakeTalkSocket[] = [];
  const states: string[] = [];
  const closes: [number, string | null][] = [];
  const s = new TalkSession({
    url: "wss://ms/api/v2/cameras/c/talkback",
    accessToken: () => token,
    onState: (st) => states.push(st),
    onClose: (code, msg) => closes.push([code, msg]),
    createSocket: (u) => {
      const f = new FakeTalkSocket(u);
      sockets.push(f);
      return f;
    },
  });
  return { s, sockets, states, closes };
}

const frame = (v: number) => new Int16Array(TALK_FRAME).fill(v);

describe("TalkSession", () => {
  it("authenticates with the first message, queues while connecting, sends only while held", async () => {
    const { s, sockets, states } = session("tok");
    await s.open();
    const ws = sockets[0];
    expect(ws.binaryType).toBe("arraybuffer");
    ws.open();
    expect(ws.sent).toEqual([JSON.stringify({ type: "auth", accessToken: "tok" })]);

    s.send(frame(1)); // not held: dropped
    s.setTalking(true);
    s.send(frame(2));
    s.send(frame(3));
    expect(ws.sent.length).toBe(1); // still connecting: queued
    ws.push({ type: "ready", codec: "PCMU/8000" });
    expect(states).toEqual(["ready"]);
    expect(ws.sent.slice(1).map((f) => (f as Int16Array)[0])).toEqual([2, 3]);
    s.send(frame(4));
    s.setTalking(false);
    s.send(frame(5));
    expect(ws.sent.slice(1).map((f) => (f as Int16Array)[0])).toEqual([2, 3, 4]);
  });

  it("uses the cookie when there is no token, and caps the queue at one second", async () => {
    const { s, sockets } = session(null);
    await s.open();
    sockets[0].open();
    expect(sockets[0].sent).toEqual([]);
    s.setTalking(true);
    for (let i = 0; i < TALK_QUEUE_FRAMES + 10; i++) s.send(frame(i));
    sockets[0].push({ type: "ready" });
    const sent = sockets[0].sent as Int16Array[];
    expect(sent.length).toBe(TALK_QUEUE_FRAMES);
    expect(sent[0][0]).toBe(10); // the oldest were dropped
  });

  it("hangs up with a stop message and reports a quiet close", async () => {
    const { s, sockets, closes, states } = session("t");
    await s.open();
    sockets[0].open();
    sockets[0].push({ type: "ready" });
    s.close();
    expect(sockets[0].sent.at(-1)).toBe(JSON.stringify({ type: "stop" }));
    expect(sockets[0].closedWith).toBe(1000);
    expect(closes).toEqual([[1000, null]]);
    expect(states.at(-1)).toBe("closed");
    s.close(); // idempotent
    sockets[0].drop(1000);
    expect(closes.length).toBe(1);
  });

  it("reports server-side ends: busy, camera refusal with the error text", async () => {
    const busy = session("t");
    await busy.s.open();
    busy.sockets[0].open();
    busy.sockets[0].drop(4409, "op is already talking through this camera");
    expect(busy.closes).toEqual([[4409, "op is already talking through this camera"]]);

    const refused = session("t");
    await refused.s.open();
    refused.sockets[0].open();
    refused.sockets[0].push({ type: "error", message: "RTSP 453 Not Enough Bandwidth" });
    refused.sockets[0].drop(4502);
    expect(refused.closes[0][1]).toMatch(/RTSP 453/);
    refused.s.setTalking(true);
    refused.s.send(frame(1)); // after the end: ignored
    expect(refused.sockets[0].sent.length).toBe(1);
  });

  it("does not open a socket when closed before the token arrived", async () => {
    let release: (t: string) => void = () => {};
    const sockets: FakeTalkSocket[] = [];
    const s = new TalkSession({
      url: "wss://x",
      accessToken: () => new Promise<string>((r) => (release = r)),
      createSocket: (u) => {
        const f = new FakeTalkSocket(u);
        sockets.push(f);
        return f;
      },
    });
    const opening = s.open();
    s.close();
    release("t");
    await opening;
    expect(sockets.length).toBe(0);
  });
});
