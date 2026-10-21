import { describe, expect, it } from "vitest";
import { alarmKindIcon, eventTags } from "./alarmKind";

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
