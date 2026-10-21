// WAVE15: Insights hosted outside the Admin. The default path (cookie client,
// hash links) must stay the Admin's; the Operator injects its client and link
// handling. fetch is mocked; nothing leaves the process.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryTokenStore, createClient } from "../shared/api";
import { RecommendationCard } from "./InsightsSection";
import { adminHref, linkTarget, loadInsights, type Recommendation } from "./insights";
import { v2 } from "./client";

const now = Date.UTC(2026, 9, 21, 12);
const respond = (body: unknown) => vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } }));
const empty = { totals: { alarms: 0, events: 0, camerasWithAlarms: 0, offlineCameras: 0 }, daily: [], mix: [], cameras: [], recommendations: [] };

afterEach(() => vi.unstubAllGlobals());

describe("loadInsights", () => {
  it("defaults to the Admin's same-origin client", async () => {
    const f = respond(empty);
    vi.stubGlobal("fetch", f);
    await loadInsights("7d", now);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`/api/v2/insights?from=${now - 7 * 86400_000}&to=${now}`);
    expect(new Headers(init.headers).get("Authorization")).toBeNull();
  });

  it("uses an injected bearer client", async () => {
    const f = respond(empty);
    vi.stubGlobal("fetch", vi.fn());
    const tokens = new MemoryTokenStore({ accessToken: "op", expiresAt: now + 3600_000, refreshToken: "r", refreshExpiresAt: now + 86400_000 });
    const client = createClient({ baseUrl: "https://ms.lan:8090", tokens, fetch: f as unknown as typeof fetch, now: () => now });
    await loadInsights("24h", now, client);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`https://ms.lan:8090/api/v2/insights?from=${now - 86400_000}&to=${now}`);
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer op");
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });
});

const rec = (action: Recommendation["action"], cameraId?: string): Recommendation => ({ id: "r", level: "suggestion", title: "t", detail: "d", ...(cameraId ? { cameraId } : {}), action });
const template = rec({ type: "openTemplate", templateId: "after-hours-people" });
const camera = rec({ type: "openCamera" }, "cam-3");
const storage = rec({ type: "openStorage" });

describe("link targets", () => {
  it("names where each link goes", () => {
    expect(linkTarget(template)).toEqual({ to: "rules", templateId: "after-hours-people" });
    expect(linkTarget(camera)).toEqual({ to: "camera", cameraId: "cam-3" });
    expect(linkTarget(storage)).toEqual({ to: "storage" });
    expect(linkTarget(rec({ type: "openTemplate" }))).toBeNull();
    expect(linkTarget(rec({ type: "setAlertOn", alertOn: "people" }, "cam-1"))).toBeNull();
  });
  it("maps to the Admin's sections", () => {
    expect(adminHref({ to: "rules", templateId: "x" })).toBe("#/admin/rules-and-events/rules?template=x");
    expect(adminHref({ to: "camera", cameraId: "c" })).toBe("#/admin/devices/cameras?camera=c");
    expect(adminHref({ to: "storage" })).toBe("#/admin/system-dashboard/system-monitor");
  });
});

describe("recommendation buttons", () => {
  const card = (r: Recommendation, host: Partial<Parameters<typeof RecommendationCard>[0]> = {}) =>
    renderToStaticMarkup(<RecommendationCard rec={r} can={() => true} onChanged={() => {}} client={v2} {...host} />);

  it("are hash links in the Admin (no onLink)", () => {
    expect(card(template)).toContain('<a class="btn small" href="#/admin/rules-and-events/rules?template=after-hours-people">Use template</a>');
    expect(card(camera)).toContain('href="#/admin/devices/cameras?camera=cam-3">Open camera</a>');
    expect(card(storage)).toContain('href="#/admin/system-dashboard/system-monitor">Check storage</a>');
  });

  it("are buttons that call the host's onLink when one is given", () => {
    const onLink = vi.fn();
    const html = card(template, { onLink });
    expect(html).toContain('<button type="button" class="btn small">Use template</button>');
    expect(html).not.toContain("<a ");
  });

  it("hide links the host can't open", () => {
    const opens = (t: { to: string }) => t.to !== "camera";
    expect(card(camera, { onLink: () => {}, opens })).not.toContain("Open camera");
    expect(card(storage, { onLink: () => {}, opens })).toContain("Check storage");
  });

  it("still follow permissions", () => {
    expect(card(template, { can: (p) => p !== "changeSettings", onLink: () => {} })).not.toContain("Use template");
  });
});
