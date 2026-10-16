import { describe, expect, it } from "vitest";
import type { Alarm } from "../shared/api";
import { activeCount, bulkTargets, emptyAlarmFilter, nextSort, sortAlarms, toAlarmQuery, toCountsQuery, toggleSelection, windowOf } from "./alarmModel";

const now = Date.UTC(2026, 9, 15, 12);
const alarm = (id: string, over: Partial<Alarm> = {}): Alarm => ({
  id,
  kind: "Person",
  title: "Person detected",
  detail: "",
  severity: "Warning",
  state: "New",
  owner: "Unassigned",
  createdAt: 1000,
  updatedAt: 1000,
  lastEventAt: 1000,
  eventCount: 1,
  responseLog: [],
  ...over,
});

describe("alarm filter", () => {
  it("asks for the open states by default and leaves unset filters out", () => {
    expect(toAlarmQuery(emptyAlarmFilter, now)).toEqual({
      states: "New,Acknowledged,Investigating,Snoozed",
      state: undefined,
      severities: undefined,
      cameras: undefined,
      owner: undefined,
      from: undefined,
      to: undefined,
      q: undefined,
    });
  });

  it("no chips selected means every state", () => {
    expect(toAlarmQuery({ ...emptyAlarmFilter, states: [] }, now)).toMatchObject({ states: undefined, state: "all" });
  });

  it("builds list filters, the time window and trimmed text", () => {
    const f = { ...emptyAlarmFilter, states: ["New" as const], severities: ["Critical", "Info"], cameras: ["a", "b"], owner: "unassigned", range: "24h" as const, q: " gate " };
    expect(toAlarmQuery(f, now)).toMatchObject({ states: "New", severities: "Critical,Info", cameras: "a,b", owner: "unassigned", from: now - 24 * 3600e3, q: "gate" });
    expect(windowOf({ ...f, range: "custom", from: 5, to: 9 }, now)).toEqual({ from: 5, to: 9 });
    expect(activeCount(f)).toBe(2 + 2 + 1 + 1 + 1);
  });

  it("counts ignore the state chips", () => {
    expect(toCountsQuery({ ...emptyAlarmFilter, states: ["Resolved"], q: "x" }, now)).not.toHaveProperty("states");
  });
});

describe("sorting and selection", () => {
  const list = [alarm("a", { severity: "Info", createdAt: 3 }), alarm("b", { severity: "Critical", createdAt: 1 }), alarm("c", { severity: "Critical", createdAt: 2 })];

  it("sorts by severity with newest first among ties, and flips", () => {
    expect(sortAlarms(list, { key: "severity", dir: "asc" }).map((a) => a.id)).toEqual(["c", "b", "a"]);
    expect(sortAlarms(list, { key: "severity", dir: "desc" }).map((a) => a.id)).toEqual(["a", "c", "b"]);
    expect(sortAlarms(list, { key: "time", dir: "desc" }).map((a) => a.id)).toEqual(["a", "c", "b"]);
  });

  it("header clicks flip the same key and pick a sensible start for a new one", () => {
    expect(nextSort({ key: "time", dir: "desc" }, "time")).toEqual({ key: "time", dir: "asc" });
    expect(nextSort({ key: "time", dir: "desc" }, "camera")).toEqual({ key: "camera", dir: "asc" });
    expect(nextSort({ key: "camera", dir: "asc" }, "events")).toEqual({ key: "events", dir: "desc" });
  });

  it("bulk targets skip alarms the action does not apply to", () => {
    const l = [alarm("n"), alarm("ack", { state: "Acknowledged" }), alarm("snz", { state: "Snoozed" }), alarm("done", { state: "Resolved" })];
    const all = new Set(l.map((a) => a.id));
    expect(bulkTargets("acknowledge", l, all)).toEqual({ ids: ["n", "snz"], state: "Acknowledged" });
    expect(bulkTargets("close", l, all)).toEqual({ ids: ["n", "ack", "snz"], state: "Resolved" });
    expect(bulkTargets("close", l, new Set(["done"])).ids).toEqual([]);
  });

  it("shift-click selects the range in table order", () => {
    const order = ["a", "b", "c", "d"];
    expect([...toggleSelection(new Set(["a"]), order, "c", "a", true)].sort()).toEqual(["a", "b", "c"]);
    expect([...toggleSelection(new Set(["a", "b", "c"]), order, "c", "a", true)]).toEqual([]);
    expect([...toggleSelection(new Set(), order, "b", null, false)]).toEqual(["b"]);
  });
});
