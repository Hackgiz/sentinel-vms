// Saved sites: the management servers this Operator knows, by address and
// display name. The list is kept by the desktop app (userData, see
// linux/desktop) or, in a plain browser, in localStorage. Tokens are kept
// separately (tokens.ts); a site record never holds a secret.

/** The Management Server's API and dashboard port (`--listen`). */
export const defaultPort = 8090;

export interface Site {
  /** Stable local id (tokens and window layouts are keyed by it). */
  id: string;
  /** What the operator calls it, e.g. "Warehouse". */
  name: string;
  /** Normalized origin of the management server, e.g. "http://ms.lan:8090". */
  address: string;
  /** Name last used to sign in here (prefills the form; not a secret). */
  lastUser?: string;
  /** Unix ms of the last sign-in; the picker lists recent sites first. */
  lastUsedAt?: number;
}

/**
 * The origin a typed address means, or an error message.
 * Without a scheme the server is assumed to be on the LAN: plain HTTP on
 * port 8090 unless another port is given. With a scheme (a reverse proxy or
 * tunnel, usually https) the scheme's default port applies. Paths are dropped.
 */
export function normalizeAddress(input: string): { address: string } | { error: string } {
  let s = input.trim();
  if (!s) return { error: "Enter the management server's address." };
  const hasScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(s);
  if (!hasScheme) s = `http://${s}`;
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return { error: "That isn't a valid address." };
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return { error: "Use an http:// or https:// address." };
  if (u.username || u.password) return { error: "Leave the name and password out of the address." };
  if (!u.hostname) return { error: "That isn't a valid address." };
  if (!hasScheme && !u.port) u.port = String(defaultPort);
  return { address: u.origin };
}

/** A display name from an address when the operator doesn't give one. */
export function defaultSiteName(address: string): string {
  try {
    return new URL(address).hostname;
  } catch {
    return address;
  }
}

export function newSiteId(random: () => number = Math.random): string {
  let id = "";
  for (let i = 0; i < 12; i++) id += Math.floor(random() * 36).toString(36);
  return `site-${id}`;
}

/**
 * Add a site, or update the one with the same address (one entry per
 * server). Returns the new list and the stored site.
 */
export function upsertSite(sites: Site[], input: { id?: string; name: string; address: string }, makeId: () => string = newSiteId): { sites: Site[]; site: Site } {
  const norm = normalizeAddress(input.address);
  if ("error" in norm) throw new Error(norm.error);
  const name = input.name.trim() || defaultSiteName(norm.address);
  const existing = sites.find((s) => (input.id ? s.id === input.id : s.address === norm.address));
  if (existing) {
    // Changing an address onto another saved site's would make two entries for one server.
    const clash = sites.find((s) => s.id !== existing.id && s.address === norm.address);
    if (clash) throw new Error(`"${clash.name}" already uses that address.`);
    const site = { ...existing, name, address: norm.address };
    return { sites: sites.map((s) => (s.id === existing.id ? site : s)), site };
  }
  const site: Site = { id: makeId(), name, address: norm.address };
  return { sites: [...sites, site], site };
}

export function removeSite(sites: Site[], id: string): Site[] {
  return sites.filter((s) => s.id !== id);
}

/** Note a successful sign-in (moves the site to the top of the picker). */
export function touchSite(sites: Site[], id: string, user: string, now = Date.now()): Site[] {
  return sites.map((s) => (s.id === id ? { ...s, lastUser: user, lastUsedAt: now } : s));
}

/** Recently used first, then by name. */
export function sortSites(sites: Site[]): Site[] {
  return [...sites].sort((a, b) => (b.lastUsedAt ?? 0) - (a.lastUsedAt ?? 0) || a.name.localeCompare(b.name));
}

/** Parse a stored list, dropping anything malformed (a hand-edited file, an older version). */
export function parseSites(raw: unknown): Site[] {
  if (!Array.isArray(raw)) return [];
  const out: Site[] = [];
  for (const r of raw) {
    if (!r || typeof r !== "object") continue;
    const o = r as Record<string, unknown>;
    if (typeof o.id !== "string" || typeof o.address !== "string") continue;
    const norm = normalizeAddress(o.address);
    if ("error" in norm) continue;
    out.push({
      id: o.id,
      name: typeof o.name === "string" && o.name ? o.name : defaultSiteName(norm.address),
      address: norm.address,
      lastUser: typeof o.lastUser === "string" ? o.lastUser : undefined,
      lastUsedAt: typeof o.lastUsedAt === "number" ? o.lastUsedAt : undefined,
    });
  }
  return out;
}
