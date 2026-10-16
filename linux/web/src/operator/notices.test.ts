import { describe, expect, it, vi } from "vitest";
import { addNotice, autoDismisses, dismissNotice, emitNotice, maxNotices, recorderNotice, subscribeNotices, type Notice } from "./notices";

const n = (id: number, over: Partial<Notice> = {}): Notice => ({ id, at: 0, kind: "info", text: `n${id}`, ...over });

describe("notice list", () => {
  it("a keyed notice replaces its predecessor", () => {
    const l = addNotice([n(1, { key: "recorder:a", kind: "warn" })], n(2, { key: "recorder:a", kind: "success" }));
    expect(l.map((x) => x.id)).toEqual([2]);
  });
  it("is capped, newest kept", () => {
    let l: Notice[] = [];
    for (let i = 1; i <= maxNotices + 2; i++) l = addNotice(l, n(i));
    expect(l).toHaveLength(maxNotices);
    expect(l[l.length - 1].id).toBe(maxNotices + 2);
  });
  it("dismisses by id; only success/info fade by themselves", () => {
    expect(dismissNotice([n(1), n(2)], 1).map((x) => x.id)).toEqual([2]);
    expect(autoDismisses(n(1, { kind: "success" }))).toBe(true);
    expect(autoDismisses(n(1, { kind: "warn" }))).toBe(false);
  });
});

describe("recorder notices", () => {
  it("says nothing without a known earlier state or a change", () => {
    expect(recorderNotice("Main", undefined, false, "a")).toBeNull();
    expect(recorderNotice("Main", true, true, "a")).toBeNull();
  });
  it("offline warns, reconnect succeeds under the same key", () => {
    const off = recorderNotice("Main", true, false, "a")!;
    const on = recorderNotice("Main", false, true, "a")!;
    expect([off.kind, on.kind]).toEqual(["warn", "success"]);
    expect(off.key).toBe(on.key);
    expect(off.text).toContain("Main");
  });
});

describe("bus", () => {
  it("delivers to subscribers until they leave", () => {
    const fn = vi.fn();
    const off = subscribeNotices(fn);
    emitNotice({ kind: "info", text: "hi" });
    off();
    emitNotice({ kind: "info", text: "again" });
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn.mock.calls[0][0]).toMatchObject({ text: "hi", kind: "info" });
  });
});
