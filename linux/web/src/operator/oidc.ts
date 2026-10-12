// Native single sign-on (openapi.yaml, /api/v2/auth/oidc/start): the client
// opens the start URL in the system browser with a loopback `redirectUri`;
// after sign-in the management server redirects there with `?code=&state=`
// (or `?error=&error_description=&state=`), and the client exchanges the
// code with grantType `oidc_code`. Pure functions, shared by the desktop
// app's loopback listener (linux/desktop/src/oidc.ts) and the browser.

/** Path the loopback listener answers on. */
export const callbackPath = "/oidc/callback";

/** `http://127.0.0.1:<port>/oidc/callback`: what the server accepts as a native redirect. */
export function loopbackRedirectUri(port: number): string {
  if (!Number.isInteger(port) || port <= 0 || port > 65535) throw new Error(`bad loopback port ${port}`);
  return `http://127.0.0.1:${port}${callbackPath}`;
}

/** Whether the server will accept this redirectUri (mirrors directory.SafeRedirectURI). */
export function isSafeRedirectUri(s: string): boolean {
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return false;
  }
  if (u.username || u.password || u.hash || /[\r\n\t\\]/.test(s)) return false;
  if (u.protocol === "sentinel-operator:") return true;
  if (u.protocol !== "http:") return false;
  const port = Number(u.port);
  return (u.hostname === "127.0.0.1" || u.hostname === "localhost") && Number.isInteger(port) && port > 0 && port < 65536;
}

/** The URL to open in the browser to start sign-in at this site. */
export function oidcStartUrl(siteAddress: string, redirectUri: string, state: string): string {
  if (!isSafeRedirectUri(redirectUri)) throw new Error("The sign-in return address isn't one the server accepts.");
  const u = new URL("/api/v2/auth/oidc/start", siteAddress);
  u.searchParams.set("redirectUri", redirectUri);
  u.searchParams.set("state", state);
  return u.toString();
}

/** An unguessable state value binding the callback to this attempt. */
export function newState(bytes: (n: number) => Uint8Array = (n) => crypto.getRandomValues(new Uint8Array(n))): string {
  return Array.from(bytes(16), (b) => b.toString(16).padStart(2, "0")).join("");
}

export type CallbackResult = { kind: "code"; code: string } | { kind: "error"; message: string } | { kind: "ignore" };

/**
 * What a request to the loopback listener means. Anything but the callback
 * path is ignored (browsers ask for /favicon.ico); a callback with the wrong
 * state is an error, so a page that guesses the port can't sign us in.
 */
export function parseCallback(requestUrl: string, expectedState: string, base = "http://127.0.0.1"): CallbackResult {
  let u: URL;
  try {
    u = new URL(requestUrl, base);
  } catch {
    return { kind: "ignore" };
  }
  if (u.pathname !== callbackPath) return { kind: "ignore" };
  const q = u.searchParams;
  if (q.get("state") !== expectedState) return { kind: "error", message: "The sign-in response didn't match this request. Try again." };
  const err = q.get("error");
  if (err) {
    const desc = q.get("error_description");
    return { kind: "error", message: desc || (err === "access_denied" ? "Sign-in was refused." : `Sign-in failed (${err}).`) };
  }
  const code = q.get("code");
  if (!code) return { kind: "error", message: "The sign-in response had no code." };
  return { kind: "code", code };
}
