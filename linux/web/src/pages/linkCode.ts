// Device-code sign-in (WAVE10(C)): helpers for the /link page, where a
// signed-in person approves a Sentinel TV. Mirrors store.NormalizeUserCode
// on the server (internal/store/device_codes.go).

/** No vowels, nothing that reads like something else (0/O, 1/I/L, 8/B). */
export const USER_CODE_ALPHABET = "CDFGHJKMNPQRSTVWXZ2345679";
export const USER_CODE_LENGTH = 8;

/** True when the dashboard was opened at /link (or #/link). */
export function isLinkRoute(loc: { pathname: string; hash: string } = window.location): boolean {
  const path = loc.pathname.replace(/\/+$/, "");
  return path === "/link" || /^#\/?link(\/|\?|$)/.test(loc.hash);
}

/** The code the device put in the link (`?code=BCDF-GHJK`), if any. */
export function codeFromSearch(search: string = window.location.search): string {
  return formatTyping(new URLSearchParams(search).get("code") ?? "");
}

/**
 * Cleans what someone is typing into the code box: upper-case, only the
 * code's characters, a dash after the fourth, at most eight characters.
 */
export function formatTyping(typed: string): string {
  const chars = [...typed.toUpperCase()].filter((c) => USER_CODE_ALPHABET.includes(c)).slice(0, USER_CODE_LENGTH);
  const s = chars.join("");
  return s.length > 4 ? `${s.slice(0, 4)}-${s.slice(4)}` : s;
}

/** True when the box holds a whole code. */
export function isCompleteCode(formatted: string): boolean {
  return formatted.replace("-", "").length === USER_CODE_LENGTH;
}
