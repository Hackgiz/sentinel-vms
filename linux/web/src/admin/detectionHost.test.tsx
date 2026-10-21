// WAVE17: People, Plates and the Detection card hosted outside the Admin.
// The default path (cookie client, <img src>) must stay the Admin's; the
// Operator injects its bearer client, and images then come through it.
// fetch is mocked; nothing leaves the process.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { detectionControls } from "../pages/DetectionCard";
import AuthImage, { imageSource } from "../shared/AuthImage";
import { MemoryTokenStore, createClient } from "../shared/api";
import { v2 } from "./client";
import { DetectionContext, detectionCtx, thumbnailPath } from "./detectionHost";
import { FaceImg } from "./PeopleSection";

const now = Date.UTC(2026, 9, 21, 12);
const respond = (body: unknown) => vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
const bearer = (f: ReturnType<typeof vi.fn>) =>
  createClient({
    baseUrl: "https://ms.lan:8090",
    tokens: new MemoryTokenStore({ accessToken: "op", expiresAt: now + 3600_000, refreshToken: "r", refreshExpiresAt: now + 86400_000 }),
    fetch: f as unknown as typeof fetch,
    now: () => now,
  });
const sent = (f: ReturnType<typeof vi.fn>, i = 0) => {
  const [url, init] = f.mock.calls[i] as unknown as [string, RequestInit];
  return { url, auth: new Headers(init.headers).get("Authorization"), credentials: init.credentials };
};

afterEach(() => vi.unstubAllGlobals());

describe("detectionCtx", () => {
  it("defaults to the Admin's cookie client with direct images", async () => {
    const f = respond({ items: [] });
    vi.stubGlobal("fetch", f);
    const ctx = detectionCtx();
    expect(ctx.client).toBe(v2);
    expect(ctx.direct).toBe(true);
    await ctx.api.people.list();
    expect(sent(f)).toEqual({ url: "/api/v2/people", auth: null, credentials: "same-origin" });
  });

  it("sends every call through an injected bearer client, and fetches images", async () => {
    const f = respond({ items: [] });
    vi.stubGlobal("fetch", vi.fn());
    const ctx = detectionCtx(bearer(f));
    expect(ctx.direct).toBe(false);
    await ctx.api.plates.list();
    await ctx.api.faces.status();
    await ctx.client.cameras.list();
    expect(sent(f, 0)).toEqual({ url: "https://ms.lan:8090/api/v2/plates", auth: "Bearer op", credentials: "omit" });
    expect(sent(f, 1).url).toBe("https://ms.lan:8090/api/v2/faces");
    expect(sent(f, 2).auth).toBe("Bearer op");
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("loads a face crop and a snapshot with the bearer token", async () => {
    const f = vi.fn(async () => new Response(new Blob(["jpg"]), { status: 200 }));
    const ctx = detectionCtx(bearer(f));
    await ctx.client.blob(ctx.api.faces.cropPath("f 1"));
    await ctx.client.blob(thumbnailPath("e1"));
    expect(sent(f, 0)).toMatchObject({ url: "https://ms.lan:8090/api/v2/faces/f%201/crop.jpg", auth: "Bearer op" });
    expect(sent(f, 1)).toMatchObject({ url: "https://ms.lan:8090/api/v2/events/e1/thumbnail.jpg", auth: "Bearer op" });
  });
});

describe("images", () => {
  it("uses <img src> only for a direct (cookie) client", () => {
    const c = bearer(vi.fn());
    expect(imageSource(v2, "/api/v2/faces/f1/crop.jpg", true)).toEqual({ src: "/api/v2/faces/f1/crop.jpg" });
    expect(imageSource(c, "/api/v2/faces/f1/crop.jpg", false)).toEqual({ fetch: "/api/v2/faces/f1/crop.jpg" });
  });

  it("renders the Admin's face crop as before", () => {
    const html = renderToStaticMarkup(<FaceImg faceId="f1" />);
    expect(html).toContain('src="/api/v2/faces/f1/crop.jpg"');
    expect(html).toContain('class="ppl-crop"');
  });

  it("never puts a token-less URL on an <img> in the Operator", () => {
    const ctx = detectionCtx(bearer(vi.fn()));
    const html = renderToStaticMarkup(
      <DetectionContext.Provider value={ctx}>
        <FaceImg faceId="f1" />
      </DetectionContext.Provider>,
    );
    expect(html).not.toContain("<img");
    expect(html).toContain('aria-label="Face"');
    expect(renderToStaticMarkup(<AuthImage client={ctx.client} path={thumbnailPath("e1")} direct={false} alt="" placeholder={<div className="event-ph">…</div>} />)).toBe('<div class="event-ph">…</div>');
  });
});

describe("Detection card controls", () => {
  it("follow the Admin user's role, or the host's ChangeSettings check", () => {
    expect(detectionControls({ user: { id: "u", name: "a", role: "Admin", createdAt: 0 } })).toBe(true);
    expect(detectionControls({ user: { id: "u", name: "v", role: "Viewer", createdAt: 0 } })).toBe(false);
    const client = bearer(vi.fn());
    expect(detectionControls({ client, canChange: true })).toBe(true);
    expect(detectionControls({ client, canChange: false })).toBe(false);
  });
});
