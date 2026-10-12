// How the app's pages reach a site, in the app's own session:
//
// - The UI is served from the bundle at sentinel-app://operator/ (never
//   from the server), with a strict Content-Security-Policy.
// - Sentinel servers send no CORS headers (the dashboard is same-origin),
//   so requests from the app's pages to http(s) servers go through a
//   handler that forwards them unchanged and adds CORS headers for the
//   app's origin (answering preflights itself). Nothing else is relaxed:
//   other origins get the server's response as is.
// - TLS is verified by Chromium as usual. A certificate it refuses is
//   accepted only when its SHA-256 fingerprint is pinned for that host,
//   which the operator does after comparing it (the trust prompt).
import type { Certificate, Session } from "electron";
import { readFile } from "node:fs/promises";
import { extname, join, normalize, sep } from "node:path";
import type { Pins } from "./storage";

export const appScheme = "sentinel-app";
export const appHost = "operator";
export const appOrigin = `${appScheme}://${appHost}`;
export const appEntry = `${appOrigin}/operator.html`;

/**
 * The renderer's policy. connect/media/img sources are any http(s) and
 * ws(s) origin because sites and recording servers are whatever the
 * operator types; scripts, styles, frames and plugins come from the
 * bundle only.
 */
export const contentSecurityPolicy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob:",
  "media-src 'self' blob: http: https:",
  "connect-src 'self' http: https: ws: wss:",
  "font-src 'self'",
  "worker-src 'self' blob:",
  "object-src 'none'",
  "frame-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join("; ");

const mime: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".json": "application/json",
  ".woff2": "font/woff2",
};

/** Serve the bundled UI from `root`. Only files under it; no directory listing. */
export function serveApp(ses: Session, root: string) {
  ses.protocol.handle(appScheme, async (req) => {
    const u = new URL(req.url);
    if (u.host !== appHost) return new Response("not found", { status: 404 });
    const rel = normalize(decodeURIComponent(u.pathname)).replace(/^([/\\])+/, "");
    const file = join(root, rel || "operator.html");
    if (!file.startsWith(root + sep)) return new Response("not found", { status: 404 });
    try {
      const body = await readFile(file);
      return new Response(body, {
        headers: {
          "Content-Type": mime[extname(file)] ?? "application/octet-stream",
          "Content-Security-Policy": contentSecurityPolicy,
          "X-Content-Type-Options": "nosniff",
          "Cache-Control": "no-cache",
        },
      });
    } catch {
      return new Response("not found", { status: 404 });
    }
  });
}

const exposed = "Location, ETag, Content-Range, Content-Length, Content-Type, Accept-Patch, Link";

function corsHeaders(origin: string): Record<string, string> {
  return { "Access-Control-Allow-Origin": origin, "Access-Control-Expose-Headers": exposed, Vary: "Origin" };
}

/** Forward http(s) requests from the app's pages, with CORS for the app's origin. */
export function bridgeCors(ses: Session) {
  for (const scheme of ["http", "https"]) {
    ses.protocol.handle(scheme, async (req) => {
      const origin = req.headers.get("origin");
      const fromApp = origin === appOrigin;
      if (fromApp && req.method === "OPTIONS") {
        return new Response(null, {
          status: 204,
          headers: {
            ...corsHeaders(appOrigin),
            "Access-Control-Allow-Methods": "GET, POST, PUT, PATCH, DELETE, OPTIONS",
            "Access-Control-Allow-Headers": req.headers.get("access-control-request-headers") ?? "",
            "Access-Control-Max-Age": "600",
          },
        });
      }
      const headers = new Headers(req.headers);
      // The server sees a plain client request (it checks Origin only for cookie sessions, which the app doesn't use).
      if (fromApp) headers.delete("origin");
      const hasBody = req.method !== "GET" && req.method !== "HEAD";
      const res = await ses.fetch(req.url, {
        method: req.method,
        headers,
        body: hasBody ? await req.arrayBuffer() : undefined,
        redirect: "manual",
        bypassCustomProtocolHandlers: true,
      });
      if (!fromApp) return res;
      const out = new Headers(res.headers);
      for (const [k, v] of Object.entries(corsHeaders(appOrigin))) out.set(k, v);
      return new Response(res.body, { status: res.status, statusText: res.statusText, headers: out });
    });
  }
}

/** "AB:CD:…" from Electron's "sha256/<base64>" fingerprint. */
export function fingerprintHex(cert: Pick<Certificate, "fingerprint">): string {
  const b64 = cert.fingerprint.replace(/^sha256\//, "");
  return Buffer.from(b64, "base64").toString("hex").toUpperCase().match(/../g)?.join(":") ?? "";
}

export interface RefusedCertificate {
  host: string;
  fingerprint: string;
  subject: string;
  issuer: string;
  validFrom: number;
  validTo: number;
  error: string;
}

/**
 * Chromium's verdict, except a refused certificate whose fingerprint is
 * pinned for the host. Refusals are remembered for the trust prompt.
 */
export function verifyWithPins(ses: Session, pins: Pins, refused: Map<string, RefusedCertificate>) {
  const proc = (req: Electron.Request, cb: (verificationResult: number) => void) => {
    if (req.errorCode === 0) return cb(-3);
    const fp = fingerprintHex(req.certificate);
    const host = req.hostname.toLowerCase();
    if (fp && pins.get(host) === fp) return cb(0);
    refused.set(host, {
      host,
      fingerprint: fp,
      subject: req.certificate.subjectName,
      issuer: req.certificate.issuerName,
      validFrom: req.certificate.validStart * 1000,
      validTo: req.certificate.validExpiry * 1000,
      error: req.verificationResult,
    });
    cb(-3);
  };
  ses.setCertificateVerifyProc(proc);
  return {
    /** After a pin changes: drop cached verdicts and open connections. */
    reset() {
      ses.setCertificateVerifyProc(proc);
      void ses.closeAllConnections();
    },
  };
}
