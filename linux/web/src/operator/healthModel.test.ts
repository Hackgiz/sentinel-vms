import { beforeEach, describe, expect, it } from "vitest";
import type { ServerHealth } from "../shared/api";
import { activityKind, cameraRows, formatAge, formatBytes, formatUptime, levelOf, offlineTransitions, serverCard, streamNotice } from "./healthModel";
import { dedupeMs, emitNotice, resetNotices, subscribeNotices, type Notice } from "./notices";

const server = (over: Partial<ServerHealth> = {}): ServerHealth => ({
  id: "local",
  name: "Main",
  online: true,
  latencyMs: 4,
  cpu: 12.4,
  mem: { used: 4e9, total: 8e9 },
  disk: { used: 900e9, free: 100e9, total: 1000e9 },
  dbBytes: 5_000_000,
  uptimeSec: 90061,
  version: "1.2.3",
  cameras: [
    { id: "c1", name: "Door", online: true, recording: true, lastSegmentAgeSec: 20, kbps: 2048 },
    { id: "c2", name: "Back", online: false, recording: false, lastSegmentAgeSec: null, kbps: null },
  ],
  ...over,
});

describe("meter levels", () => {
  it("is amber from 80 percent and red from 95", () => {
    expect(levelOf(79.9)).toBe("ok");
    expect(levelOf(80)).toBe("warn");
    expect(levelOf(94.9)).toBe("warn");
    expect(levelOf(95)).toBe("crit");
    expect(levelOf(null)).toBe("off");
  });
});

describe("server cards", () => {
  it("maps readings to meters and turns amber when one is high", () => {
    const c = serverCard(server());
    expect(c.cpu).toMatchObject({ pct: 12.4, level: "ok", label: "12%" });
    expect(c.mem).toMatchObject({ pct: 50, level: "ok" });
    expect(c.disk).toMatchObject({ pct: 90, level: "warn", label: "90%" });
    expect(c.disk.detail).toBe("900 GB of 1.0 TB (100 GB free)");
    expect(c.state).toBe("warn");
    expect(c.uptime).toBe("1d 1h");
    expect(c.db).toBe("5.0 MB");
    expect(c).toMatchObject({ cameras: 2, camerasOnline: 1, latency: "4 ms" });
  });

  it("shows an offline server red with no metrics", () => {
    const c = serverCard(server({ online: false, cpu: null, mem: null, disk: null, dbBytes: null, uptimeSec: null, latencyMs: null }));
    expect(c.state).toBe("crit");
    expect(c.cpu).toMatchObject({ pct: null, label: "n/a", level: "off" });
    expect(c.uptime).toBe("");
    expect(c.disk.label).toBe("n/a");
  });
});

describe("camera rows", () => {
  it("flattens servers into one table and colors stale or offline cameras", () => {
    const other = server({ id: "rs2", name: "Annex", cameras: [{ id: "c3", name: "Gate", online: true, recording: true, lastSegmentAgeSec: 400, kbps: 800 }] });
    const rows = cameraRows([server(), other]);
    expect(rows.map((r) => `${r.server}/${r.name}`)).toEqual(["Annex/Gate", "Main/Back", "Main/Door"]);
    const by = Object.fromEntries(rows.map((r) => [r.name, r]));
    expect(by.Door).toMatchObject({ state: "ok", lastSegment: "20 s ago", kbps: "2.0 Mbps" });
    expect(by.Back).toMatchObject({ state: "crit", lastSegment: "none", kbps: "" });
    expect(by.Gate).toMatchObject({ state: "crit", lastSegmentLevel: "crit", kbps: "800 kbps" });
  });
});

describe("formatting", () => {
  it("reads naturally", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1_500_000)).toBe("1.5 MB");
    expect(formatUptime(59)).toBe("0m");
    expect(formatUptime(3700)).toBe("1h 1m");
    expect(formatAge(45)).toBe("45 s ago");
    expect(formatAge(600)).toBe("10 min ago");
    expect(formatAge(7200)).toBe("2 h ago");
  });
});

describe("offline transitions", () => {
  it("raises a notice only for something that was online and now is not", () => {
    const before = [server()];
    expect(offlineTransitions(null, before)).toEqual([]);
    const camDown = [server({ cameras: [{ id: "c1", name: "Door", online: false, recording: true, lastSegmentAgeSec: 20, kbps: null }, before[0].cameras[1]] })];
    expect(offlineTransitions(before, camDown).map((n) => n.key)).toEqual(["camera-offline:c1"]); // c2 was already offline
    const srvDown = [server({ online: false, cameras: camDown[0].cameras })];
    const n = offlineTransitions(before, srvDown);
    expect(n.map((x) => x.key)).toEqual(["server-offline:local"]);
    expect(n[0].level).toBe("error");
  });
});

describe("stream notices", () => {
  const cams = [{ id: "c1", name: "Door", status: { online: true, message: "Online" } }];
  const servers = [{ id: "rs2", name: "Annex" }];
  it("announces offline and return, ignoring non-transitions", () => {
    expect(streamNotice({ type: "camera.status", at: 0, data: { cameraId: "c1", online: false } }, cams, servers)).toMatchObject({ key: "camera-offline:c1", level: "warn" });
    expect(streamNotice({ type: "camera.status", at: 0, data: { cameraId: "c1", online: true } }, cams, servers)).toBeNull();
    expect(streamNotice({ type: "camera.status", at: 0, data: { cameraId: "zz", online: false } }, cams, servers)).toBeNull();
    expect(streamNotice({ type: "recorder.status", at: 0, data: { recorderId: "rs2", online: false } }, cams, servers)?.text).toBe("Recording server Annex went offline");
    expect(streamNotice({ type: "recorder.status", at: 0, data: { recorderId: "rs2", online: true } }, cams, servers)?.level).toBe("info");
  });
});

describe("notice bus", () => {
  beforeEach(resetNotices);
  it("delivers once per key within the dedupe window", () => {
    const got: Notice[] = [];
    const off = subscribeNotices((n) => got.push(n));
    const n = { key: "camera-offline:c1", level: "warn" as const, text: "x" };
    expect(emitNotice(n, 1000)).toBe(true);
    expect(emitNotice(n, 1000 + dedupeMs - 1)).toBe(false);
    expect(emitNotice(n, 1000 + dedupeMs)).toBe(true);
    expect(emitNotice(null)).toBe(false);
    off();
    expect(got).toHaveLength(2);
  });
});

describe("activity glyphs", () => {
  it("picks a glyph per event kind", () => {
    expect(activityKind("Person")).toBe("person");
    expect(activityKind("Vehicle")).toBe("vehicle");
    expect(activityKind("License plate")).toBe("vehicle");
    expect(activityKind("Animal")).toBe("animal");
    expect(activityKind("Motion")).toBe("motion");
    expect(activityKind("Line crossing")).toBe("other");
  });
});
