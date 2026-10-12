import { describe, expect, it } from "vitest";
import { MemoryTokenStore, TokenRefresher, type TokenPair } from "../shared/api";
import { SharedTokenStore, type LockFn, type TokenBackend } from "./tokens";

const now = 1_000_000;
const pair = (n: number, expiresIn = 15 * 60_000): TokenPair => ({ accessToken: `a${n}`, expiresAt: now + expiresIn, refreshToken: `r${n}`, refreshExpiresAt: now + 86400_000 });

/** The desktop main process: one saved pair per site, broadcast to every window. */
function fakeMain(initial: TokenPair | null) {
  let saved = initial;
  const windows = new Set<(id: string, t: TokenPair | null) => void>();
  const backend = (): TokenBackend => ({
    load: async () => saved,
    save: async (id, t) => {
      saved = t;
      for (const fn of windows) fn(id, t);
    },
    onChange: (fn) => {
      windows.add(fn);
      return () => windows.delete(fn);
    },
  });
  return { backend, saved: () => saved };
}

/** A FIFO lock like navigator.locks. */
function fakeLock(): LockFn {
  let tail: Promise<unknown> = Promise.resolve();
  return (_name, fn) => {
    const run = tail.then(fn, fn);
    tail = run.catch(() => {});
    return run;
  };
}

describe("SharedTokenStore", () => {
  it("loads, writes through and hears other windows", async () => {
    const main = fakeMain(pair(1));
    const a = await SharedTokenStore.open("site", main.backend());
    const b = await SharedTokenStore.open("site", main.backend());
    const other = await SharedTokenStore.open("other-site", main.backend());
    expect(a.get()?.refreshToken).toBe("r1");
    const seen: (string | null)[] = [];
    b.subscribe((t) => seen.push(t?.refreshToken ?? null));
    a.set(pair(2));
    await a.flush();
    expect(main.saved()?.refreshToken).toBe("r2");
    expect(b.get()?.refreshToken).toBe("r2");
    a.set(null);
    await a.flush();
    expect(b.get()).toBeNull();
    expect(seen).toEqual(["r2", null]);
    // Broadcasts for another site are ignored (the fake keeps one pair for all ids).
    expect(other.get()?.refreshToken).toBe("r1");
  });

  it("memory only without a backend", async () => {
    const s = await SharedTokenStore.open("site", null);
    s.set(pair(1));
    await s.reload();
    expect(s.get()?.accessToken).toBe("a1");
  });

  it("lets only one window rotate the refresh token; the other adopts the new pair", async () => {
    const main = fakeMain(pair(1, 10_000)); // inside the refresh margin
    const lock = fakeLock();
    // Window B hears about writes late (as over IPC), so it relies on reload under the lock.
    const quiet = (): TokenBackend => ({ ...main.backend(), onChange: undefined });
    const a = await SharedTokenStore.open("site", quiet(), lock);
    const b = await SharedTokenStore.open("site", quiet(), lock);
    const presented: string[] = [];
    const exchange = async (rt: string) => {
      presented.push(rt);
      await new Promise((r) => setTimeout(r, 5));
      return pair(presented.length + 1);
    };
    const ra = new TokenRefresher(a, exchange, () => now);
    const rb = new TokenRefresher(b, exchange, () => now);
    const [pa, pb] = await Promise.all([ra.refresh(), rb.refresh()]);
    expect(presented).toEqual(["r1"]);
    expect(pa.refreshToken).toBe("r2");
    expect(pb.refreshToken).toBe("r2");
    expect(main.saved()?.refreshToken).toBe("r2");
  });

  it("leaves a plain TokenStore's refresh unchanged", async () => {
    const store = new MemoryTokenStore(pair(1, 10_000));
    const r = new TokenRefresher(store, async () => pair(7), () => now);
    expect((await r.refresh()).refreshToken).toBe("r7");
  });
});
