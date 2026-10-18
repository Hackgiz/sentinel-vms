import { describe, expect, it } from "vitest";
import { AUDIO_DEFAULT_DB, audioFormOf, audioPatch, clampAudioDb } from "./audioSettings";

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
