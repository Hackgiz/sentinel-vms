import { describe, expect, it } from "vitest";
import playerHtml from "../../../internal/evidencepkg/player.html?raw";
import * as ts from "./evidenceVerify";
import type { ChainEntry } from "./evidenceVerify";

// The player's own helper text, evaluated: the same vectors must pass for it.
const block = /\/\/ BEGIN HELPERS[^\n]*\n([\s\S]*?)\/\/ END HELPERS/.exec(playerHtml);
const names = ["toHex", "sha256Hex", "normHash", "decide", "shasumLine", "safeRelPath", "chainHash", "checkChain", "chainDisagrees"] as const;
const player = new Function(`${block?.[1] ?? ""}\nreturn { ${names.join(", ")} };`)() as typeof ts;

const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";

async function entry(over: Partial<ChainEntry> = {}): Promise<ChainEntry> {
  const e: ChainEntry = { id: "a1", time: 100, user: "op", area: "Evidence", action: "Locked evidence", detail: `CASE-1, sha256 ${abc.slice(0, 16)}…`, previousHash: "", chainHash: "", ...over };
  e.chainHash = await ts.chainHash(e);
  return e;
}

describe.each([
  ["evidenceVerify.ts", ts],
  ["player.html", player],
] as const)("%s helpers", (_name, h) => {
  it("finds the player's helper block", () => {
    expect(block).not.toBeNull();
  });

  it("hashes with WebCrypto", async () => {
    expect(await h.sha256Hex(new TextEncoder().encode("abc"))).toBe(abc);
    expect(h.toHex(new Uint8Array([0, 15, 16, 255]).buffer)).toBe("000f10ff");
  });

  it("decides VERIFIED / TAMPERED / UNVERIFIED", () => {
    expect(h.decide(abc, abc)).toBe("verified");
    expect(h.decide(abc.toUpperCase(), ` ${abc}\n`)).toBe("verified");
    expect(h.decide(abc, "0".repeat(64))).toBe("tampered");
    expect(h.decide(abc, "")).toBe("unverified");
    expect(h.decide(abc, null)).toBe("unverified");
    expect(h.decide("", abc)).toBe("unverified");
  });

  it("builds a shell-safe shasum line", () => {
    expect(h.shasumLine("CASE-1.mp4")).toBe('shasum -a 256 "CASE-1.mp4"');
    expect(h.shasumLine('a"b$c`d\\e.mp4')).toBe('shasum -a 256 "a\\"b\\$c\\`d\\\\e.mp4"');
  });

  it("accepts only plain relative media paths", () => {
    for (const ok of ["a.mp4", "dir/a.mp4"]) expect(h.safeRelPath(ok)).toBe(true);
    for (const bad of ["", "/a", "../a", "a/../b", "a//b", "./a", "a\\b", "http://x/a"]) expect(h.safeRelPath(bad)).toBe(false);
    expect(h.safeRelPath(undefined as unknown as string)).toBe(false);
  });

  it("checks audit chain entries and their quoted hash", async () => {
    const good = await entry();
    const next = await entry({ id: "a2", action: "Downloaded evidence", detail: "CASE-1", previousHash: good.chainHash });
    expect(await h.checkChain([good, next])).toEqual({ ok: true, bad: [] });
    expect(await h.checkChain([{ ...good, user: "mallory" }, next])).toEqual({ ok: false, bad: ["a1"] });
    expect(h.chainDisagrees([good, next], abc)).toEqual([]);
    expect(h.chainDisagrees([good, next], "f".repeat(64))).toEqual(["a1"]);
  });

  it("matches the Go chain hash", async () => {
    // Same vector as TestChainHashVector in evidencepkg_test.go (shasum of the 0x1f-joined fields).
    const e = await entry({ id: "a1", time: 100, detail: "x" });
    expect(await h.chainHash(e)).toBe("b0002961e5aab684fe9d7366ec72a3fde5211798ad7081c489afdd795f7936cc");
  });
});
