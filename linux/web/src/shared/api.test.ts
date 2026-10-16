import { describe, expect, it, vi } from "vitest";
import { ApiError, MemoryTokenStore, createClient, errorFromResponse, fetchAll, pages, type Page, type TokenPair } from "./api";

type Call = { url: string; init: RequestInit };

/** A fetch fake answering from a handler; records every call. */
function fakeFetch(handler: (url: string, init: RequestInit, n: number) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init: init ?? {} });
    return handler(url, init ?? {}, calls.length);
  });
  return { fn: fn as unknown as typeof fetch, calls };
}

const json = (status: number, body: unknown) => new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const header = (c: Call, name: string) => (c.init.headers as Record<string, string> | undefined)?.[name];

const user = { id: "u1", name: "ana", role: "Admin", createdAt: 1, source: "local", scope: { allCameras: true, groupIds: [] } };
const tokenResponse = (n: number, now: number) => ({
  tokenType: "Bearer",
  accessToken: `access-${n}`,
  expiresAt: now + 15 * 60_000,
  refreshToken: `refresh-${n}`,
  refreshExpiresAt: now + 30 * 86400_000,
  user,
});

describe("errorFromResponse", () => {
  it("reads the v2 error shape", () => {
    const e = errorFromResponse(409, JSON.stringify({ error: { code: "conflict", message: "The last Admin can't be deleted." } }));
    expect(e).toBeInstanceOf(ApiError);
    expect(e.status).toBe(409);
    expect(e.code).toBe("conflict");
    expect(e.message).toBe("The last Admin can't be deleted.");
  });

  it("reads the v1 shape and keeps a code from the status", () => {
    const e = errorFromResponse(403, JSON.stringify({ error: "admins only" }));
    expect(e.code).toBe("forbidden");
    expect(e.message).toBe("admins only");
  });

  it("falls back to a readable message for non-JSON bodies", () => {
    const e = errorFromResponse(503, "<html>Bad gateway</html>");
    expect(e.code).toBe("unavailable");
    expect(e.message).toMatch(/recording server/i);
    expect(errorFromResponse(500, "").code).toBe("internal");
  });
});

