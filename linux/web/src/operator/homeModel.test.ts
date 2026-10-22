import { describe, expect, it } from "vitest";
import { tones } from "../shared/alarmKind";
import {
  cameraCounts,
  cameraTiles,
  connectionLine,
  eventsToday,
  insightsLine,
  isLocalAddress,
  latestStill,
  mergeEvent,
  needsAttention,
  peopleLine,
  recentActivity,
  statTiles,
  type HomeAlarm,
  type HomeCamera,
  type HomeEvent,
} from "./homeModel";

const cam = (id: string, online: boolean, recording = true, location = ""): HomeCamera => ({ id, name: id, location, recording, status: { online } });
const ev = (id: string, cameraId: string, createdAt: number, hasThumbnail = true): HomeEvent => ({ id, cameraId, kind: "Person", createdAt, hasThumbnail });
const alarm = (id: string, state: string, severity: string, lastEventAt: number): HomeAlarm => ({ id, state, severity, lastEventAt });

describe("header line", () => {
  it("says where the site is and whether it is live", () => {
    expect(connectionLine("open", "https://192.168.2.10:8443")).toEqual({ text: "Local network · live", tone: tones.green });
    expect(connectionLine("reconnecting", "http://nvr.local:8090").text).toBe("Local network · reconnecting…");
    expect(connectionLine("open", "https://cams.example.com").text).toBe("Remote · live");
    expect(connectionLine("stopped", "https://cams.example.com")).toEqual({ text: "Remote · not connected", tone: tones.red });
  });
  it("knows private, link-local and bare host names", () => {
    for (const a of ["http://127.0.0.1:8090", "http://10.0.0.4", "http://172.20.1.1", "http://localhost:5173", "http://nas:8090", "http://[::1]:8090", "http://[fd00::4]/"]) expect(isLocalAddress(a)).toBe(true);
    for (const a of ["https://172.32.0.1", "https://8.8.8.8", "https://relay.sentvms.com", "http://[2001:db8::1]/", "not a url"]) expect(isLocalAddress(a)).toBe(false);
  });
});

describe("stat tiles", () => {
  const cams = [cam("a", true), cam("b", true, false), cam("c", false)];
  it("counts cameras online, offline and recording (online only)", () => {
    expect(cameraCounts(cams)).toEqual({ total: 3, online: 2, offline: 1, recording: 1 });
    expect(cameraCounts([cam("x", false, true)]).recording).toBe(0);
  });
  it("builds the four tiles with the iPhone's notes and tones", () => {
    const t = statTiles(cameraCounts(cams), 5, [alarm("1", "New", "Critical", 1), alarm("2", "Acknowledged", "Info", 2)]);
    expect(t.map((x) => x.id)).toEqual(["cameras", "recording", "events", "alarms"]);
    expect(t[0]).toMatchObject({ value: "2/3", note: "1 offline", tone: tones.red, target: "live" });
    expect(t[1]).toMatchObject({ value: "1", note: "of 3 cameras" });
    expect(t[2]).toMatchObject({ value: "5", note: "AI detections", target: "events" });
    expect(t[3]).toMatchObject({ value: "2", note: "1 new", tone: tones.red, target: "alarms" });
  });
  it("reads calm when all is well", () => {
    const t = statTiles(cameraCounts([cam("a", true)]), 0, []);
    expect(t[0]).toMatchObject({ note: "All online", tone: tones.green });
    expect(t[1].note).toBe("Every online camera");
    expect(t[3]).toMatchObject({ value: "0", note: "Nothing open", tone: tones.green });
    expect(statTiles(cameraCounts([]), 0, [])[0].note).toBe("None yet");
    expect(statTiles(cameraCounts([]), 0, [alarm("1", "Snoozed", "Info", 1)])[3]).toMatchObject({ note: "Being handled", tone: tones.amber });
  });
});

