import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Reconnector, retryDelayMs } from "./reconnect";

describe("retryDelayMs", () => {
  it("doubles from 1 s and caps at 30 s", () => {
    expect([0, 1, 2, 3, 4, 5, 6, 50].map(retryDelayMs)).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]);
  });
});

describe("Reconnector", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("retries with growing waits, one timer at a time", () => {
    const retry = vi.fn();
    const r = new Reconnector(retry);
    r.fail();
    r.fail(); // a second error before the timer fires does not stack
    vi.advanceTimersByTime(999);
    expect(retry).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(retry).toHaveBeenCalledTimes(1);
    r.fail();
    vi.advanceTimersByTime(1999);
    expect(retry).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1);
    expect(retry).toHaveBeenCalledTimes(2);
    r.fail();
    vi.advanceTimersByTime(4000);
    expect(retry).toHaveBeenCalledTimes(3);
  });

  it("starts again from 1 s after a success", () => {
    const retry = vi.fn();
    const r = new Reconnector(retry);
    r.fail();
    vi.advanceTimersByTime(1000);
    r.fail();
    vi.advanceTimersByTime(2000);
    expect(retry).toHaveBeenCalledTimes(2);
    r.ok();
    r.fail();
    vi.advanceTimersByTime(1000);
    expect(retry).toHaveBeenCalledTimes(3);
  });

  it("stops for good when told to (unmount or a new window)", () => {
    const retry = vi.fn();
    const r = new Reconnector(retry);
    r.fail();
    r.stop();
    vi.advanceTimersByTime(60_000);
    expect(retry).not.toHaveBeenCalled();
    r.fail();
    vi.advanceTimersByTime(60_000);
    expect(retry).not.toHaveBeenCalled();
  });
});
