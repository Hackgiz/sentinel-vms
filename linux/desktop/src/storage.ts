// What the app keeps in its userData folder:
//   sites.json    saved sites (no secrets)
//   tokens.json   sign-ins per site, each encrypted with safeStorage
//   pins.json     trusted certificate fingerprints per host
//   windows.json  window layouts per display setup
// Writes are atomic (temp file + rename) and owner-only.
import { safeStorage } from "electron";
import { mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

export class JsonFile<T> {
  private value: T | undefined;
  constructor(
    private readonly path: string,
    private readonly fallback: () => T,
  ) {}

  read(): T {
    if (this.value !== undefined) return this.value;
    try {
      this.value = JSON.parse(readFileSync(this.path, "utf8")) as T;
    } catch {
      this.value = this.fallback();
    }
    return this.value;
  }

  write(v: T) {
    this.value = v;
    mkdirSync(dirname(this.path), { recursive: true, mode: 0o700 });
    const tmp = `${this.path}.tmp`;
    writeFileSync(tmp, JSON.stringify(v, null, 2), { mode: 0o600 });
    renameSync(tmp, this.path);
  }
}

export interface TokenPair {
  accessToken: string;
  expiresAt: number;
  refreshToken: string;
  refreshExpiresAt: number;
}

/**
 * Sign-ins per site. Encrypted with the OS keyring through safeStorage;
 * where Linux has no keyring (safeStorage would fall back to a fixed key,
 * "basic_text"), they are kept in memory only and the operator signs in
 * again after a restart.
 */
export class TokenVault {
  private readonly file: JsonFile<Record<string, string>>;
  private readonly memory = new Map<string, TokenPair>();

  constructor(dir: string) {
    this.file = new JsonFile(join(dir, "tokens.json"), () => ({}));
  }

  encrypted(): boolean {
    if (!safeStorage.isEncryptionAvailable()) return false;
    if (process.platform === "linux" && safeStorage.getSelectedStorageBackend() === "basic_text") return false;
    return true;
  }

  load(siteId: string): TokenPair | null {
    if (this.memory.has(siteId)) return this.memory.get(siteId)!;
    if (!this.encrypted()) return null;
    const sealed = this.file.read()[siteId];
    if (!sealed) return null;
    try {
      const t = JSON.parse(safeStorage.decryptString(Buffer.from(sealed, "base64"))) as TokenPair;
      if (typeof t?.accessToken !== "string" || typeof t?.refreshToken !== "string") return null;
      this.memory.set(siteId, t);
      return t;
    } catch {
      return null; // another machine's or user's keyring
    }
  }

  save(siteId: string, t: TokenPair | null) {
    if (t) this.memory.set(siteId, t);
    else this.memory.delete(siteId);
    const all = { ...this.file.read() };
    if (t && this.encrypted()) all[siteId] = safeStorage.encryptString(JSON.stringify(t)).toString("base64");
    else delete all[siteId];
    this.file.write(all);
  }
}

/** Certificate pins: host → SHA-256 fingerprint the operator trusted. */
export class Pins {
  private readonly file: JsonFile<Record<string, string>>;
  constructor(dir: string) {
    this.file = new JsonFile(join(dir, "pins.json"), () => ({}));
  }
  get(host: string): string | null {
    return this.file.read()[host.toLowerCase()] ?? null;
  }
  set(host: string, fingerprint: string | null) {
    const all = { ...this.file.read() };
    if (fingerprint) all[host.toLowerCase()] = fingerprint;
    else delete all[host.toLowerCase()];
    this.file.write(all);
  }
}
