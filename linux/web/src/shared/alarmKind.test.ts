import { describe, expect, it } from "vitest";
import { alarmGlyph, alarmKindIcon, alarmTone, eventTags, kindGlyph, kindTone, severityTone, stateTone, tones } from "./alarmKind";

describe("alarm kind icons", () => {
  it("gives Watchlist its own icon and leaves other kinds alone", () => {
    expect(alarmKindIcon("Watchlist")).toEqual({ icon: "⚑", label: "Watchlist" });
    expect(alarmKindIcon("Person")).toBeNull();
    expect(alarmKindIcon(undefined)).toBeNull();
  });
});

describe("event tags", () => {
  it("shows the person, then the plate, skipping blanks", () => {
    expect(eventTags({ personName: "Sam", plate: "AB12" })).toEqual([
      { kind: "person", text: "Sam" },
      { kind: "plate", text: "AB12" },
    ]);
    expect(eventTags({ personName: " ", plate: undefined })).toEqual([]);
    expect(eventTags({})).toEqual([]);
  });
});

describe("iPhone chip colours and glyphs (WAVE20)", () => {
  it("colours kinds as the iPhone chips do, accent for the rest", () => {
    expect(kindTone("Person")).toBe(tones.accent);
    expect(kindTone("License Plate")).toBe(tones.amber);
    expect(kindTone("Loitering")).toBe(tones.purple);
    expect(kindTone("Watchlist")).toBe(tones.red);
    expect(kindTone("Something new")).toBe(tones.accent);
    expect(kindTone(undefined)).toBe(tones.accent);
  });
  it("picks a glyph per kind, sparkles otherwise", () => {
    expect(kindGlyph("Vehicle")).toBe("car");
    expect(kindGlyph("Unknown")).toBe("sparkles");
  });
  it("gives alarms their kind's glyph, else one by severity", () => {
    expect(alarmGlyph("Camera Offline", "Critical")).toBe("videoOff");
    expect(alarmGlyph("Tamper", "Critical")).toBe("octagon");
    expect(alarmGlyph(undefined, "Warning")).toBe("alert");
  });
  it("colours alarms by severity, Watchlist by kind", () => {
    expect(alarmTone({ kind: "Person", severity: "Critical" })).toBe(tones.red);
    expect(alarmTone({ kind: "Person", severity: "Warning" })).toBe(tones.amber);
    expect(alarmTone({ kind: "Person", severity: "Info" })).toBe(tones.accent);
    expect(alarmTone({ kind: "Watchlist", severity: "Info" })).toBe(tones.red);
    expect(severityTone("Other")).toBe(tones.accent);
  });
  it("colours states as the iPhone does", () => {
    expect(stateTone("New")).toBe(tones.red);
    expect(stateTone("Acknowledged")).toBe(tones.accent);
    expect(stateTone("Resolved")).toBe(tones.green);
    expect(stateTone("Whatever")).toBe(tones.grey);
  });
});
