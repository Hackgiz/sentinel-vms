// A TokenStore for one site that several windows share (the main window and
// its pop-outs). The pair lives in the desktop app's main process, encrypted
// on disk; each window keeps a copy for the synchronous TokenStore reads,
// writes through, and hears about the other windows' writes. Refreshes run
// under a Web Lock, so a rotated refresh token is never presented twice
// (that would revoke the sign-in). In a plain browser it is memory only.

import type { TokenPair, TokenStore } from "../shared/api";

/** Where the pair is kept (the desktop bridge's `tokens`, or a fake in tests). */
export interface TokenBackend {
  load(siteId: string): Promise<TokenPair | null>;
  save(siteId: string, tokens: TokenPair | null): Promise<void>;
  onChange?(fn: (siteId: string, tokens: TokenPair | null) => void): () => void;
}

/** Runs fn while holding a named lock shared by the app's windows. */
export type LockFn = <T>(name: string, fn: () => Promise<T>) => Promise<T>;

/** navigator.locks when the runtime has it (Chromium does), else no locking. */
export const webLock: LockFn = (name, fn) => {
  const locks = typeof navigator !== "undefined" ? (navigator as Navigator & { locks?: LockManager }).locks : undefined;
  return locks ? (locks.request(name, fn) as ReturnType<typeof fn>) : fn();
};

export class SharedTokenStore implements TokenStore {
  private tokens: TokenPair | null = null;
  private saving: Promise<void> = Promise.resolve();
  private unsubscribe: (() => void) | null = null;
  private readonly listeners = new Set<(t: TokenPair | null) => void>();

  private constructor(
    readonly siteId: string,
    private readonly backend: TokenBackend | null,
    private readonly lock: LockFn,
  ) {}

  /** A store holding the site's saved pair (null backend: memory only). */
  static async open(siteId: string, backend: TokenBackend | null, lock: LockFn = webLock): Promise<SharedTokenStore> {
    const s = new SharedTokenStore(siteId, backend, lock);
    if (backend) {
      s.tokens = await backend.load(siteId).catch(() => null);
      s.unsubscribe =
        backend.onChange?.((id, t) => {
          if (id !== siteId || sameTokens(t, s.tokens)) return;
          s.tokens = t;
          s.emit();
        }) ?? null;
    }
    return s;
  }

  get(): TokenPair | null {
    return this.tokens;
  }

  set(t: TokenPair | null) {
    if (sameTokens(t, this.tokens)) return;
    this.tokens = t;
    this.emit();
    const backend = this.backend;
    if (backend) this.saving = this.saving.then(() => backend.save(this.siteId, t)).catch(() => {});
  }

  async reload(): Promise<void> {
    if (!this.backend) return;
    await this.saving;
    const t = await this.backend.load(this.siteId).catch(() => this.tokens);
    if (!sameTokens(t, this.tokens)) {
      this.tokens = t;
      this.emit();
    }
  }

  /** Holds the site's refresh lock until fn's result is saved where the other windows read it. */
  exclusive<T>(fn: () => Promise<T>): Promise<T> {
    return this.lock(`sentinel-tokens:${this.siteId}`, async () => {
      try {
        return await fn();
      } finally {
        await this.saving;
      }
    });
  }

  /** Called when the pair changes here or in another window (null = signed out). */
  subscribe(fn: (t: TokenPair | null) => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  /** Writes still on their way to the backend. */
  flush(): Promise<void> {
    return this.saving;
  }

  close() {
    this.unsubscribe?.();
    this.unsubscribe = null;
    this.listeners.clear();
  }

  private emit() {
    for (const fn of this.listeners) fn(this.tokens);
  }
}

function sameTokens(a: TokenPair | null, b: TokenPair | null): boolean {
  if (!a || !b) return a === b;
  return a.accessToken === b.accessToken && a.refreshToken === b.refreshToken;
}
