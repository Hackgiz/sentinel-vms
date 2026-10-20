import { describe, expect, it, vi } from "vitest";
import {
  CHECKOUT_POLL_FOR_MS,
  DEFAULT_PLANS,
  NETWORK_MESSAGE,
  backupLine,
  cardReducer,
  checkoutPollNext,
  checkoutRequest,
  credentialsProblem,
  execute,
  formatGB,
  initialCardState,
  isCheckoutReturn,
  jwtUsable,
  linkErrorMessage,
  oneSentence,
  parseAuthResponse,
  parseRedirectResponse,
  parseServerError,
  parseSignIn,
  planPaid,
  planState,
  planSummary,
  portalRequest,
  priceLabel,
  pushLine,
  remoteLine,
  remoteLineFromAddress,
  returnURLs,
  secureURL,
  signInRequest,
  signUpRequest,
  turnOnLabel,
  usd,
  withoutCheckout,
  type CardEvent,
  type CardState,
  type SignInConfig,
} from "./cloudAccount";

const rawConfig = {
  supabaseUrl: "https://bfmpsvpnxjjrbvbcelqd.supabase.co",
  supabaseAnonKey: "sb_publishable_test",
  siteBase: "https://sentvms.com",
  checkoutURL: "https://bfmpsvpnxjjrbvbcelqd.supabase.co/functions/v1/sentinel-create-checkout",
  portalURL: "https://bfmpsvpnxjjrbvbcelqd.supabase.co/functions/v1/sentinel-portal",
  plans: [
    { id: "cloud", name: "Sentinel Cloud", month: 4.99, year: 49, servers: 1, backupGB: 10, days: 30 },
    { id: "business", name: "Sentinel Cloud Business", month: 19.99, year: 199, servers: 5, backupGB: 100, days: 90 },
  ],
};
const cfg = parseSignIn(rawConfig) as SignInConfig;

describe("parseSignIn", () => {
  it("reads the control plane's /v1/config", () => {
    expect(cfg).toEqual(rawConfig);
  });
  it("is null without what sign-in needs", () => {
    expect(parseSignIn(null)).toBeNull();
    expect(parseSignIn(undefined)).toBeNull();
    expect(parseSignIn("nope")).toBeNull();
    expect(parseSignIn({ ...rawConfig, supabaseAnonKey: "" })).toBeNull();
    expect(parseSignIn({ ...rawConfig, supabaseUrl: undefined })).toBeNull();
  });
  it("never sends a password in the clear", () => {
    expect(parseSignIn({ ...rawConfig, supabaseUrl: "http://bfmpsvpnxjjrbvbcelqd.supabase.co" })).toBeNull();
    expect(secureURL("http://192.168.1.20:54321")).toBe("");
    expect(secureURL("http://127.0.0.1:54321/")).toBe("http://127.0.0.1:54321");
    expect(secureURL("javascript:alert(1)")).toBe("");
    expect(secureURL("not a url")).toBe("");
  });
  it("fills what's missing from the Supabase address and the plans as sold", () => {
    const c = parseSignIn({ supabaseUrl: "https://x.supabase.co/", supabaseAnonKey: "k", plans: [{ id: "", name: "x" }] })!;
    expect(c.supabaseUrl).toBe("https://x.supabase.co");
    expect(c.checkoutURL).toBe("https://x.supabase.co/functions/v1/sentinel-create-checkout");
    expect(c.portalURL).toBe("https://x.supabase.co/functions/v1/sentinel-portal");
    expect(c.siteBase).toBe("https://sentvms.com");
    expect(c.plans).toEqual(DEFAULT_PLANS);
  });
});

