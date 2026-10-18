// Push-to-talk to a camera speaker (GET /api/v2/cameras/{id}/talkback).
//
// getUserMedia → an AudioWorklet that downsamples the microphone to 8 kHz
// PCM16 in 20 ms frames → binary WebSocket frames → the server encodes
// G.711 µ-law and relays RTP to the camera's ONVIF audio backchannel.
//
// Kept free of React so it can be tested: the downsampler (and the worklet
// source that carries the same algorithm), the URL, close-code wording and
// TalkSession (socket handling, auth, queueing while connecting).

/** The rate the server expects by default (PCMU/8000). */
export const TALK_RATE = 8000;
/** Samples per 20 ms frame at TALK_RATE. */
export const TALK_FRAME = TALK_RATE / 50;
/** Frames kept while the camera session is being set up (1 s). */
export const TALK_QUEUE_FRAMES = 50;

/** One float sample (−1..1) as signed 16-bit. */
export function toPcm16(x: number): number {
  const c = x > 1 ? 1 : x < -1 ? -1 : x;
  return Math.round(c < 0 ? c * 32768 : c * 32767);
}

/**
 * Streaming downsampler: each output sample is the mean of the input
 * samples since the previous one (a box low-pass, then decimation), with a
 * fractional step so 44.1 kHz works as well as 48 kHz. State carries over
 * between calls, so chunk boundaries don't matter.
 */
export class Downsampler {
  private acc = 0;
  private n = 0;
  private pos = 0; // in units of 1/inRate·outRate: exact integer stepping

  constructor(
    private readonly inRate: number,
    private readonly outRate = TALK_RATE,
  ) {
    if (!Number.isInteger(inRate) || !Number.isInteger(outRate) || outRate <= 0 || inRate < outRate) {
      throw new Error(`cannot downsample ${inRate} Hz to ${outRate} Hz`);
    }
  }

  process(input: ArrayLike<number>): Int16Array {
    const out = new Int16Array(Math.floor(((input.length + 1) * this.outRate) / this.inRate) + 1);
    let k = 0;
    for (let i = 0; i < input.length; i++) {
      this.acc += input[i];
      this.n++;
      this.pos += this.outRate;
      if (this.pos >= this.inRate) {
        out[k++] = toPcm16(this.acc / this.n);
        this.acc = 0;
        this.n = 0;
        this.pos -= this.inRate;
      }
    }
    return out.subarray(0, k);
  }
}

/**
 * The AudioWorklet module: the same algorithm as Downsampler (tests run
 * both on the same input), emitting Int16Array frames of `frame` samples
 * through the port. `processorOptions: { outRate, frame }`.
 */
export const WORKLET_NAME = "sentinel-talk";
export const WORKLET_SOURCE = `
class SentinelTalk extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const o = (options && options.processorOptions) || {};
    this.outRate = o.outRate || 8000;
    this.frame = o.frame || 160;
    this.inRate = Math.round(sampleRate);
    this.acc = 0; this.n = 0; this.pos = 0;
    this.buf = new Int16Array(this.frame); this.len = 0;
  }
  pcm(x) {
    const c = x > 1 ? 1 : x < -1 ? -1 : x;
    return Math.round(c < 0 ? c * 32768 : c * 32767);
  }
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (!ch) return true;
    for (let i = 0; i < ch.length; i++) {
      this.acc += ch[i]; this.n++; this.pos += this.outRate;
      if (this.pos >= this.inRate) {
        this.buf[this.len++] = this.pcm(this.acc / this.n);
        this.acc = 0; this.n = 0; this.pos -= this.inRate;
        if (this.len === this.frame) {
          this.port.postMessage(this.buf, [this.buf.buffer]);
          this.buf = new Int16Array(this.frame); this.len = 0;
        }
      }
    }
    return true;
  }
}
registerProcessor(${JSON.stringify(WORKLET_NAME)}, SentinelTalk);
`;

/** ws(s):// URL of a camera's talkback socket ("" baseUrl = this page). */
export function talkbackUrl(baseUrl: string, cameraId: string, rate = TALK_RATE): string {
  const origin = baseUrl || (typeof location !== "undefined" ? location.origin : "http://localhost");
  const u = new URL(`/api/v2/cameras/${encodeURIComponent(cameraId)}/talkback`, origin);
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
  if (rate !== TALK_RATE) u.searchParams.set("rate", String(rate));
  return u.toString();
}

/** What a close code means to the operator; null = nothing worth saying. */
export function talkCloseMessage(code: number, reason?: string): string | null {
  switch (code) {
    case 1000:
    case 1005:
    case 4408: // idle: the session simply timed out between presses
      return null;
    case 4400:
      return "This camera doesn't support two-way audio.";
    case 4401:
      return "Sign in again to talk through cameras.";
    case 4403:
      return "Your role can't talk through cameras.";
    case 4404:
      return "Camera not found.";
    case 4409:
      return reason || "Someone else is talking through this camera.";
    case 4410:
      return "Talk time limit reached (5 minutes).";
    case 4502:
      return reason ? `The camera refused two-way audio: ${reason}` : "The camera refused two-way audio.";
    default:
      return "Two-way audio disconnected.";
  }
}

