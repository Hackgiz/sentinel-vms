import { describe, expect, it } from "vitest";
import { bounded, changeOf, formOfStream, problemWith, reconnects, resolutionChoices, streamTitle, useSlider, type Stream } from "./streamSettings";

const stream = (over: Partial<Stream> = {}): Stream => ({
  configToken: "vmain",
  profileToken: "main",
  name: "Main",
  inUse: "main",
  encoding: "H264",
  current: { width: 1920, height: 1080, fps: 25, bitrate: 4096, govLength: 50, quality: 5 },
  options: {
    resolutions: [
      { width: 1920, height: 1080 },
      { width: 1280, height: 720 },
    ],
    fps: { min: 1, max: 30 },
    bitrate: { min: 0, max: 0 },
    gov: { min: 1, max: 120 },
    quality: { min: 1, max: 10 },
  },
  ...over,
});

describe("stream settings form", () => {
  it("sends only what changed, and nothing when unchanged", () => {
    const s = stream();
    const f = formOfStream(s);
    expect(changeOf(s, f)).toBeNull();
    expect(changeOf(s, { ...f, fps: 15 })).toEqual({ fps: 15 });
    expect(changeOf(s, { ...f, resolution: "1280x720", bitrate: 2000 })).toEqual({ width: 1280, height: 720, bitrate: 2000 });
  });

  it("warns about a reconnect only for size or frame rate changes", () => {
    expect(reconnects(null)).toBe(false);
    expect(reconnects({ bitrate: 1000 })).toBe(false);
    expect(reconnects({ quality: 3 })).toBe(false);
    expect(reconnects({ fps: 10 })).toBe(true);
    expect(reconnects({ width: 640, height: 360 })).toBe(true);
  });

  it("validates changed fields against the camera's ranges", () => {
    const s = stream();
    const f = formOfStream(s);
    expect(problemWith(s, f)).toBe("");
    expect(problemWith(s, { ...f, fps: 31 })).toMatch(/Frame rate must be a whole number from 1 to 30/);
    expect(problemWith(s, { ...f, quality: 11 })).toMatch(/Quality/);
    expect(problemWith(s, { ...f, bitrate: 99999999 })).toBe(""); // unbounded range: the camera decides
    expect(problemWith(s, { ...f, bitrate: -5 })).toMatch(/positive/);
  });

  it("treats a max of 0 as unbounded and picks sliders only for tight ranges", () => {
    expect(bounded({ min: 0, max: 0 })).toBe(false);
    expect(useSlider({ min: 1, max: 30 })).toBe(true);
    expect(useSlider({ min: 64, max: 16384 })).toBe(false);
    expect(useSlider({ min: 0, max: 0 })).toBe(false);
  });

  it("always offers the current resolution and titles streams by role", () => {
    const odd = stream({ current: { width: 800, height: 600, fps: 10, bitrate: 0, govLength: 0, quality: 1 } });
    expect(resolutionChoices(odd)[0]).toEqual({ key: "800x600", width: 800, height: 600 });
    expect(resolutionChoices(stream())).toHaveLength(2);
    expect(streamTitle(stream())).toBe("Main stream");
    expect(streamTitle(stream({ inUse: "sub" }))).toBe("Sub stream");
    expect(streamTitle(stream({ inUse: "", name: "Profile_3" }))).toBe("Profile_3");
  });
});
