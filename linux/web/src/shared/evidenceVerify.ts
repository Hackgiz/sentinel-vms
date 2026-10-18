// Typed mirror of the verification helpers inlined in
// linux/internal/evidencepkg/player.html (between BEGIN/END HELPERS), so
// vitest can test them. evidenceVerify.test.ts runs the same vectors against
// both this file and the player's own text, so they cannot drift apart.

export type Verdict = "verified" | "tampered" | "unverified";

export interface ChainEntry {
  id: string;
  time: number;
  user: string;
  area: string;
  action: string;
  detail: string;
  previousHash: string;
  chainHash: string;
}

export function toHex(buf: ArrayBuffer): string {
  const b = new Uint8Array(buf);
  let s = "";
  for (let i = 0; i < b.length; i++) s += (b[i] < 16 ? "0" : "") + b[i].toString(16);
  return s;
}

export async function sha256Hex(bytes: BufferSource): Promise<string> {
  return toHex(await crypto.subtle.digest("SHA-256", bytes));
}

export function normHash(s: unknown): string {
  return String(s == null ? "" : s).trim().toLowerCase();
}

/** VERIFIED only when both hashes exist and agree; no computed hash is UNVERIFIED. */
export function decide(expected: unknown, actual: unknown): Verdict {
  const a = normHash(actual);
  const e = normHash(expected);
  if (!a || !e) return "unverified";
  return a === e ? "verified" : "tampered";
}

/** The manual command shown when the browser blocks reading the file. */
export function shasumLine(file: string): string {
  return 'shasum -a 256 "' + String(file).replace(/(["\\$`])/g, "\\$1") + '"';
}

export function safeRelPath(p: unknown): boolean {
  if (typeof p !== "string" || !p || p.charAt(0) === "/" || /[\\\0]/.test(p)) return false;
  const parts = p.split("/");
  for (const part of parts) if (!part || part === "." || part === "..") return false;
  return true;
}

/** An audit entry's chain hash, as store.AuditEntry computes it. */
export async function chainHash(e: ChainEntry): Promise<string> {
  const payload = [e.previousHash, e.id, String(e.time), e.user, e.area, e.action, e.detail].join("\x1f");
  return sha256Hex(new TextEncoder().encode(payload));
}

export async function checkChain(entries: ChainEntry[]): Promise<{ ok: boolean; bad: string[] }> {
  const bad: string[] = [];
  for (const e of entries) {
    if ((await chainHash(e)) !== normHash(e.chainHash)) bad.push(e.id);
  }
  return { ok: bad.length === 0, bad };
}

/** Evidence audit entries that quote a SHA-256 which is not the manifest's. */
export function chainDisagrees(entries: ChainEntry[], sha: string): string[] {
  const bad: string[] = [];
  const want = normHash(sha);
  for (const e of entries) {
    if (e.area !== "Evidence") continue;
    const m = /sha256 ([0-9a-f]{16,64})/.exec(e.detail || "");
    if (m && want.indexOf(m[1]) !== 0) bad.push(e.id);
  }
  return bad;
}
