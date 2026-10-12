import { describe, expect, it } from "vitest";
import { isSafeRedirectUri, loopbackRedirectUri, newState, oidcStartUrl, parseCallback } from "./oidc";

describe("loopback redirect", () => {
  it("builds the start URL with a loopback redirect the server accepts", () => {
    const ru = loopbackRedirectUri(49152);
    expect(ru).toBe("http://127.0.0.1:49152/oidc/callback");
    const u = new URL(oidcStartUrl("https://ms.lan:8090", ru, "abc"));
    expect(u.origin + u.pathname).toBe("https://ms.lan:8090/api/v2/auth/oidc/start");
    expect(u.searchParams.get("redirectUri")).toBe(ru);
    expect(u.searchParams.get("state")).toBe("abc");
    expect(() => loopbackRedirectUri(0)).toThrow();
  });

  it("mirrors the server's redirectUri rule", () => {
    expect(isSafeRedirectUri("http://localhost:5173/operator.html")).toBe(true);
    expect(isSafeRedirectUri("sentinel-operator://callback")).toBe(true);
    expect(isSafeRedirectUri("http://127.0.0.1/cb")).toBe(false); // no port
    expect(isSafeRedirectUri("https://127.0.0.1:9000/cb")).toBe(false);
    expect(isSafeRedirectUri("http://evil.example:9000/cb")).toBe(false);
    expect(isSafeRedirectUri("http://u:p@127.0.0.1:9000/cb")).toBe(false);
    expect(isSafeRedirectUri("http://127.0.0.1:9000/cb#x")).toBe(false);
    expect(() => oidcStartUrl("https://ms", "http://evil.example:1/x", "s")).toThrow();
  });

  it("makes 128-bit hex states", () => {
    expect(newState(() => new Uint8Array(16).fill(171))).toBe("ab".repeat(16));
    expect(newState()).toMatch(/^[0-9a-f]{32}$/);
  });
});

describe("parseCallback", () => {
  it("takes the code when the state matches", () => {
    expect(parseCallback("/oidc/callback?code=xyz&state=s1", "s1")).toEqual({ kind: "code", code: "xyz" });
  });

  it("ignores other paths (favicon) and refuses a foreign state", () => {
    expect(parseCallback("/favicon.ico", "s1")).toEqual({ kind: "ignore" });
    expect(parseCallback("/oidc/callback?code=xyz&state=other", "s1")).toMatchObject({ kind: "error" });
    expect(parseCallback("/oidc/callback?code=xyz", "s1")).toMatchObject({ kind: "error" });
  });

  it("passes on the server's refusal", () => {
    expect(parseCallback("/oidc/callback?error=access_denied&error_description=No+role+for+you&state=s1", "s1")).toEqual({ kind: "error", message: "No role for you" });
    expect(parseCallback("/oidc/callback?error=access_denied&state=s1", "s1")).toEqual({ kind: "error", message: "Sign-in was refused." });
    expect(parseCallback("/oidc/callback?state=s1", "s1")).toMatchObject({ kind: "error" });
  });
});
