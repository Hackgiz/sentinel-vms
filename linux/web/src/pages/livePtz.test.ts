import { describe, expect, it } from "vitest";
import { tilePtz } from "./livePtz";

const yes = { supported: true, presets: [] };
const no = { supported: false, presets: [] };

describe("tilePtz (dashboard Live page)", () => {
  it("badges a supported tile in the grid and opens the controls when the tile is open", () => {
    expect(tilePtz({ state: yes, online: true, canControl: true, open: false })).toEqual({ badge: true, controls: false });
    expect(tilePtz({ state: yes, online: true, canControl: true, open: true })).toEqual({ badge: false, controls: true });
  });
  it("shows nothing for unsupported or unprobed cameras", () => {
    for (const state of [no, null]) {
      expect(tilePtz({ state, online: true, canControl: true, open: true })).toEqual({ badge: false, controls: false });
      expect(tilePtz({ state, online: true, canControl: true, open: false })).toEqual({ badge: false, controls: false });
    }
  });
  it("shows nothing for offline cameras", () => {
    expect(tilePtz({ state: yes, online: false, canControl: true, open: true })).toEqual({ badge: false, controls: false });
    expect(tilePtz({ state: yes, online: false, canControl: true, open: false })).toEqual({ badge: false, controls: false });
  });
  it("shows nothing to users without controlPtz", () => {
    expect(tilePtz({ state: yes, online: true, canControl: false, open: true })).toEqual({ badge: false, controls: false });
    expect(tilePtz({ state: yes, online: true, canControl: false, open: false })).toEqual({ badge: false, controls: false });
  });
});