describe("requests", () => {
  it("signs in with the password grant", () => {
    const r = signInRequest(cfg, " ana@example.com ", "secret");
    expect(r.url).toBe("https://bfmpsvpnxjjrbvbcelqd.supabase.co/auth/v1/token?grant_type=password");
    expect(r.method).toBe("POST");
    expect(r.headers).toEqual({ apikey: "sb_publishable_test", "content-type": "application/json" });
    expect(JSON.parse(r.body)).toEqual({ email: "ana@example.com", password: "secret" });
  });
  it("signs up with the same fields", () => {
    const r = signUpRequest(cfg, "ana@example.com", "secret");
    expect(r.url).toBe("https://bfmpsvpnxjjrbvbcelqd.supabase.co/auth/v1/signup?redirect_to=https%3A%2F%2Fsentvms.com%2Fcloud");
    expect(r.headers).toEqual({ apikey: "sb_publishable_test", "content-type": "application/json" });
    expect(JSON.parse(r.body)).toEqual({ email: "ana@example.com", password: "secret" });
  });
  it("comes back to this page from Stripe", () => {
    expect(returnURLs("http://192.168.1.20:8090")).toEqual({ success: "http://192.168.1.20:8090/#/cloud?checkout=success", cancel: "http://192.168.1.20:8090/#/cloud" });
    expect(returnURLs("https://nvr.local/")).toEqual({ success: "https://nvr.local/#/cloud?checkout=success", cancel: "https://nvr.local/#/cloud" });
  });
  it("opens Checkout with the JWT, the plan and the return URLs", () => {
    const r = checkoutRequest(cfg, "jwt-1", "business", "year", "http://10.0.0.5:8090");
    expect(r.url).toBe(rawConfig.checkoutURL);
    expect(r.headers).toEqual({ Authorization: "Bearer jwt-1", apikey: "sb_publishable_test", "content-type": "application/json" });
    expect(JSON.parse(r.body)).toEqual({ plan: "business", interval: "year", success_url: "http://10.0.0.5:8090/#/cloud?checkout=success", cancel_url: "http://10.0.0.5:8090/#/cloud" });
  });
  it("opens the Billing Portal with a return URL", () => {
    const r = portalRequest(cfg, "jwt-1", "http://10.0.0.5:8090");
    expect(r.url).toBe(rawConfig.portalURL);
    expect(r.headers.Authorization).toBe("Bearer jwt-1");
    expect(r.headers.apikey).toBe("sb_publishable_test");
    expect(JSON.parse(r.body)).toEqual({ return_url: "http://10.0.0.5:8090/#/cloud" });
  });
  it("sends with the fetch it is handed, without cookies", async () => {
    const fake = vi.fn(async () => new Response('{"access_token":"t"}', { status: 200 }));
    const out = await execute(signInRequest(cfg, "a@b.co", "pw"), fake as unknown as typeof fetch);
    expect(out).toEqual({ status: 200, text: '{"access_token":"t"}' });
    const [url, init] = fake.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("https://bfmpsvpnxjjrbvbcelqd.supabase.co/auth/v1/token?grant_type=password");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("omit");
    expect(init.body).toBe('{"email":"a@b.co","password":"pw"}');
  });
  it("reports no answer as status 0", async () => {
    const fail = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    expect(await execute(signInRequest(cfg, "a@b.co", "pw"), fail as unknown as typeof fetch)).toEqual({ status: 0, text: "" });
  });
});

describe("oneSentence", () => {
  it("keeps one tidy sentence", () => {
    expect(oneSentence("invalid login credentials")).toBe("Invalid login credentials.");
    expect(oneSentence("  Password should be at least 6 characters.   Also something else. ")).toBe("Password should be at least 6 characters.");
    expect(oneSentence("Is it?")).toBe("Is it?");
    expect(oneSentence("Sentinel Cloud: cloud.sentvms.com unreachable")).toBe("Sentinel Cloud: cloud.sentvms.com unreachable.");
    expect(oneSentence("x".repeat(300))).toHaveLength(200);
    expect(oneSentence("", "Fallback.")).toBe("Fallback.");
    expect(oneSentence(42, "Fallback.")).toBe("Fallback.");
  });
});

