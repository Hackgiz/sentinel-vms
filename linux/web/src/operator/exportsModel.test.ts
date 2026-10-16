import { describe, expect, it } from "vitest";
import type { EvidenceCase } from "../shared/api";
import { batchLabel, composeNote, exportBlocker, formatBytes, fromLocalInput, rangeText, toLocalInput } from "./exportsModel";

const now = Date.UTC(2026, 9, 12, 12);

describe("exportBlocker", () => {
  it("needs cameras and a sane range", () => {
    expect(exportBlocker([], now - 60e3, now, now)).toMatch(/camera/);
    expect(exportBlocker(["a"], 0, now, now)).toMatch(/start/);
    expect(exportBlocker(["a"], now, now - 1, now)).toMatch(/after/);
    expect(exportBlocker(["a"], now + 60e3, now + 120e3, now)).toMatch(/future/);
    expect(exportBlocker(["a"], now - 31 * 60e3, now, now)).toMatch(/30 minutes/);
    expect(exportBlocker(["a", "b"], now - 30 * 60e3, now, now)).toBe("");
  });
});

describe("batch labels and notes", () => {
  it("labels only multi-camera selections", () => {
    expect(batchLabel(1, now)).toBe("");
    expect(batchLabel(3, now)).toMatch(/^Batch \d{4}-\d\d-\d\d \d\d:\d\d \(3 cameras\)$/);
  });
  it("composes label and note", () => {
    expect(composeNote("", "  ")).toBe("");
    expect(composeNote("Batch X", "")).toBe("Batch X");
    expect(composeNote("Batch X", "break-in")).toBe("Batch X · break-in");
    expect(composeNote("", "break-in")).toBe("break-in");
  });
});

describe("formatting", () => {
  it("sizes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatBytes(250 * 1024 * 1024)).toBe("250 MB");
  });
  it("round-trips local input values to the minute", () => {
    const t = new Date(2026, 9, 12, 8, 30).getTime();
    expect(fromLocalInput(toLocalInput(t))).toBe(t);
    expect(toLocalInput(0)).toBe("");
    expect(fromLocalInput("")).toBe(0);
  });
  it("shows the server label when the range is unknown", () => {
    const c = { fromMs: 0, toMs: 0, range: "Oct 9 09:00:00 – 09:01:00" } as EvidenceCase;
    expect(rangeText(c)).toBe("Oct 9 09:00:00 – 09:01:00");
    expect(rangeText({ ...c, fromMs: now - 60e3, toMs: now }, () => "FROM")).toMatch(/^FROM – /);
  });
});
