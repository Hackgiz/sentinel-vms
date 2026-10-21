// Wave 16's calls against hand-written fixtures shaped like the openapi
// (the Go endpoints land in other streams). fetch is mocked; nothing leaves
// the process.
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { detectionApi, type DetectorSettings, type FacesStatus, type Model, type Person, type Plate, type SemanticHit } from "./detectionApi";
import { MemoryTokenStore, createClient } from "./shared/api";
import { EventTags, KindIcon } from "./shared/KindIcon";

const now = Date.UTC(2026, 9, 21, 12);

const models: Model[] = [
  { id: "yolox-tiny", title: "YOLOX-Tiny (416)", purpose: "detector", file: "yolox_tiny.onnx", sizeBytes: 20_000_000, license: "Apache-2.0", bundled: true, installed: true, state: "installed" },
  {
    id: "yolox-s",
    title: "YOLOX-S (640, more accurate)",
    purpose: "detector",
    file: "yolox_s.onnx",
    sizeBytes: 36_000_000,
    license: "Apache-2.0",
    source: "https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_s.onnx",
    bundled: false,
    installed: false,
    state: "downloading",
    download: { modelId: "yolox-s", taskId: "t-1", state: "running", done: 9_000_000, total: 36_000_000, startedAt: now },
  },
];
const detector: DetectorSettings = { model: "", rate: 0, status: { model: "yolox-tiny", engine: "CPU", effectiveRate: 1, pending: false, inferMs: 31.5 } };
const faces: FacesStatus = { enabled: false, available: false, reason: "face_det.onnx is missing", people: 0, unassigned: 0, unassignedDays: 7 };
const person: Person = { id: "p1", name: "Sam", alert: "never", faces: [{ id: "f1", eventId: "e1", cameraId: "c1", at: now, score: 0.9, personId: "p1", similarity: 0.52 }], createdAt: now };
const plate: Plate = { id: "pl1", plate: "AB12CD", name: "Delivery van", alert: "always", createdAt: now, sightings: [] };
const hit: SemanticHit = { score: 0.31, event: { id: "e9", cameraId: "c1", kind: "Vehicle", label: "car", createdAt: now, hasThumbnail: true, plate: "AB12CD", trackId: "c1:4" } as SemanticHit["event"] };

function respond(code: number, body?: unknown) {
  return vi.fn(async () => new Response(body === undefined ? null : JSON.stringify(body), { status: code, headers: body === undefined ? {} : { "Content-Type": "application/json" } }));
}
const call = (f: ReturnType<typeof respond>, i = 0) => {
  const [url, init] = f.mock.calls[i] as unknown as [string, RequestInit];
  return { url, method: init.method, body: init.body ? JSON.parse(init.body as string) : undefined };
};

afterEach(() => vi.unstubAllGlobals());

describe("models and detector", () => {
  it("lists the catalog and the models directory", async () => {
    const f = respond(200, { items: models, dir: "/var/lib/sentinel/models" });
    vi.stubGlobal("fetch", f);
    const r = await detectionApi().models.list();
    expect(r.items[1].download?.done).toBe(9_000_000);
    expect(r.dir).toBe("/var/lib/sentinel/models");
    expect(call(f)).toMatchObject({ url: "/api/v2/models", method: "GET" });
  });

  it("starts, cancels and deletes downloads by id", async () => {
    const f = respond(201, models[1].download);
    vi.stubGlobal("fetch", f);
    const api = detectionApi();
    await api.models.download("yolox-s");
    await api.models.cancel("yolox-s");
    await api.models.remove("yolox s");
    expect([call(f, 0), call(f, 1), call(f, 2)].map((c) => `${c.method} ${c.url}`)).toEqual(["POST /api/v2/models/yolox-s/download", "DELETE /api/v2/models/yolox-s/download", "DELETE /api/v2/models/yolox%20s"]);
  });

  it("saves the model and rate, and surfaces a 400 for a missing model", async () => {
    const ok = respond(200, detector);
    vi.stubGlobal("fetch", ok);
    expect(await detectionApi().detector.put({ model: "yolox-tiny", rate: 3 })).toEqual(detector);
    expect(call(ok)).toEqual({ url: "/api/v2/detector", method: "PUT", body: { model: "yolox-tiny", rate: 3 } });
    vi.stubGlobal("fetch", respond(400, { error: { code: "bad_request", message: "yolox-s is not installed" } }));
    await expect(detectionApi().detector.put({ model: "yolox-s" })).rejects.toMatchObject({ status: 400, message: "yolox-s is not installed" });
  });
});