describe("events today", () => {
  const now = new Date(2026, 9, 21, 15, 0).getTime();
  const today = new Date(2026, 9, 21, 9, 0).getTime();
  const yesterday = new Date(2026, 9, 20, 23, 0).getTime();
  it("counts today's loaded events, or the facet total when larger", () => {
    const list = [ev("1", "a", today), ev("2", "a", yesterday), ev("3", "a", today + 1)];
    expect(eventsToday(list, now)).toBe(2);
    expect(eventsToday(list, now, 40)).toBe(40);
    expect(eventsToday(list, now, 0)).toBe(2);
    expect(eventsToday(list, now, null)).toBe(2);
  });
});

describe("needs attention", () => {
  it("lists new alarms, most severe then newest, three at most", () => {
    const list = [alarm("info", "New", "Info", 9), alarm("old-crit", "New", "Critical", 1), alarm("new-crit", "New", "Critical", 5), alarm("acked", "Acknowledged", "Critical", 10), alarm("warn", "New", "Warning", 3)];
    const r = needsAttention(list);
    expect(r.shown.map((a) => a.id)).toEqual(["new-crit", "old-crit", "warn"]);
    expect(r.more).toBe(1);
    expect(needsAttention([alarm("a", "Snoozed", "Critical", 1)])).toEqual({ shown: [], more: 0 });
  });
});

describe("activity and stills", () => {
  const list = [ev("1", "a", 10), ev("2", "b", 30), ev("3", "a", 20, false), ev("4", "a", 15)];
  it("shows the newest events first, capped", () => {
    expect(recentActivity(list, 2).map((e) => e.id)).toEqual(["2", "3"]);
  });
  it("merges a streamed event in place or on top, capped", () => {
    expect(mergeEvent(list, ev("5", "a", 40)).map((e) => e.id)).toEqual(["5", "2", "3", "4", "1"]);
    expect(mergeEvent(list, { ...ev("1", "a", 10), kind: "Vehicle" }).find((e) => e.id === "1")?.kind).toBe("Vehicle");
    expect(mergeEvent(list, ev("5", "a", 40), 2).map((e) => e.id)).toEqual(["5", "2"]);
  });
  it("uses the camera's newest event that has a frame", () => {
    expect(latestStill("a", list)?.id).toBe("4");
    expect(latestStill("z", list)).toBeNull();
  });
});

describe("camera tiles", () => {
  it("puts offline cameras first, marks REC on online recording ones, and caps the grid", () => {
    const cams = [cam("a", true, true, "Front"), cam("b", false, true), { ...cam("c", true, false), status: { online: true, width: 1920, height: 1080 } }];
    const r = cameraTiles(cams, [ev("1", "a", 5)], 2);
    expect(r.tiles.map((t) => [t.camera.id, t.online, t.rec, t.place, t.still?.id ?? null])).toEqual([
      ["b", false, false, "", null],
      ["a", true, true, "Front", "1"],
    ]);
    expect(r.more).toBe(1);
    expect(cameraTiles(cams, []).tiles[2].place).toBe("1920×1080");
  });
});

describe("link cards", () => {
  it("summarises the last seven days for Insights", () => {
    expect(insightsLine(null)).toBe("Trends and recommendations");
    expect(insightsLine({ alarms: 0, events: 0 })).toBe("Nothing in the last 7 days");
    expect(insightsLine({ alarms: 1, events: 1200 })).toBe("1 alarm · 1,200 events in 7 days");
  });
  it("counts people and names the one seen last", () => {
    expect(peopleLine(null)).toBe("Who was recognized, and when");
    expect(peopleLine([])).toBe("No one enrolled yet");
    expect(peopleLine([{ name: "Sam", lastSeenAt: 5 }, { name: "Ana", lastSeenAt: 9 }, { name: "Lee" }])).toBe("3 people · Ana seen last");
    expect(peopleLine([{ name: "Lee" }])).toBe("1 person");
  });
});
