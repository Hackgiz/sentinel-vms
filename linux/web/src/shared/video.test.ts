import { describe, expect, it } from "vitest";
import { ApiError, type VideoToken } from "./api";
import { VideoTokenSource, liveHlsUrl, swapToken, videoBase, whepUrl } from "./video";

const tok = (over: Partial<VideoToken> = {}): VideoToken => ({
  token: "v1.aaa.bbb",
  baseUrl: "https://rs2.lan:8090/v/v1.aaa.bbb",
  gatewayUrl: "/v/v1.aaa.bbb",
  expiresAt: 0,
  rights: ["live"],
  cameraId: "cam1",
  recorderId: "rs2",
  ...over,
});

describe("video URLs", () => {
  it("builds video-plane paths and swaps tokens in them", () => {
    expect(liveHlsUrl("/v/T", "cam 1")).toBe("/v/T/live/cam%201/index.m3u8");
    expect(whepUrl("https://rs/v/T", "c")).toBe("https://rs/v/T/webrtc/c/whep");
    expect(swapToken("https://rs/v/v1.old-x_y.sig/live/c/seg12.mp4", "v1.new.sig")).toBe("https://rs/v/v1.new.sig/live/c/seg12.mp4");
    expect(swapToken("/api/v2/cameras", "x")).toBe("/api/v2/cameras");
  });

  it("goes direct only when it is the same origin, unless told", () => {
    expect(videoBase(tok(), "auto", "https://ms.lan:8090")).toBe("https://ms.lan:8090/v/v1.aaa.bbb");
    expect(videoBase(tok({ baseUrl: "https://ms.lan:8090/v/v1.aaa.bbb" }), "auto", "https://ms.lan:8090")).toBe("https://ms.lan:8090/v/v1.aaa.bbb");
    expect(videoBase(tok(), "direct", "https://ms.lan:8090")).toBe("https://rs2.lan:8090/v/v1.aaa.bbb");
    expect(videoBase(tok({ baseUrl: "/v/v1.aaa.bbb" }), "auto", "")).toBe("/v/v1.aaa.bbb");
  });
});

describe("VideoTokenSource", () => {
  it("refreshes before expiry and backs off on errors, stopping on 403", async () => {
    let now = 1_000_000;
    const timers: number[] = [];
    let fire: (() => void) | null = null;
    const answers: (VideoToken | Error)[] = [tok({ expiresAt: now + 600_000 }), new ApiError(503, "unavailable", "down"), new ApiError(403, "forbidden", "no")];
    const got: string[] = [];
    const src = new VideoTokenSource({
      fetchToken: async () => {
        const a = answers.shift()!;
        if (a instanceof Error) throw a;
        return a;
      },
      onToken: (t) => got.push(t.token),
      onError: (e) => got.push((e as ApiError).code),
      now: () => now,
      setTimer: (fn, ms) => {
        timers.push(ms);
        fire = fn;
        return 1;
      },
      clearTimer: () => {},
    });
    src.start();
    await Promise.resolve();
    await Promise.resolve();
    expect(got).toEqual(["v1.aaa.bbb"]);
    expect(timers[0]).toBe(480_000); // 10 min life, refresh 20% early
    now += 480_000;
    fire!();
    await new Promise((r) => setTimeout(r, 0));
    expect(got).toEqual(["v1.aaa.bbb", "unavailable"]);
    expect(timers[1]).toBe(5000);
    fire!();
    await new Promise((r) => setTimeout(r, 0));
    expect(got).toEqual(["v1.aaa.bbb", "unavailable", "forbidden"]);
    expect(timers).toHaveLength(2); // no retry after 403
  });
});
