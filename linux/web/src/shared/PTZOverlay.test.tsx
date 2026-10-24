// The shared PTZ overlay without a browser: what it renders for each camera
// and user, and (running its effects by hand) that keys move the camera and
// a stop follows a key release, a window blur and an unmount.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ApiClient, PTZState } from "./api";

// Collect the overlay's effects instead of dropping them (server rendering
// never runs effects), so the test can mount and unmount it by hand.
const effects = vi.hoisted(() => [] as Array<() => void | (() => void)>);
vi.mock("react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react")>();
  return { ...actual, useEffect: (fn: () => void | (() => void)) => void effects.push(fn) };
});

const { default: PTZOverlay } = await import("./PTZOverlay");

const supported: PTZState = { supported: true, presets: [{ token: "p1", name: "Gate" }] };

function fakeClient() {
  const calls: string[] = [];
  const client = {
    ptz: {
      state: () => new Promise<PTZState>(() => {}), // the refresh never lands; `initial` is what shows
      move: async (_id: string, pan: number, tilt: number, zoom: number) => void calls.push(`move ${pan} ${tilt} ${zoom}`),
      stop: async () => void calls.push("stop"),
    },
  } as unknown as ApiClient;
  return { client, calls };
}

type Listener = (e: unknown) => void;
function fakeTarget() {
  const listeners = new Map<string, Set<Listener>>();
  return {
    listeners,
    addEventListener: (t: string, fn: Listener) => void (listeners.get(t) ?? listeners.set(t, new Set()).get(t)!).add(fn),
    removeEventListener: (t: string, fn: Listener) => void listeners.get(t)?.delete(fn),
    fire: (t: string, e: object = {}) => listeners.get(t)?.forEach((fn) => fn(e)),
  };
}

const html = (props: Partial<Parameters<typeof PTZOverlay>[0]> = {}) => {
  effects.length = 0;
  return renderToStaticMarkup(<PTZOverlay client={fakeClient().client} cameraId="c1" keyboard initial={supported} {...props} />);
};

describe("PTZOverlay visibility", () => {
  it("shows the pad and presets for a supported, online camera and a user with controlPtz", () => {
    const out = html();
    expect(out).toContain('aria-label="Pan, tilt and zoom"');
    expect(out).toContain("Presets");
    expect(out).toContain('aria-label="Zoom in (+)"');
  });
  it("renders nothing for an unsupported camera", () => {
    expect(html({ initial: { supported: false, presets: [] } })).toBe("");
    expect(html({ initial: undefined })).toBe(""); // not probed yet
  });
  it("renders nothing for an offline camera", () => {
    expect(html({ online: false })).toBe("");
  });
  it("renders nothing without the controlPtz right", () => {
    expect(html({ allowed: false })).toBe("");
  });
  it("takes the host's class (the dashboard's placement)", () => {
    expect(html({ className: "dash-ptz" })).toContain('class="op-ptz dash-ptz"');
  });
});

describe("PTZOverlay stop on release", () => {
  let win: ReturnType<typeof fakeTarget>;
  let doc: ReturnType<typeof fakeTarget> & { visibilityState: string };
  beforeEach(() => {
    win = fakeTarget();
    doc = Object.assign(fakeTarget(), { visibilityState: "visible" });
    vi.stubGlobal("window", win);
    vi.stubGlobal("document", doc);
  });
  afterEach(() => vi.unstubAllGlobals());

  function mount(props: Partial<Parameters<typeof PTZOverlay>[0]> = {}) {
    const { client, calls } = fakeClient();
    effects.length = 0;
    renderToStaticMarkup(<PTZOverlay client={client} cameraId="c1" keyboard initial={supported} {...props} />);
    const cleanups = effects.map((fn) => fn()).filter((c): c is () => void => typeof c === "function");
    const unmount = () => cleanups.forEach((c) => c());
    const key = (type: "keydown" | "keyup", k: string) => win.fire(type, { key: k, repeat: false, ctrlKey: false, metaKey: false, altKey: false, target: null, preventDefault: () => {} });
    const settle = () => new Promise((r) => setTimeout(r, 0));
    return { calls, unmount, key, settle };
  }

  it("moves while an arrow is held and stops when it is released", async () => {
    const m = mount();
    m.key("keydown", "ArrowLeft");
    m.key("keyup", "ArrowLeft");
    await m.settle();
    expect(m.calls).toEqual(["move -0.5 0 0", "stop"]);
    m.unmount();
  });

  it("stops when unmounted mid-move", async () => {
    const m = mount();
    m.key("keydown", "+");
    m.unmount();
    await m.settle();
    expect(m.calls).toEqual(["move 0 0 0.4", "stop"]);
    // ...and the listeners are gone: a late key does nothing.
    m.key("keydown", "ArrowUp");
    await m.settle();
    expect(m.calls).toHaveLength(2);
  });

  it("stops when the window loses focus or the page is hidden", async () => {
    const m = mount();
    m.key("keydown", "ArrowRight");
    win.fire("blur");
    m.key("keydown", "ArrowDown");
    doc.visibilityState = "hidden";
    doc.fire("visibilitychange");
    await m.settle();
    expect(m.calls).toEqual(["move 0.5 0 0", "stop", "move 0 -0.5 0", "stop"]);
    m.unmount();
  });

  it("listens to nothing when hidden (offline camera, no right)", () => {
    mount({ online: false });
    expect(win.listeners.get("keydown")?.size ?? 0).toBe(0);
    mount({ allowed: false });
    expect(win.listeners.get("keydown")?.size ?? 0).toBe(0);
  });
});
