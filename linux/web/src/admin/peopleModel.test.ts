import { describe, expect, it } from "vitest";
import type { FacesStatus, Person } from "../detectionApi";
import { DAYS_DEFAULT, ago, alertLabel, canToggle, clampDays, facesStatusLine, lastSeen, personFormOf, personInput, personPatch, personProblem, sortPeople } from "./peopleModel";

const now = Date.UTC(2026, 9, 21, 12);
const person = (p: Partial<Person> & Pick<Person, "id" | "name">): Person => ({ alert: "default", faces: [], createdAt: 1, ...p });
const status = (p: Partial<FacesStatus> = {}): FacesStatus => ({ enabled: false, available: true, people: 0, unassigned: 0, unassignedDays: 7, ...p });

describe("faces status", () => {
  it("explains why it can't run, naming the models", () => {
    const s = facesStatusLine(status({ available: false, reason: "face_rec.onnx is missing" }));
    expect(s).toContain("face_rec.onnx is missing");
    expect(s).toContain("System → Models");
    expect(facesStatusLine(status({ available: false }))).toContain("not installed");
  });
  it("counts people and unassigned faces when on", () => {
    expect(facesStatusLine(status({ enabled: true, people: 1, unassigned: 3 }))).toBe("On · 1 person · 3 unassigned faces");
    expect(facesStatusLine(status())).toMatch(/^Off/);
  });
  it("allows turning on only with the models, off always", () => {
    expect(canToggle(status({ available: false }))).toBe(false);
    expect(canToggle(status({ available: false, enabled: true }))).toBe(true);
    expect(canToggle(status())).toBe(true);
  });
  it("clamps unassigned days to 1–90", () => {
    expect(clampDays(0)).toBe(1);
    expect(clampDays(365)).toBe(90);
    expect(clampDays(NaN)).toBe(DAYS_DEFAULT);
    expect(clampDays(14.2)).toBe(14);
  });
});

describe("people list", () => {
  it("sorts by name, ignoring case", () => {
    expect(sortPeople([person({ id: "1", name: "zoe" }), person({ id: "2", name: "Adam" }), person({ id: "3", name: "beth" })]).map((p) => p.name)).toEqual(["Adam", "beth", "zoe"]);
  });
  it("says when and where they were last seen", () => {
    const cams = (id: string) => (id === "c1" ? "Front door" : undefined);
    expect(lastSeen(person({ id: "1", name: "A" }), cams, now)).toBe("Never");
    expect(lastSeen(person({ id: "1", name: "A", lastSeenAt: now - 3 * 3600e3, lastSeenCameraId: "c1" }), cams, now)).toBe("3 h ago · Front door");
    expect(lastSeen(person({ id: "1", name: "A", lastSeenAt: now - 120e3, lastSeenCameraId: "gone" }), cams, now)).toBe("2 min ago");
  });
  it("formats relative times", () => {
    expect(ago(now - 10e3, now)).toBe("Just now");
    expect(ago(now - 86400e3, now)).toBe("1 day ago");
    expect(ago(now - 3 * 86400e3, now)).toBe("3 days ago");
    expect(ago(undefined, now)).toBe("Never");
  });
  it("labels alert settings", () => {
    expect(alertLabel("never")).toBe("Don't alarm (known person)");
    expect(alertLabel(undefined)).toBe("Name them in the alarm");
  });
});

describe("person form", () => {
  it("validates the name and notes", () => {
    expect(personProblem({ name: "  ", notes: "", alert: "default" })).toBe("Enter a name.");
    expect(personProblem({ name: "x".repeat(81), notes: "", alert: "default" })).toMatch(/80/);
    expect(personProblem({ name: "Sam", notes: "x".repeat(501), alert: "default" })).toMatch(/500/);
    expect(personProblem({ name: "Sam", notes: "", alert: "default" })).toBeNull();
  });
  it("builds a trimmed input and a patch of only what changed", () => {
    expect(personInput({ name: " Sam ", notes: " gardener ", alert: "never" })).toEqual({ name: "Sam", notes: "gardener", alert: "never" });
    const p = person({ id: "1", name: "Sam", alert: "default" });
    expect(personPatch(p, personFormOf(p))).toEqual({});
    expect(personPatch(p, { ...personFormOf(p), alert: "always", notes: "VIP" })).toEqual({ alert: "always", notes: "VIP" });
    expect(personPatch(p, { ...personFormOf(p), name: "Samuel" })).toEqual({ name: "Samuel" });
    expect(personFormOf(null)).toEqual({ name: "", notes: "", alert: "default" });
  });
});
