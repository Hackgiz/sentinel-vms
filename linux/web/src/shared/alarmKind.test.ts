import { describe, expect, it } from "vitest";
import { alarmGlyph, alarmKindIcon, alarmTone, eventTags, eventKinds, kindGlyph, kindLabel, kindMatches, kindTone, markerTone, severityTone, stateTone, tones } from "./alarmKind";
import { iconNames } from "./Icons";

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
    expect(kindTone("Person")).toBe(tones.teal);
    expect(kindTone("License Plate")).toBe(tones.amber);
    expect(kindTone("Loitering")).toBe(tones.purple);
    expect(kindTone("Watchlist")).toBe(tones.red);
    expect(kindTone("Something new")).toBe(tones.slate);
    expect(kindTone(undefined)).toBe(tones.slate);
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

describe("PARITY-342 kind palette and icons", () => {
  const kinds = ["Person", "Vehicle", "Face", "License Plate", "Animal", "Package", "Loitering", "Line crossing", "Intrusion", "audio", "Sound", "Watchlist", "Motion"];
  it("colours every kind as contract §1 does", () => {
    expect(kindTone("Vehicle")).toBe("#3399ff");
    expect(kindTone("Face")).toBe("#ff8c1a");
    expect(kindTone("Package")).toBe("#c8955a");
    expect(kindTone("Intrusion")).toBe("#f2559a");
    expect(kindTone("Sound")).toBe("#8b7cf6");
    expect(kindTone("Motion")).toBe(tones.slate);
  });
  it("has a drawn icon for every kind (§2)", () => {
    for (const k of kinds) {
      expect(kindGlyph(k), k).not.toBe("sparkles");
      expect(iconNames, k).toContain(kindGlyph(k));
    }
    expect(kindGlyph("Package")).toBe("package");
    expect(kindGlyph("Intrusion")).toBe("intrusion");
    expect(kindGlyph("audio")).toBe("waveform");
    expect(alarmGlyph("Package", "Info")).toBe("package");
    expect(alarmGlyph("Sound", "Info")).toBe("waveform");
  });
  it("colours timeline markers: events by kind, alarms by alarm colour", () => {
    expect(markerTone({ kind: "Vehicle", alarm: false })).toBe(kindTone("Vehicle"));
    expect(markerTone({ kind: "Motion", alarm: false })).toBe(tones.slate);
    expect(markerTone({ kind: "Person", alarm: true, severity: "Critical" })).toBe(tones.red);
    expect(markerTone({ kind: "Person", alarm: true, severity: "Warning" })).toBe(tones.amber);
    expect(markerTone({ kind: "Watchlist", alarm: true, severity: "Info" })).toBe(tones.red);
  });
});

describe("event kind filter (PARITY-342)", () => {
  it("lists every kind the server records, each with a colour other than slate except Motion", () => {
    expect(eventKinds).toContain("Face");
    expect(eventKinds).toContain("License Plate");
    expect(eventKinds).toContain("Package");
    for (const k of eventKinds) if (k !== "Motion") expect(kindTone(k), k).not.toBe(tones.slate);
  });
  it("matches kinds ignoring case, audio as Sound", () => {
    expect(kindMatches("License plate", "License Plate")).toBe(true);
    expect(kindMatches("Person", "")).toBe(true);
    expect(kindMatches("Person", "Vehicle")).toBe(false);
    expect(kindMatches("Sound", "audio")).toBe(true);
    expect(kindLabel("audio")).toBe("Sound");
    expect(kindLabel("Face")).toBe("Face");
  });
});
