import { describe, expect, it } from "vitest";
import { AUDIO_DEFAULT_DB, LOITER_MAX_S, audioFormOf, audioPatch, clampAudioDb, clampLoiter, loiterHint, loiterOf, loiterPatch } from "./audioSettings";

describe("audio settings", () => {
  it("clamps thresholds to the server's range", () => {
    expect(clampAudioDb(25)).toBe(25);
    expect(clampAudioDb(2)).toBe(6);
    expect(clampAudioDb(99)).toBe(60);
    expect(clampAudioDb(30.6)).toBe(31);
    expect(clampAudioDb(NaN)).toBe(AUDIO_DEFAULT_DB);
  });

  it("defaults for servers that do not send the fields", () => {
    expect(audioFormOf({})).toEqual({ audioDetect: false, audioThresholdDb: 25 });
    expect(audioFormOf(null)).toEqual({ audioDetect: false, audioThresholdDb: 25 });
  });

  it("patches only what changed", () => {
    const cam = { audioDetect: false, audioThresholdDb: 25 };
    expect(audioPatch(cam, { audioDetect: false, audioThresholdDb: 25 })).toEqual({});
    expect(audioPatch(cam, { audioDetect: true, audioThresholdDb: 25 })).toEqual({ audioDetect: true });
    expect(audioPatch(cam, { audioDetect: true, audioThresholdDb: 40 })).toEqual({ audioDetect: true, audioThresholdDb: 40 });
    expect(audioPatch({}, { audioDetect: false, audioThresholdDb: 25 })).toEqual({});
  });
});

describe("loitering after", () => {
  it("clamps to 0–3600, unreadable is off", () => {
    expect(clampLoiter(30)).toBe(30);
    expect(clampLoiter(-5)).toBe(0);
    expect(clampLoiter(NaN)).toBe(0);
    expect(clampLoiter(9000)).toBe(LOITER_MAX_S);
    expect(clampLoiter(12.4)).toBe(12);
  });

  it("defaults to off and patches only a change", () => {
    expect(loiterOf(null)).toBe(0);
    expect(loiterOf({})).toBe(0);
    expect(loiterPatch({ loiterSeconds: 60 }, 60)).toEqual({});
    expect(loiterPatch({}, 0)).toEqual({});
    expect(loiterPatch({ loiterSeconds: 60 }, 0)).toEqual({ loiterSeconds: 0 });
    expect(loiterPatch({}, 90)).toEqual({ loiterSeconds: 90 });
  });

  it("explains the setting", () => {
    expect(loiterHint(0)).toMatch(/^Off/);
    expect(loiterHint(90)).toContain("1 min 30 s");
    expect(loiterHint(45)).toContain("45 s");
    expect(loiterHint(120)).toContain("2 min or more");
  });
});
