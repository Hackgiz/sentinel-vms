import { describe, expect, it } from "vitest";
import type { ApiClient, VideoToken } from "../shared/api";
import { exportFileName, exportWindow, parsePlaylist } from "./export";

const playlist = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="/v/T1/recordings/cam1/a.mp4",BYTERANGE="1200@0"
#EXT-X-PROGRAM-DATE-TIME:2026-10-11T10:00:00.000Z
#EXTINF:2.000,
/v/T1/api/v1/cameras/cam1/pieces/a.mp4?f=0&l=1
#EXTINF:1.500,
/v/T1/api/v1/cameras/cam1/pieces/a.mp4?f=2&l=3
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="/v/T1/recordings/cam1/b.mp4",BYTERANGE="900@0"
#EXT-X-PROGRAM-DATE-TIME:2026-10-11T10:05:00.000Z
#EXTINF:3.000,
/v/T1/api/v1/cameras/cam1/pieces/b.mp4?f=0&l=4
#EXT-X-ENDLIST
`;

describe("parsePlaylist", () => {
  it("splits runs at each init section and resolves URIs", () => {
    const runs = parsePlaylist(playlist, "https://rs1.lan:8090/v/T1/api/v1/cameras/cam1/playback.m3u8?from=1&to=2");
    expect(runs).toHaveLength(2);
    expect(runs[0]).toEqual({
      initUrl: "https://rs1.lan:8090/v/T1/recordings/cam1/a.mp4",
      initBytes: 1200,
      pieces: ["https://rs1.lan:8090/v/T1/api/v1/cameras/cam1/pieces/a.mp4?f=0&l=1", "https://rs1.lan:8090/v/T1/api/v1/cameras/cam1/pieces/a.mp4?f=2&l=3"],
      startsAt: Date.parse("2026-10-11T10:00:00Z"),
      duration: 3.5,
    });
    expect(runs[1].initBytes).toBe(900);
    expect(runs[1].startsAt).toBe(Date.parse("2026-10-11T10:05:00Z"));
  });

  it("refuses playlists it can't turn into files", () => {
    expect(() => parsePlaylist("#EXTM3U\n#EXTINF:1,\nx.mp4\n", "https://h/p.m3u8")).toThrow(/before its init/);
    expect(() => parsePlaylist('#EXT-X-MAP:URI="i.mp4",BYTERANGE="10@5"\n', "https://h/p.m3u8")).toThrow(/doesn't start/);
    expect(parsePlaylist("#EXTM3U\n#EXT-X-ENDLIST\n", "https://h/p.m3u8")).toEqual([]);
  });
});

describe("exportFileName", () => {
  it("is safe for every file system", () => {
    const t = new Date(2026, 9, 11, 14, 3, 20).getTime();
    expect(exportFileName("Front/Door: east", t)).toBe("Front Door east 2026-10-11 14-03-20.mp4");
    expect(exportFileName("", t, 2)).toBe("camera 2026-10-11 14-03-20 (2).mp4");
  });
});

describe("exportWindow", () => {
  it("asks each recording server for an export token and assembles init + pieces", async () => {
    const tokens: [string, string[], string | undefined][] = [];
    const client = {
      baseUrl: "https://ms.lan:8090",
      cameras: {
        videoToken: async (cameraId: string, rights: string[], recorderId?: string): Promise<VideoToken> => {
          tokens.push([cameraId, rights, recorderId]);
          return { token: "T1", baseUrl: `https://${recorderId}.lan:8090/v/T1`, gatewayUrl: "/v/T1", expiresAt: 0, rights: ["export"], cameraId, recorderId: recorderId ?? "" };
        },
      },
    } as unknown as ApiClient;
    const requests: { url: string; range?: string }[] = [];
    const fakeFetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const range = (init?.headers as Record<string, string> | undefined)?.Range;
      requests.push({ url, range });
      if (url.includes("playback.m3u8")) {
        const host = new URL(url).host;
        const body = host.startsWith("rs1") ? playlist.split("#EXT-X-DISCONTINUITY")[0] + "#EXT-X-ENDLIST\n" : '#EXT-X-MAP:URI="/v/T1/recordings/cam1/c.mp4",BYTERANGE="4@0"\n#EXTINF:1,\n/v/T1/api/v1/cameras/cam1/pieces/c.mp4?f=0\n';
        return new Response(body, { status: 200 });
      }
      if (url.includes("/recordings/")) return new Response(new Uint8Array(range ? 4 : 10), { status: range ? 206 : 200 });
      return new Response(new Uint8Array([1, 2, 3]), { status: 200 });
    }) as typeof fetch;
    const progress: number[] = [];
    const files = await exportWindow({
      client,
      camera: { id: "cam1", name: "Gate", recorderId: "rs2" },
      spans: [
        { start: 0, end: 50_000, recorderId: "rs1" },
        { start: 50_000, end: 100_000, recorderId: "rs2" },
        { start: 900_000, end: 950_000, recorderId: "rs3" }, // outside the window
      ],
      from: 10_000,
      to: 70_000,
      route: "direct",
      fetch: fakeFetch,
      onProgress: (p) => progress.push(p.done),
    });
    expect(tokens).toEqual([
      ["cam1", ["export"], "rs1"],
      ["cam1", ["export"], "rs2"],
    ]);
    expect(requests[0].url).toBe("https://rs1.lan:8090/v/T1/api/v1/cameras/cam1/playback.m3u8?from=10000&to=70000");
    expect(requests.find((r) => r.url.endsWith("/recordings/cam1/a.mp4"))?.range).toBe("bytes=0-1199");
    expect(files.map((f) => [f.recorderId, f.blob.size])).toEqual([
      ["rs1", 4 + 3 + 3],
      ["rs2", 4 + 3],
    ]);
    expect(files[0].name).toMatch(/^Gate .* \(1\)\.mp4$/);
    expect(progress.at(-1)).toBe(5);
  });

  it("refuses empty and over-long ranges", async () => {
    const client = { baseUrl: "", cameras: {} } as unknown as ApiClient;
    const cam = { id: "c", name: "C", recorderId: "rs1" };
    await expect(exportWindow({ client, camera: cam, spans: [], from: 0, to: 1000 })).rejects.toThrow(/Nothing was recorded/);
    await expect(exportWindow({ client, camera: cam, spans: [], from: 0, to: 31 * 60e3 })).rejects.toThrow(/30 minutes/);
    await expect(exportWindow({ client, camera: cam, spans: [], from: 5, to: 5 })).rejects.toThrow(/end after/);
  });
});
