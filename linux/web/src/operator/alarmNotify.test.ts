import { describe, expect, it } from "vitest";
import type { Alarm } from "../shared/api";
import type { StreamMessage } from "../shared/events";
import { alarmNotice, noticeFor } from "./alarmNotify";

const alarm = (over: Partial<Alarm> = {}): Alarm => ({
  id: "a1",
  kind: "Person",
  title: "Person at Front door",
  detail: "",
  severity: "Warning",
  state: "New",
  owner: "",
  createdAt: 0,
  updatedAt: 0,
  lastEventAt: 0,
  eventCount: 1,
  responseLog: [],
  cameraName: "Front door",
  ...over,
});

describe("alarmNotice", () => {
  it("names the alarm, its camera, site and severity", () => {
    expect(alarmNotice(alarm(), "Home")).toEqual({ alarmId: "a1", title: "Person at Front door", body: "Front door · Home · Warning", critical: false });
  });
  it("marks Critical and Watchlist alarms critical, falls back to the kind", () => {
    expect(alarmNotice(alarm({ severity: "Critical" })).critical).toBe(true);
    expect(alarmNotice(alarm({ kind: "Watchlist", severity: "Info" })).critical).toBe(true);
    expect(alarmNotice(alarm({ title: "", cameraName: undefined, detail: " Seen twice " })).title).toBe("Person");
    expect(alarmNotice(alarm({ title: "", cameraName: undefined, detail: " Seen twice " })).body).toBe("Warning · Seen twice");
  });
});

describe("noticeFor", () => {
  const msg = (type: "alarm.new" | "alarm.updated", a: Alarm): StreamMessage => ({ type, at: 0, data: a });
  it("notifies once per new alarm, never for updates or handled alarms", () => {
    const seen = new Set<string>();
    expect(noticeFor(msg("alarm.new", alarm()), seen)?.id).toBe("a1");
    expect(noticeFor(msg("alarm.new", alarm()), seen)).toBeNull();
    expect(noticeFor(msg("alarm.updated", alarm({ id: "a2" })), seen)).toBeNull();
    expect(noticeFor(msg("alarm.new", alarm({ id: "a3", state: "Acknowledged" })), seen)).toBeNull();
    expect(noticeFor({ type: "lagged", at: 0, data: { dropped: 1 } }, seen)).toBeNull();
  });
});
