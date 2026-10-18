import { describe, expect, it } from "vitest";
import { ApiError } from "../shared/api";
import { backfillLabel, canFindSimilar, defaultSimilar, scorePercent, scoreTone, similarError, similarRequest, unavailableText } from "./similarModel";

const now = Date.UTC(2026, 9, 16, 12);
const day = 24 * 3600e3;
const q = { createdAt: now - 3 * day, cameraId: "cam1" };

describe("similar model", () => {
  it("offers Find similar only for detections with a box on a snapshot", () => {
    const person = { kind: "Person", hasThumbnail: true, box: [0.1, 0.2, 0.4, 0.9] };
    expect(canFindSimilar(person)).toBe(true);
    expect(canFindSimilar({ ...person, kind: "Line crossing" })).toBe(true);
    expect(canFindSimilar({ ...person, hasThumbnail: false })).toBe(false);
    expect(canFindSimilar({ ...person, box: undefined })).toBe(false);
    expect(canFindSimilar({ ...person, box: [0.5, 0.2, 0.4, 0.9] })).toBe(false);
    expect(canFindSimilar({ ...person, kind: "Motion" })).toBe(false);
    expect(canFindSimilar({ ...person, kind: "audio" })).toBe(false);
  });

  it("maps the window picker and filters to the request", () => {
    expect(similarRequest(q, defaultSimilar, now)).toEqual({ limit: 48, from: now - 7 * day });
    expect(similarRequest(q, { ...defaultSimilar, window: "around" }, now)).toEqual({ limit: 48, from: q.createdAt - day, to: q.createdAt + day });
    expect(similarRequest(q, { ...defaultSimilar, window: "30d", sameCamera: true, anyKind: true }, now)).toEqual({
      limit: 48,
      from: now - 30 * day,
      cameras: ["cam1"],
      anyKind: true,
    });
    expect(similarRequest(q, { ...defaultSimilar, window: "all", limit: 500 }, now)).toEqual({ limit: 100 });
    expect(similarRequest(q, { ...defaultSimilar, window: "all", limit: 0 }, now)).toEqual({ limit: 1 });
  });

  it("shows scores as clamped percentages with a tone", () => {
    expect(scorePercent(0.876)).toBe(88);
    expect(scorePercent(-0.3)).toBe(0);
    expect(scorePercent(1.2)).toBe(100);
    expect(scorePercent(NaN)).toBe(0);
    expect(scoreTone(0.91)).toBe("green");
    expect(scoreTone(0.8)).toBe("green");
    expect(scoreTone(0.65)).toBe("amber");
    expect(scoreTone(0.4)).toBe("");
  });

  it("explains a missing model", () => {
    expect(unavailableText(null)).toBeNull();
    expect(unavailableText({ available: true, model: "sha256:x", indexed: 3 })).toBeNull();
    const t = unavailableText({ available: false, reason: "no appearance model installed (/data/models/reid.onnx)" });
    expect(t).toContain("Appearance search is unavailable. No appearance model installed (/data/models/reid.onnx).");
    expect(t).toContain("SENTINEL_REID_MODEL");
    expect(unavailableText({ available: false })).toContain("No appearance model is installed");
  });

  it("turns a 503 into the banner and anything else into an error line", () => {
    const b = similarError(new ApiError(503, "unavailable", "appearance search is unavailable: ONNX Runtime library not found"));
    expect(b.banner).toBe(true);
    expect(b.text).toContain("ONNX Runtime library not found.");
    expect(b.text).not.toContain("unavailable: ");
    const e = similarError(new ApiError(400, "bad_request", "this event has no detection box to search by"));
    expect(e).toEqual({ text: "this event has no detection box to search by", banner: false });
  });

  it("describes the index job", () => {
    expect(backfillLabel(undefined)).toBe("");
    const base = { taskId: "t1", total: 1200, processed: 300, indexed: 290, skipped: 10, startedAt: 1 };
    expect(backfillLabel({ ...base, state: "running" })).toBe("Indexing recent events: 300 of 1,200");
    expect(backfillLabel({ ...base, state: "running", total: 0 })).toBe("Indexing recent events…");
    expect(backfillLabel({ ...base, state: "done", processed: 1200, indexed: 1190 })).toBe("Index built: 1,190 of 1,200 events (10 skipped)");
    expect(backfillLabel({ ...base, state: "canceled" })).toBe("Indexing canceled after 300 of 1,200 events");
    expect(backfillLabel({ ...base, state: "failed", error: "disk" })).toBe("Indexing failed: disk");
  });
});
