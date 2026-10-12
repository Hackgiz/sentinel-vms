// Native single sign-on (RFC 8252 style): listen on a random loopback port,
// open the site's /api/v2/auth/oidc/start in the system browser with that
// redirectUri, and wait for the identity provider's round trip to come back
// with `?code=&state=`. The code is handed to the page, which exchanges it
// (grantType oidc_code). URL handling is shared with the web build.
import { shell } from "electron";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { loopbackRedirectUri, newState, oidcStartUrl, parseCallback } from "../../web/src/operator/oidc";

const timeoutMs = 5 * 60_000;

const page = (title: string, text: string) =>
  `<!doctype html><html><head><meta charset="utf-8"><title>${title}</title>` +
  `<style>body{font:15px system-ui,sans-serif;background:#0a0e14;color:#f2f4f8;display:grid;place-items:center;height:100vh;margin:0}div{max-width:420px;text-align:center}</style>` +
  `</head><body><div><h2>${title}</h2><p>${text}</p></div></body></html>`;

const escape = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

let current: { cancel: (why: string) => void } | null = null;

/** Run one sign-in; a new one cancels the previous. */
export function oidcSignIn(siteAddress: string): Promise<{ code: string }> {
  current?.cancel("Single sign-on was restarted.");
  return new Promise((resolve, reject) => {
    const state = newState();
    let server: Server | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const finish = (err: Error | null, code?: string) => {
      if (timer) clearTimeout(timer);
      server?.close();
      server = null;
      if (current === handle) current = null;
      if (err) reject(err);
      else resolve({ code: code! });
    };
    const handle = { cancel: (why: string) => finish(new Error(why)) };
    current = handle;

    server = createServer((req, res) => {
      const r = parseCallback(req.url ?? "/", state);
      if (r.kind === "ignore") {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
      if (r.kind === "code") {
        res.end(page("Signed in", "You can close this tab and go back to Sentinel Operator."));
        finish(null, r.code);
      } else {
        res.end(page("Sign-in didn't work", escape(r.message)));
        finish(new Error(r.message));
      }
    });
    server.on("error", (e) => finish(e));
    server.listen(0, "127.0.0.1", () => {
      const port = (server!.address() as AddressInfo).port;
      let url: string;
      try {
        url = oidcStartUrl(siteAddress, loopbackRedirectUri(port), state);
      } catch (e) {
        finish(e as Error);
        return;
      }
      timer = setTimeout(() => finish(new Error("Single sign-on timed out. Try again.")), timeoutMs);
      shell.openExternal(url).catch(() => finish(new Error("Couldn't open the web browser for single sign-on.")));
    });
  });
}

export function oidcCancel() {
  current?.cancel("Single sign-on was cancelled.");
}
