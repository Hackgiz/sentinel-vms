// Wave 13's server calls: POST /api/v2/cloud/link with the account JWT and
// POST /api/v2/cloud/refresh. fetch is mocked; nothing leaves the process.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setUnauthorizedHandler } from "../api";
import { CloudError, cloudApi } from "./cloud";

const status = { linked: true, serverId: "abc123", plan: "cloud", status: "active", signIn: null };

function respond(code: number, body: unknown) {
  return vi.fn(async () => new Response(typeof body === "string" ? body : JSON.stringify(body), { status: code, headers: { "Content-Type": "application/json" } }));
}

let signedOut = 0;
beforeEach(() => {
  signedOut = 0;
  setUnauthorizedHandler(() => signedOut++);
});
afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(() => {});
});

describe("cloudApi.linkAccount", () => {
  it("posts only the JWT, as JSON, with the session cookie", async () => {
    const f = respond(200, status);
    vi.stubGlobal("fetch", f);
    expect(await cloudApi.linkAccount("jwt-1")).toEqual(status);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v2/cloud/link");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual({ accessToken: "jwt-1" });
  });

  it("keeps the Sentinel session when Sentinel Cloud refuses the JWT", async () => {
    vi.stubGlobal("fetch", respond(401, { error: { code: "cloud_sign_in", message: "Sentinel Cloud refused the sign-in" } }));
    const err = await cloudApi.linkAccount("bad").catch((e) => e);
    expect(err).toBeInstanceOf(CloudError);
    expect(err).toMatchObject({ status: 401, code: "cloud_sign_in" });
    expect(signedOut).toBe(0);
  });

  it("still signs out on a plain 401", async () => {
    vi.stubGlobal("fetch", respond(401, { error: { code: "unauthorized", message: "sign in" } }));
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ status: 401, code: "unauthorized" });
    expect(signedOut).toBe(1);
  });

  it("carries the upgrade page on the server limit", async () => {
    vi.stubGlobal("fetch", respond(402, { error: { code: "cloud_server_limit", message: "no room", upgradeURL: "https://sentvms.com/cloud" } }));
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ status: 402, code: "cloud_server_limit", upgradeURL: "https://sentvms.com/cloud" });
    vi.stubGlobal("fetch", respond(402, { error: { code: "cloud_server_limit", message: "no room" }, upgradeURL: "https://sentvms.com/pricing" }));
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ upgradeURL: "https://sentvms.com/pricing" });
  });

  it("maps the other codes and a dead server", async () => {
    vi.stubGlobal("fetch", respond(409, { error: { code: "cloud_already_linked", message: "already linked" } }));
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ status: 409, code: "cloud_already_linked" });
    vi.stubGlobal("fetch", respond(502, "<html>bad gateway</html>"));
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ status: 502, code: "internal" });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    await expect(cloudApi.linkAccount("jwt")).rejects.toMatchObject({ status: 0, code: "network" });
  });
});

describe("cloudApi.refresh", () => {
  it("posts with no body and answers the status", async () => {
    const f = respond(200, status);
    vi.stubGlobal("fetch", f);
    expect(await cloudApi.refresh()).toEqual(status);
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v2/cloud/refresh");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
  });
});
