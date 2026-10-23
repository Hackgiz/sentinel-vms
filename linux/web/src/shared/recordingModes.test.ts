import { describe, expect, it, vi } from "vitest";
import { MemoryTokenStore, createClient } from "./api";
import {
  clampMinFree,
  clampPostRoll,
  clampPreRoll,
  effectiveModeOf,
  gbPerDay,
  keptClipLabel,
  lowResDaysOf,
  modeBadge,
  modeOf,
  noTriggerWarning,
  recommendedMode,
  recordingApi,
  recordingDefaults,
  recordingFormOf,
  recordingInput,
  recordingPatch,
  resolveMode,
  storageEstimate,
  type RecCamera,
} from "./recordingModes";

const cam = (over: Partial<RecCamera> = {}) => ({ recording: true, retentionDays: 0, subRtspUrl: "", ...over }) as RecCamera;

describe("modes", () => {
  it("reads the camera's setting, falling back to the old flag", () => {
    expect(modeOf(null)).toBe("default");
    expect(modeOf(cam())).toBe("default");
    expect(modeOf(cam({ recording: false }))).toBe("off");
    expect(modeOf(cam({ recordingMode: "events" }))).toBe("events");
  });

  it("resolves the server default, and dual without a sub-stream becomes events", () => {
    expect(resolveMode("default", "events", false)).toBe("events");
    expect(resolveMode("default", undefined, false)).toBe("continuous");
    expect(resolveMode("dual", "continuous", true)).toBe("dual");
    expect(resolveMode("dual", "continuous", false)).toBe("events");
    expect(resolveMode("default", "dual", false)).toBe("events");
  });

  it("prefers what the server says it does", () => {
    expect(effectiveModeOf(cam({ recordingMode: "default", effectiveRecordingMode: "dual" }), "continuous")).toBe("dual");
    expect(effectiveModeOf(cam({ recordingMode: "default" }), "events")).toBe("events");
    expect(effectiveModeOf(cam())).toBe("continuous"); // older server
    expect(effectiveModeOf(cam({ recording: false }))).toBe("off");
  });

  it("recommends dual only with a sub-stream", () => {
    expect(recommendedMode(true)).toBe("dual");
    expect(recommendedMode(false)).toBe("continuous");
  });

  it("labels the list badge", () => {
    expect(modeBadge("continuous").text).toBe("24/7");
    expect(modeBadge("events").text).toBe("Events");
    expect(modeBadge("dual").text).toBe("24/7 low + events");
    expect(modeBadge("off").text).toBe("Live only");
  });

  it("warns when nothing can start a recording", () => {
    expect(noTriggerWarning("events", 0, false)).toMatch(/^Nothing will start a recording/);
    expect(noTriggerWarning("dual", 0, false)).not.toBe("");
    expect(noTriggerWarning("events", 2, false)).toBe("");
    expect(noTriggerWarning("events", 0, true)).toBe("");
    expect(noTriggerWarning("continuous", 0, false)).toBe("");
  });
});

describe("numbers", () => {
  it("clamps to the ranges the server takes", () => {
    expect(clampPreRoll(45)).toBe(30);
    expect(clampPreRoll(-1)).toBe(0);
    expect(clampPostRoll(2)).toBe(5);
    expect(clampPostRoll(0)).toBe(0);
    expect(clampPostRoll(500)).toBe(120);
    expect(clampMinFree(0)).toBe(0);
    expect(clampMinFree(3)).toBe(5);
    expect(clampMinFree(70)).toBe(50);
  });

  it("works out low-res keep days", () => {
    expect(lowResDaysOf(10, null, 7)).toBe(10);
    expect(lowResDaysOf(0, { ...recordingDefaults, defaultLowResRetentionDays: 21 }, 7)).toBe(21);
    expect(lowResDaysOf(0, null, 7)).toBe(14);
  });
});

