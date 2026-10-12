// Compile-time checks for the typed client (run by `npm run typecheck`; nothing executes).
import { createClient, type Camera, type Page, type AuditEntry } from "./api";

export async function typeChecks() {
  const api = createClient();
  const cam: Camera = await api.call("get", "/api/v2/cameras/{cameraId}", { path: { cameraId: "c1" } });
  const page: Page<AuditEntry> = await api.call("get", "/api/v2/audit", { query: { serverId: "", limit: 10 } });
  const none: void = await api.call("delete", "/api/v2/cameras/{cameraId}", { path: { cameraId: "c1" } });
  // @ts-expect-error path parameters are required
  await api.call("get", "/api/v2/cameras/{cameraId}");
  // @ts-expect-error no such route for PUT
  await api.call("put", "/api/v2/cameras");
  // @ts-expect-error body must match CameraGroupInput
  await api.call("post", "/api/v2/camera-groups", { body: { title: "x" } });
  // @ts-expect-error unknown query parameter type
  await api.call("get", "/api/v2/alarms", { query: { state: "closed" } });
  return [cam, page, none];
}
