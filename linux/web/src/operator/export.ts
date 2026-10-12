// Export through the video plane (openapi.yaml, "Video"): a video token with
// the `export` right for the recording server that has the footage, the
// window's playback playlist from it, then the playlist's pieces. Each run
// of the playlist (one recording file; runs are split by
// EXT-X-DISCONTINUITY) becomes one standalone fragmented MP4: the file's
// init section followed by its keyframe-aligned pieces, which is what the
// server's own clip export writes before it remuxes.

import { errorFromResponse, type ApiClient } from "../shared/api";
import { playbackUrl, videoBase, type VideoRoute } from "../shared/video";
import { recordersOf, spansOn, type PlaybackSpan } from "./sync";

/** Longest export, as the server's own clip export (playback.MaxClip). */
export const maxExportMs = 30 * 60e3;

export interface PlaylistRun {
  /** Init section: the start of the recording file, `initBytes` long. */
  initUrl: string;
  initBytes: number;
  pieces: string[];
  /** EXT-X-PROGRAM-DATE-TIME of the run's first piece (Unix ms), when given. */
  startsAt?: number;
  /** Seconds of media in the run. */
  duration: number;
}

/** The runs of a playback playlist; URIs resolved against the playlist's URL. */
export function parsePlaylist(text: string, playlistUrl: string): PlaylistRun[] {
  const runs: PlaylistRun[] = [];
  let cur: PlaylistRun | null = null;
  let pendingDate: number | undefined;
  let pendingDur = 0;
  const abs = (u: string) => new URL(u, playlistUrl).toString();
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith("#EXT-X-MAP:")) {
      const uri = /URI="([^"]*)"/.exec(line)?.[1];
      const range = /BYTERANGE="(\d+)(?:@(\d+))?"/.exec(line);
      if (!uri) throw new Error("The playlist's init section has no URI.");
      if (range && range[2] && range[2] !== "0") throw new Error("The playlist's init section doesn't start the file.");
      cur = { initUrl: abs(uri), initBytes: range ? Number(range[1]) : 0, pieces: [], duration: 0 };
      runs.push(cur);
    } else if (line.startsWith("#EXT-X-PROGRAM-DATE-TIME:")) {
      const t = Date.parse(line.slice("#EXT-X-PROGRAM-DATE-TIME:".length));
      pendingDate = Number.isNaN(t) ? undefined : t;
    } else if (line.startsWith("#EXTINF:")) {
      pendingDur = parseFloat(line.slice("#EXTINF:".length)) || 0;
    } else if (!line.startsWith("#")) {
      if (!cur) throw new Error("The playlist has a piece before its init section.");
      if (cur.startsAt === undefined && pendingDate !== undefined) cur.startsAt = pendingDate;
      cur.pieces.push(abs(line));
      cur.duration += pendingDur;
      pendingDur = 0;
      pendingDate = undefined;
    }
  }
  return runs.filter((r) => r.pieces.length > 0);
}

/** "Front door 2026-10-11 14-03-20.mp4": a file name for a run. */
export function exportFileName(cameraName: string, at: number, part?: number): string {
  const d = new Date(at);
  const p = (n: number) => String(n).padStart(2, "0");
  const stamp = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}-${p(d.getMinutes())}-${p(d.getSeconds())}`;
  const safe = cameraName.replace(/[\\/:*?"<>|\u0000-\u001f]+/g, " ").replace(/\s+/g, " ").trim() || "camera";
  return `${safe} ${stamp}${part ? ` (${part})` : ""}.mp4`;
}

export interface ExportFile {
  name: string;
  blob: Blob;
  recorderId: string;
}

export interface ExportProgress {
  done: number;
  total: number;
}

/**
 * Download a window of one camera as MP4 files, one per run, from every
 * recording server that has footage in it.
 */
export async function exportWindow(opts: {
  client: ApiClient;
  camera: { id: string; name: string; recorderId: string };
  spans: PlaybackSpan[];
  from: number;
  to: number;
  route?: VideoRoute;
  fetch?: typeof fetch;
  onProgress?: (p: ExportProgress) => void;
  signal?: AbortSignal;
}): Promise<ExportFile[]> {
  const { client, camera, from, to } = opts;
  if (to <= from) throw new Error("Pick an end after the start.");
  if (to - from > maxExportMs) throw new Error("Exports are limited to 30 minutes.");
  const doFetch = opts.fetch ?? ((i: RequestInfo | URL, init?: RequestInit) => fetch(i, init));
  const inWindow = opts.spans.filter((s) => s.start < to && s.end > from);
  const recorders = recordersOf(inWindow, camera.recorderId);
  if (!recorders.length) throw new Error("Nothing was recorded in that time range.");

  const get = async (url: string, headers?: Record<string, string>) => {
    const res = await doFetch(url, { headers, signal: opts.signal, credentials: "omit" });
    if (!res.ok && res.status !== 206) throw errorFromResponse(res.status, await res.text().catch(() => ""));
    return res;
  };

  // Plan first, so progress has a total.
  const plans: { recorderId: string; runs: PlaylistRun[] }[] = [];
  for (const recorderId of recorders) {
    const token = await client.cameras.videoToken(camera.id, ["export"], recorderId);
    const base = videoBase(token, opts.route ?? "auto", client.baseUrl);
    const url = playbackUrl(base, camera.id, from, to);
    const text = await (await get(url)).text();
    plans.push({ recorderId, runs: parsePlaylist(text, url) });
  }
  const total = plans.reduce((n, p) => n + p.runs.reduce((m, r) => m + r.pieces.length + 1, 0), 0);
  if (!total) throw new Error("Nothing was recorded in that time range.");
  let done = 0;
  const step = () => opts.onProgress?.({ done: ++done, total });

  const files: ExportFile[] = [];
  const allRuns = plans.flatMap((p) => p.runs.map((r) => ({ recorderId: p.recorderId, run: r })));
  let part = 0;
  for (const { recorderId, run } of allRuns) {
    const parts: BlobPart[] = [];
    const initRes = await get(run.initUrl, run.initBytes ? { Range: `bytes=0-${run.initBytes - 1}` } : undefined);
    let init = await initRes.arrayBuffer();
    // A server that ignores Range sends the whole file: keep the init section only.
    if (run.initBytes && init.byteLength > run.initBytes) init = init.slice(0, run.initBytes);
    parts.push(init);
    step();
    for (const piece of run.pieces) {
      parts.push(await (await get(piece)).arrayBuffer());
      step();
    }
    part++;
    const startsAt = run.startsAt ?? firstStartOn(inWindow, recorderId, camera.recorderId, from);
    files.push({ name: exportFileName(camera.name, startsAt, allRuns.length > 1 ? part : undefined), blob: new Blob(parts, { type: "video/mp4" }), recorderId });
  }
  return files;
}

function firstStartOn(spans: PlaybackSpan[], recorderId: string, fallback: string, from: number): number {
  const own = spansOn(spans, recorderId, fallback);
  return Math.max(from, own[0]?.start ?? from);
}

/** Hand a file to the user: a browser download (the desktop app asks where to save it). */
export function saveBlob(name: string, blob: Blob) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}
