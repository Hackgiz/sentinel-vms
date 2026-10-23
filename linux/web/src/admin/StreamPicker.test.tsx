// WAVE22 addendum: the Find streams picker. Rendered statically; fetch is
// mocked for the client calls, nothing leaves the process.
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, type FoundStream, type StreamDiscovery } from "../shared/api";
import { v2 } from "./client";
import { recordingDefaults, recordingFormOf } from "../shared/recordingModes";
import RecordingFields from "./RecordingFields";
import { StreamPickerView, candidateStreams, findError, type FindState } from "./StreamPicker";

const stream = (over: Partial<FoundStream>): FoundStream => ({
  url: "rtsp://cam/s1",
  label: "1920×1080 · H.264 · 25 fps",
  width: 1920,
  height: 1080,
  fps: 25,
  codec: "H264",
  source: "onvif",
  role: "",
  ...over,
});

const result = (over: Partial<StreamDiscovery> = {}): StreamDiscovery => ({
  streams: [
    stream({ role: "main", encoderToken: "v1" }),
    stream({ url: "rtsp://cam/s2", label: "1280×720 · H.264 · 15 fps", width: 1280, height: 720, encoderToken: "v2" }),
    stream({ url: "rtsp://cam/s3", label: "H.264", width: 0, height: 0, fps: 0, source: "guess" }),
  ],
  recommendedSub: "rtsp://cam/s2",
  tooSmallSub: null,
  ...over,
});

const noop = () => {};
const render = (state: FindState, current = "") =>
  renderToStaticMarkup(<StreamPickerView state={state} mainUrl="rtsp://cam/s1" current={current} onUse={noop} onResize={noop} onRetry={noop} onClose={noop} />);

describe("stream picker", () => {
  it("renders nothing until asked", () => {
    expect(render({ kind: "idle" })).toBe("");
    expect(render({ kind: "loading" })).toContain("Looking for this camera");
  });

  it("lists the other streams with the recommended tag and Use recommended", () => {
    const html = render({ kind: "done", result: result() });
    expect(html).not.toContain(">rtsp://cam/s1<"); // the main stream isn't offered
    expect(html).toMatch(/1280×720 · H\.264 · 15 fps<\/strong><span class="badge green">Recommended/);
    expect(html).toContain("Use recommended");
    expect(html).not.toContain("Make it 1280×720");
  });

  it("badges guessed streams", () => {
    const html = render({ kind: "done", result: result() });
    expect(html).toMatch(/H\.264<\/strong><span class="badge amber">Guessed<\/span><span class="mono">rtsp:\/\/cam\/s3/);
    expect(html.match(/Guessed/g)).toHaveLength(1);
  });

  it("marks the stream in use and hides Use recommended when it is already chosen", () => {
    const html = render({ kind: "done", result: result() }, "rtsp://cam/s2");
    expect(html).toContain("In use");
    expect(html).not.toContain("Use recommended");
  });

  it("offers to resize a too-small second stream", () => {
    const small = result({
      streams: [stream({ role: "main" }), stream({ url: "rtsp://cam/s2", label: "352×288 · H.264", width: 352, height: 288, encoderToken: "v2" })],
      tooSmallSub: { url: "rtsp://cam/s2", encoderToken: "v2", width: 352, height: 288 },
    });
    const html = render({ kind: "done", result: small });
    expect(html).toContain("This camera&#x27;s second stream is only 352×288.");
    expect(html).toContain("Make it 1280×720");
    expect(render({ kind: "resizing", result: small })).toContain("Changing…");
  });

  it("shows errors inline with Try again, keeping what was found", () => {
    const html = render({ kind: "error", message: "The camera rejected the username or password.", authFailed: true });
    expect(html).toMatch(/role="alert">The camera rejected the username or password\. <button[^>]*>Try again/);
    const kept = render({ kind: "error", message: "the camera refused", authFailed: false, result: result() });
    expect(kept).toContain("the camera refused");
    expect(kept).toContain("Recommended");
  });

  it("says when nothing was found", () => {
    expect(render({ kind: "done", result: { streams: [stream({ role: "main" })], recommendedSub: null, tooSmallSub: null } })).toContain("No second stream found");
  });

  it("filters candidates and maps camera errors", () => {
    expect(candidateStreams(result(), "rtsp://cam/s1").map((s) => s.url)).toEqual(["rtsp://cam/s2", "rtsp://cam/s3"]);
    expect(findError(new ApiError(422, "camera_auth", "x")).authFailed).toBe(true);
    expect(findError(new ApiError(422, "camera_unreachable", "couldn't reach the camera"))).toEqual({ authFailed: false, message: "couldn't reach the camera" });
  });
});

describe("dual option hint", () => {
  const fields = (onFindStreams?: () => void) =>
    renderToStaticMarkup(
      <RecordingFields form={recordingFormOf(null)} onChange={noop} hasSub={false} motionLevel={2} analytics={false} server={recordingDefaults} onFindStreams={onFindStreams} />,
    );
  it("links to Find streams for a saved camera", () => {
    expect(fields(noop)).toMatch(/Needs a second stream\. <button type="button" class="linkish">Find streams<\/button> to add one\./);
    expect(fields()).toContain("Needs a second stream. Add one above to use this.");
    expect(fields()).not.toContain("linkish");
  });
});

describe("client calls", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("posts discover and resize to the camera's streams routes", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify(result()), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", f);
    await v2.cameras.discoverStreams("c1", { password: "typed" });
    await v2.cameras.resizeStream("c1", { encoderToken: "v2", width: 1280, height: 720 });
    const calls = f.mock.calls as unknown as [string, RequestInit][];
    expect(calls[0][0]).toBe("/api/v2/cameras/c1/streams/discover");
    expect(JSON.parse(String(calls[0][1].body))).toEqual({ password: "typed" });
    expect(calls[1][0]).toBe("/api/v2/cameras/c1/streams/resize");
    expect(JSON.parse(String(calls[1][1].body))).toEqual({ encoderToken: "v2", width: 1280, height: 720 });
  });

  it("surfaces a 422 as camera_auth", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ error: { code: "camera_auth", message: "the camera rejected the username or password" }, authFailed: true }), { status: 422 })),
    );
    const err = await v2.cameras.discoverStreams("c1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(findError(err).authFailed).toBe(true);
  });
});
