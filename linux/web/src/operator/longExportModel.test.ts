import { describe, expect, it } from "vitest";
import type { ExportJob } from "../shared/api";
import { blankDraft, clockMinutes, daysText, jobActive, jobText, lastRunText, longExportBlocker, scheduleBlocker, scheduleText, spanText, windowMinutes } from "./longExportModel";

const now = Date.UTC(2026, 9, 12, 12);
const H = 3600e3;

describe("longExportBlocker", () => {
  it("needs a camera and a sane range up to 12 hours", () => {
    expect(longExportBlocker("", now - H, now, now)).toMatch(/camera/);
    expect(longExportBlocker("a", 0, now, now)).toMatch(/start/);
    expect(longExportBlocker("a", now, now - 1, now)).toMatch(/after/);
    expect(longExportBlocker("a", now + H, now + 2 * H, now)).toMatch(/future/);
    expect(longExportBlocker("a", now - 12 * H - 1, now, now)).toMatch(/12 hours/);
    expect(longExportBlocker("a", now - 12 * H, now, now)).toBe("");
    expect(longExportBlocker("a", now - 31 * 60e3, now, now)).toBe(""); // beyond the 30-minute clip cap is fine here
  });
});

describe("text helpers", () => {
  it("spans", () => {
    expect(spanText(45 * 60e3)).toBe("45 min");
    expect(spanText(8 * H)).toBe("8 h");
    expect(spanText(5.5 * H)).toBe("5 h 30 min");
  });
  it("days", () => {
    expect(daysText([0, 1, 2, 3, 4, 5, 6])).toBe("Every day");
    expect(daysText([5, 4, 3, 2, 1])).toBe("Weekdays");
    expect(daysText([6, 0])).toBe("Weekends");
    expect(daysText([5, 1, 3, 3])).toBe("Mon, Wed, Fri");
  });
  it("jobs", () => {
    const j = { taskId: "t1", state: "running", progress: 40, cameraId: "c", fromMs: 0, toMs: 1, dest: "evidence" } as ExportJob;
    expect(jobActive(j)).toBe(true);
    expect(jobActive({ ...j, state: "done" })).toBe(false);
    expect(jobActive(null)).toBe(false);
    expect(jobText(j)).toBe("Exporting… 40%");
    expect(jobText({ ...j, state: "done", caseId: "EV-1" })).toBe("Filed as evidence EV-1.");
    expect(jobText({ ...j, state: "done", dest: "download" })).toBe("Ready to download.");
    expect(jobText({ ...j, state: "failed", error: "disk full" })).toBe("Failed: disk full");
    expect(jobText({ ...j, state: "canceled" })).toBe("Canceled.");
  });
});

describe("schedule windows", () => {
  it("parses clock values", () => {
    expect(clockMinutes("22:30")).toBe(1350);
    expect(clockMinutes("24:00")).toBeNull();
    expect(clockMinutes("24:00", true)).toBe(1440);
    expect(clockMinutes("6:00")).toBeNull();
    expect(clockMinutes("12:60")).toBeNull();
  });
  it("measures overnight windows", () => {
    expect(windowMinutes("22:00", "06:00")).toBe(8 * 60);
    expect(windowMinutes("08:00", "17:30")).toBe(9.5 * 60);
    expect(windowMinutes("12:00", "24:00")).toBe(12 * 60);
    expect(windowMinutes("09:00", "09:00")).toBeNull();
    expect(windowMinutes("nope", "09:00")).toBeNull();
  });
  it("describes them", () => {
    expect(scheduleText({ days: [5, 1], start: "22:00", end: "06:00" })).toBe("Mon, Fri · 22:00 → 06:00 (next day)");
    expect(scheduleText({ days: [1, 2, 3, 4, 5], start: "08:00", end: "18:00" })).toBe("Weekdays · 08:00 → 18:00");
  });
  it("blocks bad drafts", () => {
    const ok = { ...blankDraft("cam"), name: "Nightly" };
    expect(scheduleBlocker(ok)).toBe("");
    expect(scheduleBlocker({ ...ok, name: " " })).toMatch(/name/);
    expect(scheduleBlocker({ ...ok, cameraId: "" })).toMatch(/camera/);
    expect(scheduleBlocker({ ...ok, days: [] })).toMatch(/day/);
    expect(scheduleBlocker({ ...ok, end: ok.start })).toMatch(/differ/);
    expect(scheduleBlocker({ ...ok, start: "06:00", end: "19:00" })).toMatch(/12 hours/);
    expect(scheduleBlocker({ ...ok, start: "08:00", end: "20:00" })).toBe("");
  });
  it("reports the last run", () => {
    const fmt = () => "WHEN";
    expect(lastRunText({ lastRunAt: 0, lastStatus: "" }, fmt)).toBe("Never");
    expect(lastRunText({ lastRunAt: 5, lastStatus: "ok" }, fmt)).toBe("OK — WHEN");
    expect(lastRunText({ lastRunAt: 5, lastStatus: "running" }, fmt)).toBe("Running — WHEN");
    expect(lastRunText({ lastRunAt: 5, lastStatus: "failed: nothing was recorded in the window" }, fmt)).toBe("Failed — nothing was recorded in the window");
  });
});
