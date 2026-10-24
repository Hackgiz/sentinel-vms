// Retry-with-backoff for a player that hit a fatal network error: 1, 2, 4, 8,
// 16, then 30 s between attempts, for as long as the owner keeps it alive.

export const RETRY_MIN_MS = 1000;
export const RETRY_MAX_MS = 30_000;

/** Wait before retry number `attempt` (0 = the first retry). */
export const retryDelayMs = (attempt: number): number => Math.min(RETRY_MAX_MS, RETRY_MIN_MS * 2 ** Math.max(0, Math.min(attempt, 10)));

export class Reconnector {
  private attempt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;

  constructor(private readonly onRetry: () => void) {}

  /** A fatal error: schedule the next retry (once; further errors before it fires are ignored). */
  fail(): void {
    if (this.stopped || this.timer !== null) return;
    const wait = retryDelayMs(this.attempt++);
    this.timer = setTimeout(() => {
      this.timer = null;
      if (!this.stopped) this.onRetry();
    }, wait);
  }

  /** The stream is playing again: the next failure starts from 1 s. */
  ok(): void {
    this.attempt = 0;
  }

  /** The tile unmounted or its window changed: no more retries. */
  stop(): void {
    this.stopped = true;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
  }
}