describe("faces and people", () => {
  it("reports 409 with the missing model when turning on", async () => {
    vi.stubGlobal("fetch", respond(409, { error: { code: "conflict", message: "face_det.onnx is missing" } }));
    await expect(detectionApi().faces.put({ enabled: true })).rejects.toMatchObject({ status: 409, message: "face_det.onnx is missing" });
  });

  it("reads status, unassigned faces and crops", async () => {
    const f = respond(200, faces);
    vi.stubGlobal("fetch", f);
    const api = detectionApi();
    expect((await api.faces.status()).reason).toBe("face_det.onnx is missing");
    vi.stubGlobal("fetch", respond(200, { items: person.faces }));
    expect(await api.faces.unassigned(25)).toHaveLength(1);
    expect(((globalThis.fetch as unknown as ReturnType<typeof respond>).mock.calls[0] as unknown as [string])[0]).toBe("/api/v2/faces/unassigned?limit=25");
    expect(api.faces.cropUrl("f/1")).toBe("/api/v2/faces/f%2F1/crop.jpg");
  });

  it("creates a person from a face and enrolls another", async () => {
    const f = respond(201, person);
    vi.stubGlobal("fetch", f);
    const api = detectionApi();
    await api.people.create({ name: "Sam", alert: "never", faceId: "f1" });
    await api.people.enroll("p1", "f2");
    await api.people.update("p1", { alert: "always" });
    expect(call(f, 0)).toEqual({ url: "/api/v2/people", method: "POST", body: { name: "Sam", alert: "never", faceId: "f1" } });
    expect(call(f, 1)).toEqual({ url: "/api/v2/people/p1/faces", method: "POST", body: { faceId: "f2" } });
    expect(call(f, 2)).toEqual({ url: "/api/v2/people/p1", method: "PATCH", body: { alert: "always" } });
  });

  it("deletes a person (204)", async () => {
    const f = respond(204);
    vi.stubGlobal("fetch", f);
    await expect(detectionApi().people.remove("p1")).resolves.toBeUndefined();
  });
});

describe("plates", () => {
  it("lists, adds and opens a plate", async () => {
    const f = respond(200, { items: [plate] });
    vi.stubGlobal("fetch", f);
    const api = detectionApi();
    expect(await api.plates.list()).toEqual([plate]);
    vi.stubGlobal("fetch", respond(201, plate));
    await api.plates.create({ plate: "AB12CD", name: "Delivery van", alert: "always" });
    const g = respond(200, { enabled: true, available: true, plates: 1, readLast24h: 4 });
    vi.stubGlobal("fetch", g);
    expect((await api.lpr.put(true)).readLast24h).toBe(4);
    expect(call(g)).toEqual({ url: "/api/v2/lpr", method: "PUT", body: { enabled: true } });
  });
});

describe("semantic search", () => {
  it("posts the description through an injected bearer client", async () => {
    const f = respond(200, { items: [hit], model: "a1b2c3" });
    const tokens = new MemoryTokenStore({ accessToken: "op", expiresAt: now + 3600_000, refreshToken: "r", refreshExpiresAt: now + 86400_000 });
    const client = createClient({ baseUrl: "https://ms.lan:8090", tokens, fetch: f as unknown as typeof fetch, now: () => now });
    const r = await detectionApi(client).semantic.search({ text: "a white van", limit: 30, from: now - 86400_000 });
    expect(r.items[0].event.plate).toBe("AB12CD");
    expect(call(f)).toEqual({ url: "https://ms.lan:8090/api/v2/events/semantic", method: "POST", body: { text: "a white van", limit: 30, from: now - 86400_000 } });
  });

  it("carries the 503 reason when no model is installed", async () => {
    vi.stubGlobal("fetch", respond(503, { error: { code: "unavailable", message: "semantic search is unavailable: clip_image.onnx is missing" } }));
    await expect(detectionApi().semantic.search({ text: "cat" })).rejects.toMatchObject({ status: 503 });
  });
});

describe("event chips and kind icon", () => {
  it("render the person, the plate and the Watchlist icon", () => {
    expect(renderToStaticMarkup(<EventTags event={{ personName: "Sam", plate: "AB12CD" }} />)).toContain("AB12CD");
    expect(renderToStaticMarkup(<EventTags event={{}} />)).toBe("");
    expect(renderToStaticMarkup(<KindIcon kind="Watchlist" />)).toContain('aria-label="Watchlist"');
    expect(renderToStaticMarkup(<KindIcon kind="Motion" />)).toBe("");
  });
});
