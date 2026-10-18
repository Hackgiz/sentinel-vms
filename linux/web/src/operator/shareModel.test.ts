import { describe, expect, it } from "vitest";
import type { ShareLink } from "../shared/api";
import { maxShareMs, shareBlocker, shareBody, sortShares, usesText } from "./shareModel";

const now = Date.UTC(2026, 9, 16, 12);

describe("shareBlocker", () => {
  it("accepts presets and blank or valid max uses", () => {
    expect(shareBlocker("24h", 0, "", now)).toBe("");
    expect(shareBlocker("7d", 0, "5", now)).toBe("");
    expect(shareBlocker("1h", 0, "0", now)).toBe("");
  });
  it("validates a custom expiry", () => {
    expect(shareBlocker("custom", 0, "", now)).toMatch(/Pick/);
    expect(shareBlocker("custom", now - 1, "", now)).toMatch(/future/);
    expect(shareBlocker("custom", now + maxShareMs + 1, "", now)).toMatch(/30 days/);
    expect(shareBlocker("custom", now + 3600e3, "", now)).toBe("");
  });
  it("validates max uses", () => {
    for (const bad of ["-1", "1.5", "abc", "1001"]) expect(shareBlocker("1h", 0, bad, now)).toMatch(/Max uses/);
  });
});

describe("shareBody", () => {
  it("builds the request", () => {
    expect(shareBody("24h", 0, "", "  ")).toEqual({ expiry: "24h", maxUses: 0 });
    expect(shareBody("custom", 123, "3", " for police ")).toEqual({ expiry: "custom", expiresAt: 123, maxUses: 3, note: "for police" });
  });
});

describe("list helpers", () => {
  const l = (id: string, status: ShareLink["status"], createdAt: number) => ({ id, status, createdAt }) as ShareLink;
  it("formats uses", () => {
    expect(usesText({ uses: 2, maxUses: 0 })).toBe("2");
    expect(usesText({ uses: 2, maxUses: 5 })).toBe("2 of 5");
  });
  it("puts active links first, newest first", () => {
    const out = sortShares([l("old-dead", "revoked", 9), l("old", "active", 1), l("new", "active", 5)]);
    expect(out.map((x) => x.id)).toEqual(["new", "old", "old-dead"]);
  });
});
