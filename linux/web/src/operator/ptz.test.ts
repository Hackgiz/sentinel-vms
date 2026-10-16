import { describe, expect, it } from "vitest";
import { cleanPresetName, isStill, isTypingTarget, keyDir, velocityOf } from "./ptz";

describe("velocityOf", () => {
  it("maps held controls to velocities in -1..1", () => {
    expect(velocityOf(["right"])).toEqual({ pan: 0.5, tilt: 0, zoom: 0 });
    expect(velocityOf(["left", "up"])).toEqual({ pan: -0.5, tilt: 0.5, zoom: 0 });
    expect(velocityOf(["zoomOut"]).zoom).toBeLessThan(0);
  });
  it("cancels opposite controls", () => {
    expect(isStill(velocityOf(["left", "right", "up", "down", "zoomIn", "zoomOut"]))).toBe(true);
    expect(isStill(velocityOf([]))).toBe(true);
    expect(isStill(velocityOf(["up"]))).toBe(false);
  });
});

describe("keyDir", () => {
  it("maps arrows and +/-", () => {
    expect(keyDir("ArrowLeft")).toBe("left");
    expect(keyDir("+")).toBe("zoomIn");
    expect(keyDir("=")).toBe("zoomIn");
    expect(keyDir("-")).toBe("zoomOut");
    expect(keyDir("a")).toBeNull();
  });
});

describe("isTypingTarget", () => {
  it("ignores keys typed into fields", () => {
    expect(isTypingTarget({ tagName: "INPUT" } as unknown as EventTarget)).toBe(true);
    expect(isTypingTarget({ tagName: "DIV" } as unknown as EventTarget)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });
});

describe("cleanPresetName", () => {
  it("trims and bounds", () => {
    expect(cleanPresetName("  Gate ")).toBe("Gate");
    expect(cleanPresetName("   ")).toBeNull();
    expect(cleanPresetName("x".repeat(65))).toBeNull();
  });
});
