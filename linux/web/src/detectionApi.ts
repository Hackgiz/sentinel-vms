// Wave 16 (detection depth) calls on API v2: the model catalog and detector
// settings, faces and people, plates, and semantic search. Typed from the
// generated contract. `client` is the Admin's cookie client by default; the
// Operator passes its bearer client.
import { v2 } from "./admin/client";
import type { ApiClient, Schemas } from "./shared/api";

export type Model = Schemas["Model"];
export type ModelDownload = Schemas["ModelDownload"];
export type ModelPurpose = Model["purpose"];
export type DetectorSettings = Schemas["DetectorSettings"];
export type FacesStatus = Schemas["FacesStatus"];
export type Face = Schemas["Face"];
export type Person = Schemas["Person"];
export type PersonInput = Schemas["PersonInput"];
export type WatchAlert = Schemas["WatchAlert"];
export type LPRStatus = Schemas["LPRStatus"];
export type Plate = Schemas["Plate"];
export type PlateInput = Schemas["PlateInput"];
export type SemanticStatus = Schemas["SemanticStatus"];
export type SemanticHit = Schemas["SemanticHit"];
export type SemanticRequest = NonNullable<import("./api/v2").paths["/api/v2/events/semantic"]["post"]["requestBody"]>["content"]["application/json"];

export function detectionApi(client: ApiClient = v2) {
  const c = client;
  return {
    models: {
      /** The catalog with each model's state, and the models directory. */
      list: () => c.call("get", "/api/v2/models"),
      remove: (modelId: string) => c.call("delete", "/api/v2/models/{modelId}", { path: { modelId } }),
      download: (modelId: string) => c.call("post", "/api/v2/models/{modelId}/download", { path: { modelId } }),
      cancel: (modelId: string) => c.call("delete", "/api/v2/models/{modelId}/download", { path: { modelId } }),
    },
    detector: {
      get: () => c.call("get", "/api/v2/detector"),
      /** model "" = yolox-tiny; rate 0 = auto, else 1–5. */
      put: (body: { model?: string; rate?: number }) => c.call("put", "/api/v2/detector", { body }),
    },
    faces: {
      status: () => c.call("get", "/api/v2/faces"),
      put: (body: { enabled?: boolean; unassignedDays?: number }) => c.call("put", "/api/v2/faces", { body }),
      unassigned: async (limit = 60) => (await c.call("get", "/api/v2/faces/unassigned", { query: { limit } })).items,
      remove: (faceId: string) => c.call("delete", "/api/v2/faces/{faceId}", { path: { faceId } }),
      /** The crop's path; a bearer client loads it with `client.blob` (see shared/AuthImage). */
      cropPath: (faceId: string) => `/api/v2/faces/${encodeURIComponent(faceId)}/crop.jpg`,
      /** For <img src>: cookie mode only. */
      cropUrl: (faceId: string) => `${c.baseUrl}/api/v2/faces/${encodeURIComponent(faceId)}/crop.jpg`,
      ofEvent: async (eventId: string) => (await c.call("get", "/api/v2/events/{eventId}/faces", { path: { eventId } })).items,
    },
    people: {
      list: async () => (await c.call("get", "/api/v2/people")).items,
      get: (personId: string) => c.call("get", "/api/v2/people/{personId}", { path: { personId } }),
      create: (body: PersonInput & { faceId?: string }) => c.call("post", "/api/v2/people", { body }),
      update: (personId: string, body: PersonInput) => c.call("patch", "/api/v2/people/{personId}", { path: { personId }, body }),
      remove: (personId: string) => c.call("delete", "/api/v2/people/{personId}", { path: { personId } }),
      enroll: (personId: string, faceId: string) => c.call("post", "/api/v2/people/{personId}/faces", { path: { personId }, body: { faceId } }),
    },
    lpr: {
      status: () => c.call("get", "/api/v2/lpr"),
      put: (enabled: boolean) => c.call("put", "/api/v2/lpr", { body: { enabled } }),
    },
    plates: {
      list: async () => (await c.call("get", "/api/v2/plates")).items,
      /** One entry with its newest 20 sightings. */
      get: (plateId: string) => c.call("get", "/api/v2/plates/{plateId}", { path: { plateId } }),
      create: (body: PlateInput) => c.call("post", "/api/v2/plates", { body }),
      update: (plateId: string, body: PlateInput) => c.call("patch", "/api/v2/plates/{plateId}", { path: { plateId }, body }),
      remove: (plateId: string) => c.call("delete", "/api/v2/plates/{plateId}", { path: { plateId } }),
    },
    semantic: {
      status: () => c.call("get", "/api/v2/semantic"),
      startBackfill: () => c.call("post", "/api/v2/semantic/backfill"),
      cancelBackfill: () => c.call("delete", "/api/v2/semantic/backfill"),
      /** Local image-text search (503 when no model is installed). */
      search: (body: SemanticRequest) => c.call("post", "/api/v2/events/semantic", { body }),
    },
  };
}

export type DetectionApi = ReturnType<typeof detectionApi>;
