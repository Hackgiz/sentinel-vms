// Synchronized playback: one clock for several cameras, and where each
// camera's footage for a wall time lives. A camera that moved between
// recording servers has spans on each (Span.recorderId); each recording
// server serves its own playback playlist for the window, and `media` is
// seconds into that server's playlist. Wall times are Unix ms.

export interface PlaybackSpan {
  start: number;
  end: number;
  /** Seconds into the playlist of the same window on the same recording server. */
  media?: number;
  recorderId?: string;
}

/** The span recorded at t, or null in a gap. */
export function spanAt(spans: PlaybackSpan[], t: number): PlaybackSpan | null {
  for (const s of spans) if (t >= s.start && t < s.end) return s;
  return null;
}

/** The camera's spans on one recording server, oldest first, with `media` filled in. */
export function spansOn(spans: PlaybackSpan[], recorderId: string, fallbackRecorder = ""): PlaybackSpan[] {
  const mine = spans.filter((s) => (s.recorderId || fallbackRecorder) === recorderId).sort((a, b) => a.start - b.start);
  // The server sends `media`; when it doesn't, spans play end to end in the playlist.
  let m = 0;
  return mine.map((s) => {
    const out = { ...s, media: s.media ?? m };
    m = out.media + (s.end - s.start) / 1000;
    return out;
  });
}

/** Recording servers that hold footage in the list, in order of first appearance. */
export function recordersOf(spans: PlaybackSpan[], fallbackRecorder = ""): string[] {
  return [...new Set(spans.map((s) => s.recorderId || fallbackRecorder))];
}

/**
 * Where to play wall time t: the recording server and the media time in
 * its playlist, or null in a gap.
 */
export function locate(spans: PlaybackSpan[], t: number, fallbackRecorder = ""): { recorderId: string; media: number } | null {
  const s = spanAt(spans, t);
  if (!s) return null;
  const recorderId = s.recorderId || fallbackRecorder;
  const own = spansOn(spans, recorderId, fallbackRecorder);
  const o = spanAt(own, t)!;
  return { recorderId, media: o.media! + (t - o.start) / 1000 };
}

/** The wall time a player on one recording server shows at media time m (null between its spans). */
export function wallAt(spans: PlaybackSpan[], recorderId: string, m: number, fallbackRecorder = ""): number | null {
  const own = spansOn(spans, recorderId, fallbackRecorder);
  for (let i = 0; i < own.length; i++) {
    const s = own[i];
    const len = (s.end - s.start) / 1000;
    if (m >= s.media! && (m < s.media! + len || i === own.length - 1)) return Math.min(s.start + (m - s.media!) * 1000, s.end);
  }
  return null;
}

/** Start of the first span after t, or null. */
export function nextStart(spans: PlaybackSpan[], t: number): number | null {
  let best: number | null = null;
  for (const s of spans) if (s.start > t && (best === null || s.start < best)) best = s.start;
  return best;
}

/**
 * Skip time nobody recorded: t itself when any camera has footage there,
 * else the next start on any camera, or null past the last recording.
 */
export function skipGap(lanes: PlaybackSpan[][], t: number): number | null {
  if (lanes.some((l) => spanAt(l, t))) return t;
  const next = lanes.map((l) => nextStart(l, t)).filter((x): x is number => x !== null);
  return next.length ? Math.min(...next) : null;
}

/** Any footage in (a, b) on this camera? */
export function recordedBetween(spans: PlaybackSpan[], a: number, b: number): boolean {
  return spans.some((s) => s.start < b && s.end > a);
}

/**
 * The shared clock. It runs on a monotonic timer at `rate`; players are
 * steered to it, and it follows them back (`follow`) so decoder drift
 * doesn't build up. Pure: the time source is injected.
 */
export class SyncClock {
  private pos: number | null = null;
  private anchor = 0;
  private running = false;
  private speed = 1;

  constructor(private readonly now: () => number = () => performance.now()) {}

  /** Wall time shown now, or null before the first seek. */
  get position(): number | null {
    if (this.pos === null) return null;
    return this.running ? this.pos + (this.now() - this.anchor) * this.speed : this.pos;
  }

  get playing(): boolean {
    return this.running;
  }

  get rate(): number {
    return this.speed;
  }

  seek(t: number) {
    this.pos = t;
    this.anchor = this.now();
  }

  play() {
    if (this.pos === null || this.running) return;
    this.anchor = this.now();
    this.running = true;
  }

  pause() {
    this.pos = this.position;
    this.running = false;
  }

  setRate(r: number) {
    if (!(r > 0)) return;
    this.pos = this.position;
    this.anchor = this.now();
    this.speed = r;
  }

  /**
   * Adopt what a playing camera shows when it is close to the clock (its
   * decoder is the truth); far off means it is still seeking: ignore it.
   */
  follow(observed: number | null, tolerance = 1500): boolean {
    const p = this.position;
    if (observed === null || p === null || Math.abs(observed - p) >= tolerance) return false;
    this.seek(observed);
    return true;
  }

  /**
   * Advance past gaps on every lane. Returns the position, or null when
   * playback ran past the last recording (the clock then pauses there).
   */
  settle(lanes: PlaybackSpan[][]): number | null {
    const p = this.position;
    if (p === null) return null;
    const t = skipGap(lanes, p);
    if (t === null) {
      this.pause();
      return null;
    }
    if (t !== p) this.seek(t);
    return t;
  }
}

/** What one camera's player should do at the clock's position. */
export type Steer =
  | { kind: "gap" }
  | { kind: "load"; recorderId: string; media: number }
  | { kind: "seek"; media: number }
  | { kind: "play"; media: number }
  | { kind: "ok" };

/**
 * Steering for one player: `loaded` is the recording server whose playlist
 * it has, `current` its media time, `paused` whether it is paused;
 * `clockPlaying` whether the shared clock runs.
 */
export function steer(
  spans: PlaybackSpan[],
  t: number,
  player: { loaded: string | null; current: number; paused: boolean },
  clockPlaying: boolean,
  fallbackRecorder = "",
  tolerance = 1.5,
): Steer {
  const at = locate(spans, t, fallbackRecorder);
  if (!at) return { kind: "gap" };
  if (player.loaded !== at.recorderId) return { kind: "load", recorderId: at.recorderId, media: at.media };
  if (!clockPlaying) return Math.abs(player.current - at.media) > 0.3 ? { kind: "seek", media: at.media } : { kind: "ok" };
  if (player.paused) return { kind: "play", media: at.media };
  return Math.abs(player.current - at.media) > tolerance ? { kind: "seek", media: at.media } : { kind: "ok" };
}