describe("client requests", () => {
  it("throws a typed ApiError from a v2 error body", async () => {
    const f = fakeFetch(() => json(404, { error: { code: "not_found", message: "no such camera" } }));
    const api = createClient({ fetch: f.fn });
    const err = await api.cameras.get("nope").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, code: "not_found", message: "no such camera" });
    expect(f.calls[0].url).toBe("/api/v2/cameras/nope");
  });

  it("maps a failed fetch to code network", async () => {
    const api = createClient({ fetch: (async () => Promise.reject(new TypeError("Failed to fetch"))) as typeof fetch });
    await expect(api.me()).rejects.toMatchObject({ status: 0, code: "network" });
  });

  it("uploads a floorplan as multipart without forcing a JSON content type", async () => {
    const plan = { id: "p1", name: "Ground", sortOrder: 0, width: 4, height: 3, mime: "image/png", updatedAt: 1, pins: [] };
    const f = fakeFetch(() => json(201, plan));
    const api = createClient({ fetch: f.fn });
    await expect(api.floorplans.create("Ground", new Blob(["x"], { type: "image/png" }))).resolves.toMatchObject({ id: "p1" });
    const c = f.calls[0];
    expect(c.url).toBe("/api/v2/floorplans");
    expect(c.init.method).toBe("POST");
    expect(c.init.body).toBeInstanceOf(FormData);
    expect((c.init.body as FormData).get("name")).toBe("Ground");
    expect(header(c, "Content-Type")).toBeUndefined();
    expect(header(c, "X-Sentinel-Upload")).toBe("1");
  });

  it("returns a server log as text, even when it looks like JSON", async () => {
    const f = fakeFetch(() => new Response('{"level":"INFO"}\n[x]', { status: 200, headers: { "Content-Type": "text/plain; charset=utf-8" } }));
    const api = createClient({ fetch: f.fn });
    await expect(api.servers.log("rs 2", 50)).resolves.toBe('{"level":"INFO"}\n[x]');
    expect(f.calls[0].url).toBe("/api/v2/recording-servers/rs%202/log?lines=50");
    const down = fakeFetch(() => json(503, { error: { code: "unavailable", message: "the recording server isn't connected" } }));
    await expect(createClient({ fetch: down.fn }).servers.log("rs2")).rejects.toMatchObject({ status: 503, code: "unavailable" });
  });

  it("uses the session cookie in cookie mode, sends JSON on writes, encodes paths and queries", async () => {
    const f = fakeFetch(() => new Response(null, { status: 204 }));
    const onUnauthorized = vi.fn();
    const api = createClient({ fetch: f.fn, onUnauthorized });
    await expect(api.groups.remove("a/b")).resolves.toBeUndefined();
    expect(f.calls[0].url).toBe("/api/v2/camera-groups/a%2Fb");
    expect(f.calls[0].init.method).toBe("DELETE");
    expect(f.calls[0].init.credentials).toBe("same-origin");
    expect(header(f.calls[0], "Content-Type")).toBe("application/json");
    expect(header(f.calls[0], "Authorization")).toBeUndefined();

    await api.audit.page({ serverId: "", limit: 50 });
    expect(f.calls[1].url).toBe("/api/v2/audit?serverId=&limit=50");
  });

  it("calls the unauthorized handler on a 401 in cookie mode", async () => {
    const onUnauthorized = vi.fn();
    const api = createClient({ fetch: fakeFetch(() => json(401, { error: { code: "unauthorized", message: "sign in" } })).fn, onUnauthorized });
    await expect(api.me()).rejects.toMatchObject({ code: "unauthorized" });
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });
});

describe("paging", () => {
  const book: Record<string, Page<number>> = {
    "": { items: [1, 2], nextCursor: "b" },
    b: { items: [3, 4], nextCursor: "c" },
    c: { items: [5] },
  };
  const fetchPage = vi.fn(async (cursor: string | undefined) => book[cursor ?? ""]);

  it("follows nextCursor to the last page", async () => {
    fetchPage.mockClear();
    expect(await fetchAll(fetchPage)).toEqual([1, 2, 3, 4, 5]);
    expect(fetchPage.mock.calls.map((c) => c[0])).toEqual([undefined, "b", "c"]);
  });

  it("stops at max items and at maxPages", async () => {
    expect(await fetchAll(fetchPage, 3)).toEqual([1, 2, 3]);
    const seen: number[][] = [];
    for await (const items of pages(fetchPage, 2)) seen.push(items);
    expect(seen).toEqual([
      [1, 2],
      [3, 4],
    ]);
  });

  it("doesn't loop when a server repeats a cursor", async () => {
    const stuck = vi.fn(async () => ({ items: [1], nextCursor: "same" }));
    expect(await fetchAll(stuck)).toEqual([1, 1]);
  });
});