describe("editor form", () => {
  it("patches only what changed, with the new fields on a new server", () => {
    const c = cam({ recordingMode: "default", recordTrigger: "default", preRollSeconds: 0, postRollSeconds: 0, lowResRetentionDays: 0 });
    expect(recordingPatch(c, recordingFormOf(c))).toEqual({});
    expect(recordingPatch(c, { ...recordingFormOf(c), recordingMode: "dual", lowResRetentionDays: 30, preRollSeconds: 10 })).toEqual({
      recordingMode: "dual",
      lowResRetentionDays: 30,
      preRollSeconds: 10,
    });
  });

  it("sends the old recording flag to a server without modes", () => {
    const c = cam({ recording: true, retentionDays: 7 });
    expect(recordingPatch(c, { ...recordingFormOf(c), recordingMode: "off" })).toEqual({ recording: false });
    expect(recordingPatch(c, { ...recordingFormOf(c), retentionDays: 14 })).toEqual({ retentionDays: 14 });
  });

  it("creates with recording set from the mode", () => {
    expect(recordingInput({ ...recordingFormOf(null), recordingMode: "off" }).recording).toBe(false);
    expect(recordingInput(recordingFormOf(null))).toMatchObject({ recording: true, recordingMode: "default" });
  });
});

describe("estimates", () => {
  it("turns the ingest rate into GB a day", () => {
    expect(gbPerDay(4000)).toBeCloseTo(43.2, 1);
    expect(storageEstimate("continuous", 4000)).toBe("About 43 GB a day at the camera's current 4.0 Mbps.");
    expect(storageEstimate("events", 800)).toMatch(/^Up to 8\.6 GB a day/);
    expect(storageEstimate("dual", 800)).toMatch(/^Full-res: up to/);
  });
  it("shows nothing without a rate or when nothing is recorded", () => {
    expect(storageEstimate("continuous", null)).toBe("");
    expect(storageEstimate("continuous", 0)).toBe("");
    expect(storageEstimate("off", 4000)).toBe("");
  });
});

describe("health label", () => {
  it("shows the last kept clip", () => {
    const now = new Date(2026, 9, 23, 15, 0).getTime();
    expect(keptClipLabel(null, now)).toBe("Events only: no clips yet");
    expect(keptClipLabel(new Date(2026, 9, 23, 9, 5).getTime(), now)).toBe("Events only: last kept clip 09:05");
    expect(keptClipLabel(new Date(2026, 9, 23, 9, 5).toISOString(), now)).toBe("Events only: last kept clip 09:05");
    expect(keptClipLabel("not a date", now)).toBe("Events only: no clips yet");
    expect(keptClipLabel(Math.floor(new Date(2026, 9, 23, 9, 5).getTime() / 1000), now)).toBe("Events only: last kept clip 09:05"); // seconds
    expect(keptClipLabel(new Date(2026, 9, 21, 9, 5).getTime(), now)).toMatch(/^Events only: last kept clip .+ 09:05$/);
  });
});

describe("settings API", () => {
  it("reads and patches /api/v2/settings/recording", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify(recordingDefaults), { status: 200, headers: { "Content-Type": "application/json" } }));
    const now = Date.UTC(2026, 9, 23);
    const c = createClient({
      baseUrl: "https://ms.lan:8090",
      tokens: new MemoryTokenStore({ accessToken: "a", expiresAt: now + 3600e3, refreshToken: "r", refreshExpiresAt: now + 86400e3 }),
      fetch: f as unknown as typeof fetch,
      now: () => now,
    });
    expect(await recordingApi.get(c)).toEqual(recordingDefaults);
    await recordingApi.patch(c, { minFreePercent: 10 });
    const [url, init] = f.mock.calls[1] as unknown as [string, RequestInit];
    expect(url).toBe("https://ms.lan:8090/api/v2/settings/recording");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(String(init.body))).toEqual({ minFreePercent: 10 });
  });
});
