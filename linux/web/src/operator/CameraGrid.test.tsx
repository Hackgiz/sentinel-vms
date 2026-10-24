// The Operator's live tile still carries the PTZ overlay, now from shared/.
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { Camera } from "../shared/api";

const rights = vi.hoisted(() => ({ ptz: true }));
vi.mock("./session", () => ({
  useSigned: () => ({ session: { client: {}, route: {} }, can: (p: string) => p !== "controlPtz" || rights.ptz }),
}));
vi.mock("./data", () => ({ useData: () => ({ webrtcFor: () => false, subscribe: () => () => {} }) }));
vi.mock("../shared/VideoTile", () => ({ default: ({ children }: { children?: ReactNode }) => <div className="tile">{children}</div> }));
vi.mock("./TalkButton", () => ({ default: () => null }));
// The shared overlay, marked so the test sees which module the grid used and what it was told.
vi.mock("../shared/PTZOverlay", () => ({
  default: (p: { cameraId: string; online: boolean; allowed: boolean; keyboard: boolean }) => (
    <i data-shared-ptz={p.cameraId} data-online={String(p.online)} data-allowed={String(p.allowed)} data-keyboard={String(p.keyboard)} />
  ),
}));

const { LiveCell } = await import("./CameraGrid");

const camera = (online = true) => ({ id: "cam1", name: "Gate", status: { online } }) as unknown as Camera;

describe("Operator LiveCell PTZ", () => {
  it("renders the shared PTZ overlay on a focused or selected tile", () => {
    expect(renderToStaticMarkup(<LiveCell camera={camera()} index={0} focused />)).toContain('data-shared-ptz="cam1"');
    const sel = renderToStaticMarkup(<LiveCell camera={camera()} index={0} selected />);
    expect(sel).toContain('data-shared-ptz="cam1"');
    expect(sel).toContain('data-online="true"');
    expect(sel).toContain('data-allowed="true"');
    expect(sel).toContain('data-keyboard="true"');
  });
  it("leaves other tiles alone", () => {
    expect(renderToStaticMarkup(<LiveCell camera={camera()} index={0} />)).not.toContain("data-shared-ptz");
  });
  it("tells the overlay when the camera is offline or the user lacks controlPtz", () => {
    expect(renderToStaticMarkup(<LiveCell camera={camera(false)} index={0} focused />)).toContain('data-online="false"');
    rights.ptz = false;
    expect(renderToStaticMarkup(<LiveCell camera={camera()} index={0} focused />)).toContain('data-allowed="false"');
    rights.ptz = true;
  });
});