describe("token mode", () => {
  const now = 1_700_000_000_000;
  const pair = (expiresIn: number): TokenPair => ({ accessToken: "old", expiresAt: now + expiresIn, refreshToken: "r0", refreshExpiresAt: now + 86400_000 });

  it("signs in with a password grant and sends the bearer token", async () => {
    const store = new MemoryTokenStore();
    const f = fakeFetch((url) => (url.endsWith("/auth/token") ? json(200, tokenResponse(1, now)) : json(200, { items: [] })));
    const api = createClient({ baseUrl: "https://ms.lan:8090/", tokens: store, fetch: f.fn, now: () => now, clientName: "Sentinel Operator (Linux)" });
    await api.signIn("ana", "pw");
    expect(JSON.parse(String(f.calls[0].init.body))).toEqual({ grantType: "password", name: "ana", password: "pw", client: "Sentinel Operator (Linux)" });
    await api.cameras.list();
    expect(f.calls[1].url).toBe("https://ms.lan:8090/api/v2/cameras");
    expect(header(f.calls[1], "Authorization")).toBe("Bearer access-1");
    expect(f.calls[1].init.credentials).toBe("omit");
  });

  it("refreshes an access token about to expire once, however many calls are waiting", async () => {
    const store = new MemoryTokenStore(pair(10_000)); // inside the refresh margin
    let refreshes = 0;
    const f = fakeFetch(async (url, init) => {
      if (url.endsWith("/auth/token")) {
        refreshes++;
        expect(JSON.parse(String(init.body))).toEqual({ grantType: "refresh", refreshToken: "r0" });
        await new Promise((r) => setTimeout(r, 5));
        return json(200, tokenResponse(refreshes, now));
      }
      return json(200, { items: [] });
    });
    const api = createClient({ tokens: store, fetch: f.fn, now: () => now });
    await Promise.all([api.cameras.list(), api.rules.list(), api.users.list()]);
    expect(refreshes).toBe(1);
    const apiCalls = f.calls.filter((c) => !c.url.endsWith("/auth/token"));
    expect(apiCalls.map((c) => header(c, "Authorization"))).toEqual(["Bearer access-1", "Bearer access-1", "Bearer access-1"]);
    expect(store.get()?.refreshToken).toBe("refresh-1");
  });

  it("refreshes and retries once on a 401", async () => {
    const store = new MemoryTokenStore(pair(10 * 60_000));
    const f = fakeFetch((url, init) => {
      if (url.endsWith("/auth/token")) return json(200, tokenResponse(2, now));
      return header({ url, init }, "Authorization") === "Bearer old" ? json(401, { error: { code: "unauthorized", message: "expired" } }) : json(200, { ...user, permissions: [] });
    });
    const api = createClient({ tokens: store, fetch: f.fn, now: () => now });
    await expect(api.me()).resolves.toMatchObject({ name: "ana" });
    expect(f.calls.map((c) => c.url)).toEqual(["/api/v2/me", "/api/v2/auth/token", "/api/v2/me"]);
  });

  it("ends the sign-in when the refresh token is refused", async () => {
    const store = new MemoryTokenStore(pair(10 * 60_000));
    const onUnauthorized = vi.fn();
    const f = fakeFetch((url) =>
      url.endsWith("/auth/token") ? json(401, { error: { code: "token_reused", message: "signed out" } }) : json(401, { error: { code: "unauthorized", message: "expired" } }),
    );
    const api = createClient({ tokens: store, fetch: f.fn, now: () => now, onUnauthorized });
    await expect(api.me()).rejects.toMatchObject({ status: 401 });
    expect(store.get()).toBeNull();
    expect(onUnauthorized).toHaveBeenCalledOnce();
  });

  it("keeps the sign-in through a network blip during refresh", async () => {
    const store = new MemoryTokenStore(pair(10_000));
    const f = fakeFetch((url) => {
      if (url.endsWith("/auth/token")) throw new TypeError("offline");
      return json(200, { items: [] });
    });
    const api = createClient({ tokens: store, fetch: f.fn, now: () => now });
    await api.cameras.list().catch(() => {});
    expect(store.get()?.refreshToken).toBe("r0");
  });

  it("revokes the refresh token on sign-out", async () => {
    const store = new MemoryTokenStore(pair(10 * 60_000));
    const f = fakeFetch(() => new Response(null, { status: 204 }));
    const api = createClient({ tokens: store, fetch: f.fn, now: () => now });
    await api.signOut();
    expect(store.get()).toBeNull();
    expect(f.calls[0].url).toBe("/api/v2/auth/revoke");
    expect(JSON.parse(String(f.calls[0].init.body))).toEqual({ refreshToken: "r0" });
  });
});