/** Minimal WebSocket surface (tests pass a fake). */
export interface TalkSocket {
  binaryType: string;
  readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number; reason?: string }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send(data: string | ArrayBufferLike | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
}

export type TalkState = "connecting" | "ready" | "closed";

export interface TalkSessionOptions {
  url: string;
  /** Token mode: sent as the first message. null = the cookie authenticates. */
  accessToken: () => Promise<string | null> | string | null;
  onState?: (s: TalkState) => void;
  /** The session ended; message is for the operator (null = quiet end). */
  onClose?: (code: number, message: string | null) => void;
  createSocket?: (url: string) => TalkSocket;
}

/**
 * One talkback socket. Frames given to `send` go out only while `talking`
 * (held button); frames sent before the camera is ready are queued (up to
 * one second) so the first words aren't lost to the connection setup.
 */
export class TalkSession {
  state: TalkState = "connecting";
  talking = false;
  private ws: TalkSocket | null = null;
  private queue: Int16Array[] = [];
  private ended = false;
  private lastError = ""; // the server's {"type":"error"} text before a 4502

  constructor(private readonly opts: TalkSessionOptions) {}

  async open(): Promise<void> {
    const token = await this.opts.accessToken();
    if (this.ended) return;
    const create = this.opts.createSocket ?? ((u: string) => new WebSocket(u) as unknown as TalkSocket);
    const ws = create(this.opts.url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    ws.onopen = () => {
      if (token) ws.send(JSON.stringify({ type: "auth", accessToken: token }));
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data !== "string") return;
      let m: { type?: string; message?: string };
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (m.type === "ready") {
        this.setState("ready");
        for (const f of this.queue) if (this.talking) ws.send(f);
        this.queue = [];
      } else if (m.type === "error" && m.message) {
        this.lastError = m.message;
      }
    };
    ws.onclose = (ev) => this.finish(ev.code, ev.reason || this.lastError);
    ws.onerror = () => {};
  }

  /** Hold (true) or release (false) the talk button. */
  setTalking(on: boolean) {
    this.talking = on;
    if (!on) this.queue = [];
  }

  /** One 20 ms PCM16 frame from the worklet. */
  send(frame: Int16Array) {
    if (!this.talking || this.ended) return;
    if (this.state === "ready" && this.ws) {
      this.ws.send(frame);
    } else if (this.state === "connecting") {
      this.queue.push(frame);
      if (this.queue.length > TALK_QUEUE_FRAMES) this.queue.shift();
    }
  }

  /** Hang up: tell the server, then close. */
  close() {
    if (this.ended) return;
    const ws = this.ws;
    if (ws && ws.readyState === 1) {
      try {
        ws.send(JSON.stringify({ type: "stop" }));
      } catch {
        /* closing anyway */
      }
    }
    ws?.close(1000);
    this.finish(1000, "");
  }

  private setState(s: TalkState) {
    this.state = s;
    this.opts.onState?.(s);
  }

  private finish(code: number, reason: string) {
    if (this.ended) return;
    this.ended = true;
    this.queue = [];
    this.setState("closed");
    this.opts.onClose?.(code, talkCloseMessage(code, reason));
  }
}

/** A running microphone capture; stop() releases the mic. */
export interface Capture {
  stop(): void;
}

/**
 * Opens the microphone and starts the downsampling worklet; onFrame gets
 * 20 ms PCM16 frames at 8 kHz. Throws a readable Error when the browser
 * can't (no secure context, permission denied, no AudioWorklet).
 */
export async function startCapture(onFrame: (f: Int16Array) => void): Promise<Capture> {
  if (typeof navigator === "undefined" || !navigator.mediaDevices?.getUserMedia) {
    throw new Error("This browser can't use the microphone here (it needs HTTPS or the desktop app).");
  }
  const stream = await navigator.mediaDevices
    .getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true } })
    .catch((e: unknown) => {
      throw new Error(e instanceof DOMException && e.name === "NotAllowedError" ? "Microphone access was denied." : "The microphone could not be opened.");
    });
  const ctx = new AudioContext();
  const stopAll = () => {
    stream.getTracks().forEach((t) => t.stop());
    void ctx.close().catch(() => {});
  };
  try {
    if (!ctx.audioWorklet) throw new Error("This browser has no AudioWorklet.");
    const url = URL.createObjectURL(new Blob([WORKLET_SOURCE], { type: "text/javascript" }));
    try {
      await ctx.audioWorklet.addModule(url);
    } finally {
      URL.revokeObjectURL(url);
    }
    const src = ctx.createMediaStreamSource(stream);
    const node = new AudioWorkletNode(ctx, WORKLET_NAME, { numberOfInputs: 1, numberOfOutputs: 0, processorOptions: { outRate: TALK_RATE, frame: TALK_FRAME } });
    node.port.onmessage = (ev: MessageEvent<Int16Array>) => onFrame(ev.data);
    src.connect(node);
    if (ctx.state === "suspended") await ctx.resume();
    return {
      stop: () => {
        node.port.onmessage = null;
        src.disconnect();
        stopAll();
      },
    };
  } catch (e) {
    stopAll();
    throw e;
  }
}
