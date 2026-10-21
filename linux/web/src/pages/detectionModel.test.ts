import { describe, expect, it } from "vitest";
import type { DetectorSettings, Model } from "../detectionApi";
import { anyDownloading, detectorChoices, downloadPercent, modelActions, pendingNote, purposeOf, savedModel, sortModels, stateLabel, statusLine } from "./detectionModel";

const model = (p: Partial<Model> & Pick<Model, "id">): Model => ({
  title: p.id,
  purpose: "detector",
  file: `${p.id}.onnx`,
  sizeBytes: 1000,
  license: "Apache-2.0",
  bundled: false,
  installed: false,
  state: "missing",
  source: "https://example.com/m.onnx",
  ...p,
});

const catalog: Model[] = [
  model({ id: "yolox-tiny", title: "YOLOX-Tiny (416)", bundled: true, installed: true, state: "installed", source: undefined }),
  model({ id: "yolox-s", title: "YOLOX-S (640, more accurate)", note: "Needs an accelerator for 5/s" }),
  model({ id: "reid-osnet", purpose: "reid", installed: true, state: "installed" }),
  model({ id: "face-yunet", purpose: "face-detect", state: "downloading", download: { modelId: "face-yunet", taskId: "t1", state: "running", done: 250, total: 1000 } }),
  model({ id: "custom", purpose: "detector", installed: true, state: "installed", source: undefined, note: "custom.onnx + custom.json" }),
];

const settings = (p: Partial<DetectorSettings["status"]> = {}, model = ""): DetectorSettings => ({
  model,
  rate: 0,
  status: { model: "yolox-tiny", engine: "CPU", effectiveRate: 1, pending: false, ...p },
});

describe("detector picker", () => {
  it("offers installed detectors only", () => {
    expect(detectorChoices(catalog, "yolox-tiny").map((c) => c.id)).toEqual(["yolox-tiny", "custom"]);
  });
  it("keeps the saved model visible when its file is gone", () => {
    const c = detectorChoices(catalog, "yolox-s");
    expect(c.map((x) => [x.id, x.installed])).toEqual([["yolox-tiny", true], ["yolox-s", false], ["custom", true]]);
    expect(detectorChoices([], "gone")[0]).toMatchObject({ id: "gone", installed: false });
  });
  it("reads an empty saved model as the bundled default", () => {
    expect(savedModel({ model: "" })).toBe("yolox-tiny");
    expect(savedModel({ model: "yolox-s" })).toBe("yolox-s");
  });
});

describe("status line", () => {
  it("names model, engine, device, rate and timing", () => {
    expect(statusLine(settings({ engine: "OpenVINO", device: "GPU", effectiveRate: 5, inferMs: 12.34 }), catalog)).toBe("YOLOX-Tiny (416) · OpenVINO (GPU) · 5/s per camera · 12.3 ms per frame");
    expect(statusLine(settings({ model: "x" }), [])).toBe("x · CPU · 1/s per camera");
  });
  it("says restart when pending, the reason when it can't run", () => {
    expect(pendingNote(settings())).toBeNull();
    expect(pendingNote(settings({ pending: true }))).toMatch(/Restart Sentinel/);
    expect(pendingNote(settings({ pending: true, reason: "yolox_s.onnx is missing" }))).toBe("The chosen model is not running: yolox_s.onnx is missing.");
  });
});

describe("models table", () => {
  it("computes download progress", () => {
    expect(downloadPercent(catalog[3])).toBe(25);
    expect(downloadPercent({ state: "downloading", download: { modelId: "a", taskId: "t", state: "running", done: 5, total: 0 } })).toBeNull();
    expect(downloadPercent(catalog[1])).toBeNull();
  });
  it("offers the actions each state allows", () => {
    expect(modelActions(catalog[0], "yolox-tiny", true)).toEqual([]);
    expect(modelActions(catalog[1], "yolox-tiny", true)).toEqual(["download"]);
    expect(modelActions(catalog[2], "yolox-tiny", true)).toEqual(["delete"]);
    expect(modelActions(catalog[3], "yolox-tiny", true)).toEqual(["cancel"]);
    expect(modelActions(catalog[4], "custom", true)).toEqual([]);
    expect(modelActions(catalog[4], "yolox-tiny", true)).toEqual(["delete"]);
    expect(modelActions(model({ id: "c", state: "corrupt" }), "", true)).toEqual(["download", "delete"]);
    expect(modelActions(catalog[1], "yolox-tiny", false)).toEqual([]);
  });
  it("labels states and polls while downloading", () => {
    expect(stateLabel(catalog[0])).toBe("Bundled");
    expect(stateLabel(catalog[1])).toBe("Not installed");
    expect(stateLabel({ state: "missing", bundled: false, download: { modelId: "a", taskId: "t", state: "failed", done: 0, total: 0 } })).toBe("Download failed");
    expect(anyDownloading(catalog)).toBe(true);
    expect(anyDownloading(catalog.slice(0, 3))).toBe(false);
  });
  it("groups by purpose and names them", () => {
    expect(sortModels([catalog[3], catalog[2], catalog[1]]).map((m) => m.id)).toEqual(["yolox-s", "reid-osnet", "face-yunet"]);
    expect(purposeOf("clip-text")).toBe("Semantic search (text)");
    expect(purposeOf("other")).toBe("other");
  });
});