describe("parseAuthResponse", () => {
  const now = 1_000_000;
  it("takes the session", () => {
    expect(parseAuthResponse("signIn", 200, JSON.stringify({ access_token: "jwt", expires_at: 2000, expires_in: 3600 }), now)).toEqual({ kind: "session", accessToken: "jwt", expiresAt: 2_000_000 });
    expect(parseAuthResponse("signIn", 200, JSON.stringify({ access_token: "jwt", expires_in: 3600 }), now)).toEqual({ kind: "session", accessToken: "jwt", expiresAt: now + 3_600_000 });
    expect(parseAuthResponse("signUp", 200, JSON.stringify({ access_token: "jwt" }), now)).toEqual({ kind: "session", accessToken: "jwt", expiresAt: null });
    expect(parseAuthResponse("signUp", 200, JSON.stringify({ user: { id: "u" }, session: { access_token: "jwt", expires_in: 60 } }), now)).toEqual({ kind: "session", accessToken: "jwt", expiresAt: now + 60_000 });
  });
  it("asks for the email confirmation after a sign-up without a session", () => {
    expect(parseAuthResponse("signUp", 200, JSON.stringify({ user: { id: "u", email: "ana@example.com", identities: [{}] }, session: null }))).toEqual({ kind: "confirm", email: "ana@example.com" });
    expect(parseAuthResponse("signUp", 200, JSON.stringify({ id: "u", email: "ana@example.com" }))).toEqual({ kind: "confirm", email: "ana@example.com" });
  });
  it("spots an existing account hidden by enumeration protection", () => {
    const out = parseAuthResponse("signUp", 200, JSON.stringify({ id: "u", email: "ana@example.com", identities: [] }));
    expect(out).toMatchObject({ kind: "error", reason: "exists" });
  });
  it("words every Supabase error as one sentence", () => {
    const err = (mode: "signIn" | "signUp", status: number, body: unknown) => parseAuthResponse(mode, status, JSON.stringify(body));
    expect(err("signIn", 400, { error: "invalid_grant", error_description: "Invalid login credentials" })).toEqual({ kind: "error", reason: "credentials", message: "Wrong email or password." });
    expect(err("signIn", 400, { code: 400, error_code: "invalid_credentials", msg: "Invalid login credentials" })).toMatchObject({ reason: "credentials" });
    expect(err("signIn", 400, { error_code: "email_not_confirmed", msg: "Email not confirmed" })).toMatchObject({ reason: "unconfirmed", message: expect.stringMatching(/confirm/) });
    expect(err("signUp", 422, { error_code: "user_already_exists", msg: "User already registered" })).toMatchObject({ reason: "exists" });
    expect(err("signUp", 422, { error_code: "weak_password", msg: "Password should be at least 6 characters." })).toEqual({ kind: "error", reason: "password", message: "Password should be at least 6 characters." });
    expect(err("signUp", 400, { error_code: "email_address_invalid", msg: "Email address \"x@y\" is invalid" })).toMatchObject({ reason: "email" });
    expect(err("signIn", 429, { msg: "Request rate limit reached" })).toMatchObject({ reason: "rate" });
    expect(err("signUp", 422, { error_code: "signup_disabled", msg: "Signups not allowed for this instance" })).toMatchObject({ reason: "other", message: expect.stringMatching(/can't be created/) });
    expect(err("signIn", 503, {})).toMatchObject({ reason: "other", message: expect.stringMatching(/isn't answering/) });
    expect(err("signIn", 400, { message: "something odd happened" })).toEqual({ kind: "error", reason: "other", message: "Something odd happened." });
    expect(parseAuthResponse("signIn", 400, "<html>")).toEqual({ kind: "error", reason: "other", message: "Signing in failed (400)." });
    expect(parseAuthResponse("signIn", 0, "")).toEqual({ kind: "error", reason: "network", message: NETWORK_MESSAGE });
    expect(parseAuthResponse("signIn", 200, "{}")).toMatchObject({ kind: "error", reason: "other" });
  });
});

describe("credentialsProblem", () => {
  it("checks before sending", () => {
    expect(credentialsProblem("", "x", "signIn")).toMatch(/email/);
    expect(credentialsProblem("ana", "x", "signIn")).toMatch(/email/);
    expect(credentialsProblem("ana@example.com", "", "signIn")).toMatch(/password/);
    expect(credentialsProblem("ana@example.com", "12345", "signUp")).toMatch(/6 characters/);
    expect(credentialsProblem("ana@example.com", "12345", "signIn")).toBe("");
    expect(credentialsProblem(" ana@example.com ", "123456", "signUp")).toBe("");
  });
});

describe("parseRedirectResponse", () => {
  it("opens Stripe's https page", () => {
    expect(parseRedirectResponse(200, JSON.stringify({ url: "https://checkout.stripe.com/c/pay/cs_test" }))).toEqual({ kind: "url", url: "https://checkout.stripe.com/c/pay/cs_test" });
    expect(parseRedirectResponse(200, JSON.stringify({ url: "http://evil.example/" }))).toMatchObject({ kind: "error" });
    expect(parseRedirectResponse(200, "{}")).toMatchObject({ kind: "error" });
  });
  it("asks to sign in again when the JWT is refused", () => {
    expect(parseRedirectResponse(401, JSON.stringify({ error: "Invalid session — sign in again" }))).toMatchObject({ kind: "error", signIn: true, manage: false });
  });
  it("points an existing subscriber at Manage plan", () => {
    const out = parseRedirectResponse(409, JSON.stringify({ error: "You already subscribe to Sentinel Cloud. Use Manage subscription to change your plan.", manage: true }));
    expect(out).toEqual({ kind: "error", signIn: false, manage: true, message: "This account already has a subscription. Use Manage plan to change it." });
  });
  it("words the rest", () => {
    expect(parseRedirectResponse(404, JSON.stringify({ error: "No subscription found for this account" }))).toMatchObject({ message: "No subscription found for this account." });
    expect(parseRedirectResponse(400, JSON.stringify({ error: 'plan must be "cloud" or "business"' }))).toMatchObject({ message: 'Plan must be "cloud" or "business".' });
    expect(parseRedirectResponse(500, JSON.stringify({ error: "Stripe exploded: " + ["sk", "live", "…"].join("_") + " details" }))).toMatchObject({ message: expect.not.stringMatching(/sk_/) });
    expect(parseRedirectResponse(0, "")).toMatchObject({ message: NETWORK_MESSAGE });
  });
});

describe("jwtUsable", () => {
  it("wants more than a minute left", () => {
    expect(jwtUsable(null, 0)).toBe(true);
    expect(jwtUsable(120_000, 0)).toBe(true);
    expect(jwtUsable(60_000, 0)).toBe(false);
    expect(jwtUsable(10, 100)).toBe(false);
  });
});

describe("server errors", () => {
  it("decodes the v2 shape and a flat one", () => {
    expect(parseServerError(402, JSON.stringify({ error: { code: "cloud_server_limit", message: "no room", upgradeURL: "https://sentvms.com/cloud" } }))).toEqual({
      status: 402,
      code: "cloud_server_limit",
      message: "no room",
      upgradeURL: "https://sentvms.com/cloud",
    });
    expect(parseServerError(402, JSON.stringify({ code: "cloud_server_limit", upgradeURL: "https://sentvms.com/cloud" }))).toMatchObject({ code: "cloud_server_limit", upgradeURL: "https://sentvms.com/cloud" });
    expect(parseServerError(402, JSON.stringify({ code: "cloud_server_limit", upgradeURL: "javascript:alert(1)" })).upgradeURL).toBe("");
    expect(parseServerError(500, "oops")).toEqual({ status: 500, code: "", message: "", upgradeURL: "" });
  });
  it("words each link failure", () => {
    expect(linkErrorMessage({ status: 401, code: "cloud_sign_in", message: "" })).toMatch(/Sign in again/);
    expect(linkErrorMessage({ status: 402, code: "cloud_server_limit", message: "" })).toMatch(/no room for another server/);
    expect(linkErrorMessage({ status: 409, code: "cloud_already_linked", message: "" })).toMatch(/already linked/);
    expect(linkErrorMessage({ status: 502, code: "cloud_unreachable", message: "" })).toMatch(/can't reach Sentinel Cloud/);
    expect(linkErrorMessage({ status: 502, code: "unavailable", message: "Sentinel Cloud: boom" })).toMatch(/can't reach Sentinel Cloud/);
    expect(linkErrorMessage({ status: 403, code: "forbidden", message: "" })).toMatch(/administrator/);
    expect(linkErrorMessage({ status: 0, code: "network", message: "" })).toMatch(/Can't reach this server/);
    expect(linkErrorMessage({ status: 400, code: "bad_request", message: "accessToken is required" })).toBe("AccessToken is required.");
  });
});

const linked = {
  linked: true,
  plan: "cloud",
  status: "active",
  features: ["relay", "push", "backup"],
  quotas: { backupBytes: 10 * 1024 ** 3 },
  remote: { enabled: true, state: "connected", url: "https://abc123-cloud.sentvms.com", error: "" },
  push: { state: "cloud" },
  backup: { enabled: true, bytesUsed: 1.2 * 1024 ** 3 },
};

describe("status lines", () => {
  it("shows the remote address", () => {
    expect(remoteLine(linked)).toEqual({ text: "connected", url: "https://abc123-cloud.sentvms.com", tone: "green" });
    expect(remoteLine({ remote: { state: "connecting" } })).toMatchObject({ text: "connecting…", url: "" });
    expect(remoteLine({ remote: { state: "retrying" } })).toMatchObject({ text: "connecting…" });
    expect(remoteLine({ remote: { state: "inactive" } })).toMatchObject({ text: "off" });
    expect(remoteLine({})).toMatchObject({ text: "off" });
    expect(remoteLineFromAddress({ remoteURL: "https://abc-cloud.sentvms.com", via: "cloud" })).toMatchObject({ text: "connected", url: "https://abc-cloud.sentvms.com" });
    expect(remoteLineFromAddress({ remoteURL: "https://x.trycloudflare.com", via: "tunnel" })).toMatchObject({ text: "off", url: "" });
    expect(remoteLineFromAddress(null)).toMatchObject({ text: "off" });
  });
  it("shows push and backup", () => {
    expect(pushLine(linked)).toBe("through Sentinel Cloud");
    expect(pushLine({ push: { state: "direct" } })).toBe("your own APNs key");
    expect(pushLine({})).toBe("off");
    expect(backupLine(linked)).toBe("on · 1.2 GB of 10 GB");
    expect(backupLine({ ...linked, features: ["relay"] })).toBe("off");
    expect(backupLine({ ...linked, backup: { enabled: false, bytesUsed: 0 } })).toBe("off");
    expect(backupLine({ ...linked, quotas: {} })).toBe("on · 1.2 GB used");
  });
  it("sizes in GB as the plans are sold", () => {
    expect(formatGB(0)).toBe("0 GB");
    expect(formatGB(10 * 1024 ** 3)).toBe("10 GB");
    expect(formatGB(100 * 1024 ** 3)).toBe("100 GB");
    expect(formatGB(1.25 * 1024 ** 3)).toBe("1.3 GB");
    expect(formatGB(9.97 * 1024 ** 3)).toBe("10 GB");
    expect(formatGB(512 * 1024 ** 2)).toBe("512 MB");
    expect(formatGB(2048)).toBe("2 KB");
    expect(formatGB(undefined)).toBe("—");
    expect(formatGB(-1)).toBe("—");
  });
  it("never says tunnel", () => {
    const all = [
      remoteLine(linked).text,
      ...["off", "connecting", "connected", "retrying", "inactive"].map((state) => remoteLine({ remote: { state } }).text),
      ...["off", "cloud", "direct"].map((state) => pushLine({ push: { state } })),
      backupLine(linked),
      remoteLineFromAddress({ remoteURL: "https://x", via: "tunnel" }).text,
    ];
    for (const s of all) expect(s.toLowerCase()).not.toContain("tunnel");
  });
});

describe("plans", () => {
  it("picks the buttons by plan and status", () => {
    expect(planState({ plan: "free", status: "inactive" })).toBe("inactive");
    expect(planState({ plan: "cloud", status: "active" })).toBe("active");
    expect(planState({ plan: "business", status: "grace" })).toBe("grace");
    expect(planState({ plan: "cloud", status: "canceled" })).toBe("canceled");
    expect(planState({ plan: "free", status: "canceled" })).toBe("canceled");
    expect(planState({ plan: "cloud", status: "inactive" })).toBe("inactive");
    expect(planState({})).toBe("inactive");
    expect(planPaid({ plan: "cloud", status: "active" })).toBe(true);
    expect(planPaid({ plan: "cloud", status: "grace" })).toBe(true);
    expect(planPaid({ plan: "free", status: "active" })).toBe(false);
  });
  it("prices them", () => {
    expect(usd(4.99)).toBe("$4.99");
    expect(usd(49)).toBe("$49");
    expect(priceLabel(DEFAULT_PLANS[0], "month")).toBe("$4.99/mo");
    expect(priceLabel(DEFAULT_PLANS[1], "year")).toBe("$199/yr");
    expect(planSummary(DEFAULT_PLANS[0])).toBe("1 server · 10 GB offsite backup · 30 days of alarms");
    expect(planSummary(DEFAULT_PLANS[1])).toBe("5 servers · 100 GB offsite backup · 90 days of alarms");
  });
});

describe("checkout return", () => {
  it("recognises Stripe's return and cleans it up", () => {
    expect(isCheckoutReturn("#/cloud?checkout=success")).toBe(true);
    expect(isCheckoutReturn("#cloud?x=1&checkout=success")).toBe(true);
    expect(isCheckoutReturn("#/cloud")).toBe(false);
    expect(isCheckoutReturn("#/cloud?checkout=cancel")).toBe(false);
    expect(isCheckoutReturn("#/live?checkout=success")).toBe(false);
    expect(withoutCheckout("#/cloud?checkout=success")).toBe("#/cloud");
    expect(withoutCheckout("#/cloud?checkout=success&x=1")).toBe("#/cloud?x=1");
    expect(withoutCheckout("#/live")).toBe("#/live");
  });
  it("polls every 5 s for up to 2 minutes until the plan is active", () => {
    const inactive = { plan: "free", status: "inactive" };
    expect(checkoutPollNext(0, 0, null)).toBe("wait");
    expect(checkoutPollNext(0, 60_000, inactive)).toBe("wait");
    expect(checkoutPollNext(0, CHECKOUT_POLL_FOR_MS - 5_000, inactive)).toBe("wait");
    expect(checkoutPollNext(0, CHECKOUT_POLL_FOR_MS - 4_999, inactive)).toBe("timeout");
    expect(checkoutPollNext(0, 10_000, { plan: "cloud", status: "active" })).toBe("done");
    // One refresh now, then one every 5 s through the 2-minute mark: 25 at most.
    let calls = 0;
    for (let t = 0; ; t += 5_000) {
      calls++;
      if (checkoutPollNext(0, t, inactive) !== "wait") break;
    }
    expect(calls).toBe(25);
  });
});

describe("cardReducer", () => {
  const run = (events: CardEvent[], from: CardState = initialCardState) => events.reduce(cardReducer, from);

  it("signs in and links", () => {
    let s = run([{ type: "loaded", linked: false }]);
    expect(s.phase).toBe("notLinked");
    expect(turnOnLabel(s)).toBe("Turn on Sentinel Cloud");
    s = cardReducer(s, { type: "submit" });
    expect(s).toMatchObject({ phase: "linking", step: "signIn" });
    expect(turnOnLabel(s)).toBe("Signing in…");
    s = cardReducer(s, { type: "loaded", linked: false }); // a background read doesn't yank the form
    expect(s.phase).toBe("linking");
    s = cardReducer(s, { type: "authed" });
    expect(s.step).toBe("link");
    expect(turnOnLabel(s)).toBe("Turning on…");
    s = cardReducer(s, { type: "linked" });
    expect(s).toMatchObject({ phase: "linked", step: null, error: "" });
  });

  it("creates an account that needs the email confirmed", () => {
    let s = run([{ type: "loaded", linked: false }, { type: "mode", mode: "signUp" }, { type: "submit" }]);
    expect(s.step).toBe("signUp");
    expect(turnOnLabel(s)).toBe("Creating your account…");
    s = cardReducer(s, { type: "confirmNeeded", email: "ana@example.com" });
    expect(s).toMatchObject({ phase: "notLinked", mode: "signIn", confirmEmail: "ana@example.com" });
    expect(turnOnLabel(s)).toBe("I confirmed it, sign me in");
    s = cardReducer(s, { type: "submit" });
    expect(s.step).toBe("signIn");
    s = run([{ type: "authed" }, { type: "linked" }], s);
    expect(s).toMatchObject({ phase: "linked", confirmEmail: "" });
  });

  it("goes back to the form with one sentence on failure", () => {
    const form = run([{ type: "loaded", linked: false }, { type: "mode", mode: "signUp" }, { type: "submit" }]);
    let s = cardReducer(form, { type: "authFailed", message: "That email already has an account. Sign in instead.", reason: "exists" });
    expect(s).toMatchObject({ phase: "notLinked", mode: "signIn", error: "That email already has an account. Sign in instead." });
    s = run([{ type: "submit" }, { type: "authed" }, { type: "linkFailed", message: "No room.", upgradeURL: "https://sentvms.com/cloud" }], s);
    expect(s).toMatchObject({ phase: "notLinked", error: "No room.", errorURL: "https://sentvms.com/cloud" });
    s = cardReducer(s, { type: "mode", mode: "signUp" });
    expect(s).toMatchObject({ error: "", errorURL: "", mode: "signUp" });
    s = cardReducer(s, { type: "invalid", message: "Enter your password." });
    expect(s.error).toBe("Enter your password.");
  });

  it("asks for the password again when Subscribe has no JWT", () => {
    const action = { kind: "checkout", plan: "cloud", interval: "month" } as const;
    let s = run([{ type: "loaded", linked: true }, { type: "needSignIn", action }]);
    expect(s).toMatchObject({ phase: "linked", pending: action, busy: false });
    s = cardReducer(s, { type: "redirecting" });
    expect(s.busy).toBe(true);
    s = cardReducer(s, { type: "authFailed", message: "Wrong email or password.", reason: "credentials" });
    expect(s).toMatchObject({ phase: "linked", pending: action, busy: false, error: "Wrong email or password." });
    s = run([{ type: "redirecting" }, { type: "redirected" }], s);
    expect(s).toMatchObject({ pending: null, busy: false, error: "", watchFocus: true });
    s = run([{ type: "focusSeen", paid: false }], s);
    expect(s.watchFocus).toBe(true);
    s = run([{ type: "focusSeen", paid: true }], s);
    expect(s.watchFocus).toBe(false);
  });

  it("keeps the inline sign-in when Stripe refuses the JWT", () => {
    const action = { kind: "portal" } as const;
    let s = run([{ type: "loaded", linked: true }, { type: "redirecting" }, { type: "redirectFailed", message: "Expired.", signIn: true, action }]);
    expect(s).toMatchObject({ pending: action, error: "Expired.", busy: false });
    s = cardReducer(s, { type: "cancelSignIn" });
    expect(s).toMatchObject({ pending: null, error: "" });
    s = run([{ type: "redirecting" }, { type: "redirectFailed", message: "Try again.", signIn: false, action }], s);
    expect(s).toMatchObject({ pending: null, error: "Try again." });
  });

  it("turns off, and notices an unlink made elsewhere", () => {
    let s = run([{ type: "loaded", linked: true }, { type: "unlinked" }]);
    expect(s).toEqual({ ...initialCardState, phase: "notLinked" });
    s = run([{ type: "loaded", linked: true }, { type: "loaded", linked: false }]);
    expect(s.phase).toBe("notLinked");
    s = run([{ type: "loaded", linked: true }], s);
    expect(s.phase).toBe("linked");
  });

  it("tracks the checkout return", () => {
    let s = run([{ type: "loaded", linked: true }, { type: "checkoutReturn" }]);
    expect(s.checkout).toBe("waiting");
    expect(cardReducer(s, { type: "checkoutDone" }).checkout).toBe("done");
    expect(cardReducer(s, { type: "checkoutTimeout" }).checkout).toBe("timeout");
    s = cardReducer(s, { type: "unlinked" });
    expect(s.checkout).toBe("idle");
  });

  it("handles load failures and viewers", () => {
    let s = cardReducer(initialCardState, { type: "loadFailed", message: "Down." });
    expect(s).toMatchObject({ phase: "unavailable", error: "Down." });
    s = cardReducer(s, { type: "loaded", linked: false });
    expect(s).toMatchObject({ phase: "notLinked", error: "" });
    // A failed background read leaves a working card alone.
    expect(cardReducer(s, { type: "loadFailed", message: "Blip." })).toBe(s);
    expect(cardReducer(initialCardState, { type: "viewer" }).phase).toBe("viewer");
    // Submitting outside the form does nothing.
    expect(cardReducer(initialCardState, { type: "submit" })).toBe(initialCardState);
  });
});
