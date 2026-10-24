import { describe, expect, it } from "vitest";
import { cleanPresetName, isStill, isTypingTarget, keyDir, PtzHold, ptzVisible, velocityOf } from "./ptz";

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

describe("ptzVisible", () => {
  it("needs support, an online camera and the right", () => {
    expect(ptzVisible({ supported: true, online: true, canControl: true })).toBe(true);
    expect(ptzVisible({ supported: false, online: true, canControl: true })).toBe(false);
    expect(ptzVisible({ supported: true, online: false, canControl: true })).toBe(false);
    expect(ptzVisible({ supported: true, online: true, canControl: false })).toBe(false);
  });
});

function recorder() {
  const calls: string[] = [];
  const mover = {
    move: async (id: string, pan: number, tilt: number, zoom: number) => void calls.push(`move ${id} ${pan} ${tilt} ${zoom}`),
    stop: async (id: string) => void calls.push(`stop ${id}`),
  };
  return { calls, mover };
}

describe("PtzHold", () => {
  it("moves on press and stops on release", async () => {
    const { calls, mover } = recorder();
    const h = new PtzHold(mover, "c1");
    h.press("left");
    h.release("left");
    await h.idle();
    expect(calls).toEqual(["move c1 -0.5 0 0", "stop c1"]);
  });

  it("ignores repeats and a second release (pointer up, then lost capture)", async () => {
    const { calls, mover } = recorder();
    const h = new PtzHold(mover, "c1");
    h.press("up");
    h.press("up");
    h.release("up");
    h.release("up");
    await h.idle();
    expect(calls).toEqual(["move c1 0 0.5 0", "stop c1"]);
  });

  it("keeps moving while another control is still held", async () => {
    const { calls, mover } = recorder();
    const h = new PtzHold(mover, "c1");
    h.press("right");
    h.press("zoomIn");
    h.release("right");
    h.release("zoomIn");
    await h.idle();
    expect(calls).toEqual(["move c1 0.5 0 0", "move c1 0.5 0 0.4", "move c1 0 0 0.4", "stop c1"]);
  });

  it("stops on releaseAll (unmount, window blur, camera offline) while moving", async () => {
    const { calls, mover } = recorder();
    const h = new PtzHold(mover, "c1");
    h.press("down");
    h.press("left");
    h.releaseAll();
    await h.idle();
    expect(calls.at(-1)).toBe("stop c1");
    expect(calls.filter((c) => c.startsWith("stop"))).toHaveLength(1);
  });

  it("sends nothing on releaseAll when the camera is not moving", async () => {
    const { calls, mover } = recorder();
    const h = new PtzHold(mover, "c1");
    h.releaseAll();
    h.press("left");
    h.release("left");
    h.releaseAll();
    await h.idle();
    expect(calls).toEqual(["move c1 -0.5 0 0", "stop c1"]);
  });

  it("keeps commands in order even when a move is slow, and reports failures", async () => {
    const calls: string[] = [];
    const errors: unknown[] = [];
    let finish = () => {};
    const mover = {
      move: (id: string) =>
        new Promise<void>((resolve) => {
          finish = () => (calls.push(`move ${id}`), resolve());
        }),
      stop: async () => {
        calls.push("stop");
        throw new Error("camera said no");
      },
    };
    const h = new PtzHold(mover, "c1", (e) => errors.push(e));
    h.press("left");
    h.release("left");
    await Promise.resolve();
    finish();
    await h.idle();
    expect(calls).toEqual(["move c1", "stop"]);
    expect(errors).toHaveLength(1);
  });

  it("lights the last held control", () => {
    const lit: (string | null)[] = [];
    const h = new PtzHold(recorder().mover, "c1", undefined, (d) => lit.push(d));
    h.press("up");
    h.press("zoomOut");
    h.release("zoomOut");
    h.release("up");
    expect(lit).toEqual(["up", "zoomOut", "up", null]);
  });
});
