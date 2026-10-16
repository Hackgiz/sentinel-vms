import { describe, expect, it } from "vitest";
import type { Alarm, Camera } from "./api";
import { alarmOn, pinState, toNormalized, withPin } from "./floorplan";

const box = { left: 100, top: 50, width: 200, height: 100 };
const cam = (online: boolean) => ({ id: "c1", status: { online } }) as Camera;
const alarm = (state: string, cameraId = "c1") => ({ id: "a", cameraId, state }) as Alarm;

describe("toNormalized", () => {
  it("maps a point in the box to 0..1", () => {
    expect(toNormalized(200, 100, box)).toEqual({ x: 0.5, y: 0.5, inside: true });
  });
  it("clamps and flags points outside the box", () => {
    expect(toNormalized(50, 400, box)).toEqual({ x: 0, y: 1, inside: false });
    expect(toNormalized(100, 50, box).inside).toBe(true);
  });
  it("survives an empty box", () => {
    expect(toNormalized(10, 10, { left: 0, top: 0, width: 0, height: 0 }).x).toBe(0);
  });
});

describe("withPin", () => {
  const pins = [{ cameraId: "a", x: 0.1, y: 0.1, heading: 45 }];
  it("adds, moves (keeping the heading) and removes", () => {
    const added = withPin(pins, "b", { x: 0.33333333, y: 0.5 });
    expect(added).toHaveLength(2);
    expect(added.find((p) => p.cameraId === "b")).toEqual({ cameraId: "b", x: 0.3333, y: 0.5, heading: -1 });
    const moved = withPin(pins, "a", { x: 0.9, y: 0.9 });
    expect(moved).toEqual([{ cameraId: "a", x: 0.9, y: 0.9, heading: 45 }]);
    expect(withPin(pins, "a", null)).toEqual([]);
  });
});

describe("pin state", () => {
  it("an open alarm beats offline beats ok", () => {
    expect(pinState(cam(true), [], "c1")).toBe("ok");
    expect(pinState(cam(false), [], "c1")).toBe("offline");
    expect(pinState(undefined, [], "c1")).toBe("offline");
    expect(pinState(cam(false), [alarm("New")], "c1")).toBe("alarm");
  });
  it("ignores snoozed alarms and other cameras", () => {
    expect(alarmOn([alarm("Snoozed")], "c1")).toBeUndefined();
    expect(alarmOn([alarm("New", "c2")], "c1")).toBeUndefined();
    expect(alarmOn([alarm("Snoozed"), alarm("Investigating")], "c1")?.state).toBe("Investigating");
  });
});
