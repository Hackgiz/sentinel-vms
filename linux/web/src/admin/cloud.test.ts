import { describe, expect, it } from "vitest";
import { countdown, formatGiB, hasFeature, lastUpload, periodLabel, phase, planLabel, pollInterval, pushStateLabel, remoteStateLabel, remoteTone, statusLabel, statusTone, usage } from "./cloud";

describe("labels", () => {
  it("names the plans as sold", () => {
    expect(planLabel("cloud")).toBe("Sentinel Cloud");
    expect(planLabel("business")).toBe("Sentinel Cloud Business");
    expect(planLabel("free")).toBe("Free");
    expect(planLabel(undefined)).toBe("Free");
  });
  it("words the subscription status", () => {
    expect(statusLabel("active")).toBe("Active");
    expect(statusLabel("grace")).toBe("Payment due");
    expect(statusLabel("canceled")).toBe("Canceled");
    expect(statusLabel("inactive")).toBe("Not active");
    expect(statusTone("active")).toBe("green");
    expect(statusTone("grace")).toBe("amber");
    expect(statusTone("inactive")).toBe("");
  });
  it("covers every remote state", () => {
    expect(remoteStateLabel("connected")).toBe("Connected");
    expect(remoteStateLabel("retrying")).toBe("Reconnecting…");
    expect(remoteStateLabel("inactive")).toBe("Not active");
    expect(remoteStateLabel(undefined)).toBe("Off");
    expect(remoteTone("connected")).toBe("green");
    expect(remoteTone("connecting")).toBe("amber");
    expect(remoteTone("inactive")).toBe("red");
    expect(remoteTone("off")).toBe("");
  });
  it("never says tunnel", () => {
    for (const s of ["off", "connecting", "connected", "retrying", "inactive"] as const) expect(remoteStateLabel(s).toLowerCase()).not.toContain("tunnel");
    for (const s of ["off", "cloud", "direct"] as const) expect(pushStateLabel(s).toLowerCase()).not.toContain("tunnel");
  });
  it("describes push", () => {
    expect(pushStateLabel("cloud")).toBe("Through Sentinel Cloud");
    expect(pushStateLabel("direct")).toMatch(/own key/);
    expect(pushStateLabel("off")).toBe("Off");
  });
});

describe("formatGiB and usage", () => {
  it("uses binary units", () => {
    expect(formatGiB(10737418240)).toBe("10 GiB");
    expect(formatGiB(107374182400)).toBe("100 GiB");
    expect(formatGiB(1536 * 1024 * 1024)).toBe("1.5 GiB");
    expect(formatGiB(512)).toBe("512 B");
    expect(formatGiB(0)).toBe("0 B");
    expect(formatGiB(undefined)).toBe("—");
    expect(formatGiB(-1)).toBe("—");
  });
  it("clamps the bar and colours it near the quota", () => {
    expect(usage(0, 10737418240)).toEqual({ pct: 0, label: "0 B of 10 GiB", tone: "" });
    expect(usage(5368709120, 10737418240).pct).toBe(50);
    expect(usage(9 * 1024 ** 3, 10 * 1024 ** 3).tone).toBe("amber");
    expect(usage(10 * 1024 ** 3, 10 * 1024 ** 3).tone).toBe("red");
    expect(usage(12 * 1024 ** 3, 10 * 1024 ** 3).pct).toBe(100);
    expect(usage(1024, 0)).toEqual({ pct: 0, label: "1 KiB used", tone: "" });
  });
});

describe("link flow", () => {
  const now = Date.parse("2026-10-19T20:00:00Z");
  it("counts down to the code's expiry", () => {
    expect(countdown("2026-10-19T20:09:41Z", now)).toBe("9:41");
    expect(countdown("2026-10-19T20:00:05Z", now)).toBe("0:05");
    expect(countdown("2026-10-19T20:00:00Z", now)).toBe("");
    expect(countdown("2026-10-19T19:59:00Z", now)).toBe("");
    expect(countdown(undefined, now)).toBe("");
    expect(countdown("not a date", now)).toBe("");
  });
  it("tracks the phase", () => {
    const link = { userCode: "ABCD-EFGH", verifyURL: "https://sentvms.com/cloud?code=ABCD-EFGH", expiresAt: "2026-10-19T20:10:00Z" };
    expect(phase(null, false)).toBe("unlinked");
    expect(phase({ linked: false }, false)).toBe("unlinked");
    expect(phase({ linked: false, link }, false)).toBe("pending");
    expect(phase({ linked: true }, true)).toBe("linked");
    expect(phase({ linked: false }, true)).toBe("expired");
  });
  it("polls every 5 s while pending, 30 s while linked", () => {
    expect(pollInterval("pending")).toBe(5000);
    expect(pollInterval("linked")).toBe(30000);
    expect(pollInterval("unlinked")).toBeNull();
    expect(pollInterval("expired")).toBeNull();
  });
});

describe("plan details", () => {
  it("words the renewal line", () => {
    expect(periodLabel("active", "2026-11-19T00:00:00Z")).toMatch(/^Renews /);
    expect(periodLabel("grace", "2026-11-19T00:00:00Z")).toMatch(/overdue/);
    expect(periodLabel("canceled", "2026-11-19T00:00:00Z")).toMatch(/^Ended /);
    expect(periodLabel("active", null)).toBe("");
    expect(periodLabel("inactive", "2026-11-19T00:00:00Z")).toBe("");
  });
  it("shows the last upload", () => {
    expect(lastUpload(null)).toBe("Not yet");
    expect(lastUpload("2026-10-19T20:00:00Z")).not.toBe("Not yet");
  });
  it("reads features", () => {
    expect(hasFeature({ features: ["relay", "backup"] }, "backup")).toBe(true);
    expect(hasFeature({ features: [] }, "push")).toBe(false);
    expect(hasFeature(null, "relay")).toBe(false);
  });
});
