import { describe, expect, it } from "vitest";
import type { LPRStatus, Plate } from "../detectionApi";
import { alertLabel, canToggle, filterPlates, lprStatusLine, normalizePlate, plateFormOf, plateInput, platePatch, plateProblem, plateTitle, sortPlates } from "./platesModel";

const plate = (p: Partial<Plate> & Pick<Plate, "id" | "plate">): Plate => ({ alert: "default", createdAt: 1, ...p });
const status = (p: Partial<LPRStatus> = {}): LPRStatus => ({ enabled: false, available: true, plates: 0, ...p });

describe("plates", () => {
  it("normalizes like the server", () => {
    expect(normalizePlate(" ab-12 cd ")).toBe("AB12CD");
    expect(normalizePlate("été 7")).toBe("T7");
  });
  it("validates length, duplicates, name and notes", () => {
    const list = [plate({ id: "1", plate: "ABC123" })];
    expect(plateProblem({ ...plateFormOf(null), plate: "-" })).toMatch(/at least 2/);
    expect(plateProblem({ ...plateFormOf(null), plate: "ABCDEFGHIJK" })).toMatch(/at most 10/);
    expect(plateProblem({ ...plateFormOf(null), plate: "abc 123" }, list)).toBe("ABC123 is already on the watchlist.");
    expect(plateProblem({ ...plateFormOf(list[0]) }, list, "1")).toBeNull();
    expect(plateProblem({ ...plateFormOf(null), plate: "XY12", name: "x".repeat(81) })).toMatch(/80/);
    expect(plateProblem({ ...plateFormOf(null), plate: "XY12" })).toBeNull();
  });
  it("builds inputs and patches", () => {
    expect(plateInput({ plate: "ab 12", name: " Van ", notes: "", alert: "always" })).toEqual({ plate: "AB12", name: "Van", notes: "", alert: "always" });
    const p = plate({ id: "1", plate: "AB12", name: "Van" });
    expect(platePatch(p, plateFormOf(p))).toEqual({});
    expect(platePatch(p, { ...plateFormOf(p), plate: "ZZ99", alert: "never", notes: "ours" })).toEqual({ alert: "never", notes: "ours" });
  });
  it("sorts and filters", () => {
    const list = [plate({ id: "1", plate: "ZZ1" }), plate({ id: "2", plate: "AB12", name: "Delivery van" }), plate({ id: "3", plate: "CD34" })];
    expect(sortPlates(list).map((p) => p.plate)).toEqual(["AB12", "CD34", "ZZ1"]);
    expect(filterPlates(list, "ab-1").map((p) => p.id)).toEqual(["2"]);
    expect(filterPlates(list, "van").map((p) => p.id)).toEqual(["2"]);
    expect(filterPlates(list, "  ")).toBe(list);
    expect(plateTitle(list[1])).toBe("AB12 · Delivery van");
  });
  it("explains the status", () => {
    expect(lprStatusLine(status({ available: false, reason: "plate_rec.txt is missing" }))).toContain("plate_rec.txt is missing");
    expect(lprStatusLine(status({ enabled: true, plates: 3, readLast24h: 1 }))).toBe("On · 3 on the watchlist · 1 plate read in the last 24 h");
    expect(lprStatusLine(status())).toMatch(/^Off/);
    expect(canToggle(status({ available: false }))).toBe(false);
    expect(alertLabel("always")).toBe("Critical watchlist alarm");
  });
});
