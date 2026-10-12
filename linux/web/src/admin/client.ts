// The Sentinel Admin pages' API v2 client: same-origin, session cookie.
import { onUnauthorized } from "../api";
import { createClient } from "../shared/api";

/** A 401 drops back to the sign-in screen, as on the v1 pages. */
export const v2 = createClient({ onUnauthorized: () => onUnauthorized() });

export function formatBytes(n: number | undefined): string {
  if (n === undefined || n === null) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0;
  while (n >= 1000 && i < units.length - 1) {
    n /= 1000;
    i++;
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`;
}

export const fmtDateTime = (t: number) => (t ? new Date(t).toLocaleString() : "—");
