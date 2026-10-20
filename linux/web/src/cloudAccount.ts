// Sentinel Cloud from the browser (WAVE13-CLOUD-ONBOARDING.md, Stream C):
// the Supabase sign-in and sign-up requests, Stripe Checkout and Billing
// Portal requests, the return URLs, every error as one sentence, the card's
// three status lines and its state machine. Pure: nothing here touches the
// network or storage (`execute` sends only with the fetch it is handed). The
// account JWT only passes through; the card keeps it in page memory and
// never in localStorage or on the server.

// ------------------------------------------------------------------ config ---

export type Interval = "month" | "year";
export type Mode = "signIn" | "signUp";

export interface PlanOffer {
  id: string;
  name: string;
  month: number;
  year: number;
  servers: number;
  backupGB: number;
  days: number;
}

/** The control plane's public `/v1/config`, as `GET /api/v2/cloud` relays it in `signIn`. */
export interface SignInConfig {
  supabaseUrl: string;
  supabaseAnonKey: string;
  siteBase: string;
  checkoutURL: string;
  portalURL: string;
  plans: PlanOffer[];
}

/** The plans as sold, for a config that lists none. */
export const DEFAULT_PLANS: PlanOffer[] = [
  { id: "cloud", name: "Sentinel Cloud", month: 4.99, year: 49, servers: 1, backupGB: 10, days: 30 },
  { id: "business", name: "Sentinel Cloud Business", month: 19.99, year: 199, servers: 5, backupGB: 100, days: 90 },
];

export const SITE_BASE = "https://sentvms.com";

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
const num = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const obj = (v: unknown): Record<string, unknown> => (v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {});

/**
 * An https URL without its trailing slash, or "". Plain http is accepted for
 * loopback only (a local Supabase in development): the password goes to this
 * address, so it must never travel in the clear.
 */
export function secureURL(v: unknown): string {
  const s = str(v);
  if (!s) return "";
  try {
    const u = new URL(s);
    const loopback = u.hostname === "localhost" || u.hostname === "127.0.0.1" || u.hostname === "[::1]";
    if (u.protocol !== "https:" && !(u.protocol === "http:" && loopback)) return "";
    return s.replace(/\/+$/, "");
  } catch {
    return "";
  }
}

function parsePlans(v: unknown): PlanOffer[] {
  if (!Array.isArray(v)) return DEFAULT_PLANS;
  const plans = v
    .map(obj)
    .map((p) => ({ id: str(p.id), name: str(p.name), month: num(p.month), year: num(p.year), servers: num(p.servers), backupGB: num(p.backupGB), days: num(p.days) }))
    .filter((p) => p.id && p.name && (p.month > 0 || p.year > 0));
  return plans.length ? plans : DEFAULT_PLANS;
}

/**
 * Decode `signIn` from `GET /api/v2/cloud` tolerantly. null when the server
 * sent none (Sentinel Cloud unreachable, or an older server) or it lacks
 * what sign-in needs.
 */
export function parseSignIn(raw: unknown): SignInConfig | null {
  const c = obj(raw);
  const supabaseUrl = secureURL(c.supabaseUrl);
  const supabaseAnonKey = str(c.supabaseAnonKey);
  if (!supabaseUrl || !supabaseAnonKey) return null;
  return {
    supabaseUrl,
    supabaseAnonKey,
    siteBase: secureURL(c.siteBase) || SITE_BASE,
    checkoutURL: secureURL(c.checkoutURL) || `${supabaseUrl}/functions/v1/sentinel-create-checkout`,
    portalURL: secureURL(c.portalURL) || `${supabaseUrl}/functions/v1/sentinel-portal`,
    plans: parsePlans(c.plans),
  };
}

// ---------------------------------------------------------------- requests ---

/** A JSON POST, ready for `fetch(r.url, r)`. */
export interface JsonRequest {
  url: string;
  method: "POST";
  headers: Record<string, string>;
  body: string;
}

function post(url: string, headers: Record<string, string>, body: unknown): JsonRequest {
  return { url, method: "POST", headers: { ...headers, "content-type": "application/json" }, body: JSON.stringify(body) };
}

/**
 * `POST {supabaseUrl}/auth/v1/token?grant_type=password` (sign in) or
 * `/auth/v1/signup` (create account). A sign-up's confirmation link opens
 * sentvms.com/cloud, as the site's own sign-up does; without `redirect_to`
 * it would land on the home page, which starts a download.
 */
export function authRequest(cfg: SignInConfig, mode: Mode, email: string, password: string): JsonRequest {
  const path = mode === "signUp" ? `/auth/v1/signup?redirect_to=${encodeURIComponent(`${cfg.siteBase}/cloud`)}` : "/auth/v1/token?grant_type=password";
  return post(cfg.supabaseUrl + path, { apikey: cfg.supabaseAnonKey }, { email: email.trim(), password });
}

export const signInRequest = (cfg: SignInConfig, email: string, password: string) => authRequest(cfg, "signIn", email, password);
export const signUpRequest = (cfg: SignInConfig, email: string, password: string) => authRequest(cfg, "signUp", email, password);

/** Where Stripe sends the browser back: this page, which refreshes the plan on `?checkout=success`. */
export function returnURLs(origin: string): { success: string; cancel: string } {
  const base = origin.replace(/\/+$/, "");
  return { success: `${base}/#/cloud?checkout=success`, cancel: `${base}/#/cloud` };
}

const bearer = (cfg: SignInConfig, jwt: string) => ({ Authorization: `Bearer ${jwt}`, apikey: cfg.supabaseAnonKey });

/** Stripe Checkout for a plan: answers `{url}`, opened in a new tab. */
export function checkoutRequest(cfg: SignInConfig, jwt: string, plan: string, interval: Interval, origin: string): JsonRequest {
  const back = returnURLs(origin);
  return post(cfg.checkoutURL, bearer(cfg, jwt), { plan, interval, success_url: back.success, cancel_url: back.cancel });
}

/** The Stripe Billing Portal: answers `{url}`. */
export function portalRequest(cfg: SignInConfig, jwt: string, origin: string): JsonRequest {
  return post(cfg.portalURL, bearer(cfg, jwt), { return_url: returnURLs(origin).cancel });
}

/**
 * Send a prepared request with the fetch it is given (the page passes the
 * browser's; tests a fake). No cookies, no referrer. `status` 0 when nothing
 * answered: offline, DNS, CORS or a Content-Security-Policy block.
 */
export async function execute(r: JsonRequest, doFetch: typeof fetch): Promise<{ status: number; text: string }> {
  try {
    const res = await doFetch(r.url, { method: r.method, headers: r.headers, body: r.body, credentials: "omit", referrerPolicy: "no-referrer" });
    return { status: res.status, text: await res.text() };
  } catch {
    return { status: 0, text: "" };
  }
}

// ------------------------------------------------------------------ errors ---

/**
 * Any message as exactly one sentence: whitespace collapsed, the first
 * sentence only, capitalised, ending in a full stop; `fallback` when empty.
 */
export function oneSentence(raw: unknown, fallback = ""): string {
  let s = (typeof raw === "string" ? raw : "").replace(/\s+/g, " ").trim();
  if (!s) return fallback;
  const first = s.match(/^.*?[.!?](?=\s|$)/);
  if (first) s = first[0];
  if (s.length > 200) s = `${s.slice(0, 199).trimEnd()}…`;
  s = s[0].toUpperCase() + s.slice(1);
  return /[.!?…]$/.test(s) ? s : `${s}.`;
}

function parseJSON(text: string): Record<string, unknown> {
  try {
    return obj(JSON.parse(text));
  } catch {
    return {};
  }
}

/** Supabase errors arrive as `{error_description | msg | message | error}`. */
function supabaseMessage(d: Record<string, unknown>): string {
  return str(d.error_description) || str(d.msg) || str(d.message) || str(d.error) || str(obj(d.error).message);
}

export const NETWORK_MESSAGE = "Can't reach Sentinel Cloud. Check this computer's internet connection.";

// ------------------------------------------------------------- sign-in out ---

export type AuthReason = "credentials" | "unconfirmed" | "exists" | "password" | "email" | "rate" | "network" | "other";

export type AuthOutcome =
  | { kind: "session"; accessToken: string; /** Unix ms, or null when unknown. */ expiresAt: number | null }
  | { kind: "confirm"; email: string }
  | { kind: "error"; reason: AuthReason; message: string };

function sessionOf(d: Record<string, unknown>, now: number): AuthOutcome | null {
  const token = str(d.access_token);
  if (!token) return null;
  const at = num(d.expires_at);
  const inSecs = num(d.expires_in);
  return { kind: "session", accessToken: token, expiresAt: at ? at * 1000 : inSecs ? now + inSecs * 1000 : null };
}

/** What Supabase answered to a sign-in or sign-up. `status` 0 = no answer at all. */
export function parseAuthResponse(mode: Mode, status: number, bodyText: string, now: number = Date.now()): AuthOutcome {
  if (status === 0) return { kind: "error", reason: "network", message: NETWORK_MESSAGE };
  const d = parseJSON(bodyText);
  if (status >= 200 && status < 300) {
    const s = sessionOf(d, now) ?? sessionOf(obj(d.session), now);
    if (s) return s;
    if (mode === "signUp") {
      // Email confirmation on: a user and no session. With enumeration
      // protection an existing address answers a user with no identities.
      const user = Object.keys(obj(d.user)).length ? obj(d.user) : d;
      if (Array.isArray(user.identities) && user.identities.length === 0) {
        return { kind: "error", reason: "exists", message: "That email already has an account. Sign in instead." };
      }
      if (str(user.email) || str(user.id)) return { kind: "confirm", email: str(user.email) };
    }
    return { kind: "error", reason: "other", message: "Sentinel Cloud didn't send a sign-in back. Try again." };
  }
  return { kind: "error", ...authError(mode, status, d) };
}

function authError(mode: Mode, status: number, d: Record<string, unknown>): { reason: AuthReason; message: string } {
  const raw = supabaseMessage(d);
  const m = raw.toLowerCase();
  const code = (str(d.error_code) || str(d.code)).toLowerCase();
  const has = (...xs: string[]) => xs.some((x) => m.includes(x) || code === x);
  if (status === 429 || has("over_request_rate_limit", "over_email_send_rate_limit", "rate limit")) {
    return { reason: "rate", message: "Too many tries. Wait a minute and try again." };
  }
  if (has("email_not_confirmed", "not confirmed")) {
    return { reason: "unconfirmed", message: "Your email isn't confirmed yet. Open the confirmation email, then sign in." };
  }
  if (has("user_already_exists", "email_exists", "already registered", "already exists")) {
    return { reason: "exists", message: "That email already has an account. Sign in instead." };
  }
  if (has("invalid_credentials", "invalid login", "invalid_grant")) {
    return { reason: "credentials", message: "Wrong email or password." };
  }
  if (has("email_address_invalid", "validation_failed") && m.includes("email")) {
    return { reason: "email", message: "Enter a valid email address." };
  }
  if (has("weak_password", "password")) {
    return { reason: "password", message: oneSentence(raw, "Choose a longer password, at least 6 characters.") };
  }
  if (has("signup_disabled", "signups not allowed")) {
    return { reason: "other", message: "New accounts can't be created right now. Try again later." };
  }
  if (status >= 500) return { reason: "other", message: "Sentinel Cloud's sign-in isn't answering right now. Try again in a minute." };
  return { reason: "other", message: oneSentence(raw, mode === "signUp" ? `Creating the account failed (${status}).` : `Signing in failed (${status}).`) };
}

/** Checked before anything is sent: "" when the fields are fine. */
export function credentialsProblem(email: string, password: string, mode: Mode): string {
  const e = email.trim();
  if (!e || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e)) return "Enter the email address of your Sentinel Cloud account.";
  if (!password) return "Enter your password.";
  if (mode === "signUp" && password.length < 6) return "Choose a password of at least 6 characters.";
  return "";
}

// ------------------------------------------------------- checkout / portal ---

export type RedirectOutcome =
  | { kind: "url"; url: string }
  | { kind: "error"; message: string; /** The JWT was refused: sign in again. */ signIn: boolean; /** Already subscribed: use Manage plan. */ manage: boolean };

/** What the checkout or portal function answered. */
export function parseRedirectResponse(status: number, bodyText: string): RedirectOutcome {
  const fail = (message: string, signIn = false, manage = false): RedirectOutcome => ({ kind: "error", message, signIn, manage });
  if (status === 0) return fail(NETWORK_MESSAGE);
  const d = parseJSON(bodyText);
  if (status >= 200 && status < 300) {
    const url = secureURL(d.url);
    return url ? { kind: "url", url } : fail("Stripe didn't send a page to open. Try again.");
  }
  if (status === 401) return fail("Your Sentinel Cloud sign-in has expired. Sign in again to continue.", true);
  if (status === 409 && d.manage) return fail("This account already has a subscription. Use Manage plan to change it.", false, true);
  if (status === 404) return fail(oneSentence(supabaseMessage(d), "This account has no subscription yet."));
  if (status >= 500) return fail("Stripe couldn't be opened right now. Try again in a minute.");
  return fail(oneSentence(supabaseMessage(d), `Stripe couldn't be opened (${status}).`));
}

/** A JWT is worth sending while it has more than a minute left. */
export function jwtUsable(expiresAt: number | null, now: number = Date.now()): boolean {
  return expiresAt === null || expiresAt - now > 60_000;
}

// ------------------------------------------------------------ server side ---

/** An error from the Sentinel server's `/api/v2/cloud/*`. */
export interface ServerError {
  status: number;
  code: string;
  message: string;
  upgradeURL: string;
}

/** Decode `{error:{code,message,upgradeURL}}` or a flat `{code,message,upgradeURL}`. */
export function parseServerError(status: number, bodyText: string): ServerError {
  const d = parseJSON(bodyText);
  const e = obj(d.error);
  return {
    status,
    code: str(e.code) || str(d.code),
    message: str(e.message) || str(d.message) || str(d.error),
    upgradeURL: secureURL(e.upgradeURL) || secureURL(d.upgradeURL),
  };
}

/** `POST /api/v2/cloud/link` failures, as the card says them. */
export function linkErrorMessage(e: Pick<ServerError, "status" | "code" | "message">): string {
  switch (e.code) {
    case "cloud_sign_in":
      return "Sentinel Cloud didn't accept that sign-in. Sign in again.";
    case "cloud_server_limit":
      return "Your plan has no room for another server. Upgrade it, or turn off Sentinel Cloud on another server first.";
    case "cloud_already_linked":
      return "This server is already linked to Sentinel Cloud.";
    case "cloud_unreachable":
      return "This server can't reach Sentinel Cloud right now. Try again in a minute.";
  }
  if (e.status === 0) return "Can't reach this server. Check the connection.";
  if (e.status === 403) return "Only an administrator can turn on Sentinel Cloud.";
  if (e.status === 404) return "Sentinel Cloud is turned on from the Management Server.";
  if (e.status === 409) return "This server is already linked to Sentinel Cloud.";
  if (e.status === 502 || e.status === 503) return "This server can't reach Sentinel Cloud right now. Try again in a minute.";
  return oneSentence(e.message, `Turning on Sentinel Cloud failed (${e.status}).`);
}

// ------------------------------------------------------------ status lines ---

export type Tone = "green" | "amber" | "red" | "";

/** The parts of CloudStatus the card reads, every field optional (tolerant decoding). */
export interface StatusLike {
  linked?: boolean;
  plan?: string;
  status?: string;
  features?: string[];
  quotas?: { backupBytes?: number };
  remote?: { enabled?: boolean; state?: string; url?: string; error?: string };
  push?: { state?: string };
  backup?: { enabled?: boolean; bytesUsed?: number };
}

/** "connected" (+ the address), "connecting…" or "off". */
export function remoteLine(s: StatusLike): { text: string; url: string; tone: Tone } {
  const r = s.remote ?? {};
  switch (r.state) {
    case "connected":
      return { text: "connected", url: str(r.url), tone: "green" };
    case "connecting":
    case "retrying":
      return { text: "connecting…", url: "", tone: "amber" };
    default:
      return { text: "off", url: "", tone: "" };
  }
}

/** The remote line for someone who may only read `GET /api/v2/cloud/remote`. */
export function remoteLineFromAddress(r: { remoteURL?: string | null; via?: string | null } | null): { text: string; url: string; tone: Tone } {
  const url = str(r?.remoteURL);
  return r?.via === "cloud" && url ? { text: "connected", url, tone: "green" } : { text: "off", url: "", tone: "" };
}

export function pushLine(s: StatusLike): string {
  switch (s.push?.state) {
    case "cloud":
      return "through Sentinel Cloud";
    case "direct":
      return "your own APNs key";
    default:
      return "off";
  }
}

/** Sizes as the plans are sold ("10 GB" is 10 GiB). */
export function formatGB(n: number | undefined): string {
  if (n === undefined || !Number.isFinite(n) || n < 0) return "—";
  const gb = n / 1024 ** 3;
  if (gb >= 1) {
    const r = Math.round(gb * 10) / 10;
    return `${r >= 10 || Number.isInteger(r) ? Math.round(r) : r.toFixed(1)} GB`;
  }
  if (n === 0) return "0 GB";
  const mb = n / 1024 ** 2;
  return mb >= 1 ? `${Math.round(mb)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`;
}

/** "on · 1.2 GB of 10 GB" or "off". */
export function backupLine(s: StatusLike): string {
  const on = !!s.backup?.enabled && !!s.features?.includes("backup");
  if (!on) return "off";
  const used = formatGB(s.backup?.bytesUsed ?? 0);
  const quota = s.quotas?.backupBytes ?? 0;
  return quota > 0 ? `on · ${used} of ${formatGB(quota)}` : `on · ${used} used`;
}

export type PlanState = "inactive" | "active" | "grace" | "canceled";

/** Which buttons the linked card shows: Subscribe (inactive, canceled) or Manage plan (active, grace). */
export function planState(s: StatusLike): PlanState {
  if (s.plan === "free" || !s.plan) return s.status === "canceled" ? "canceled" : "inactive";
  if (s.status === "active" || s.status === "grace" || s.status === "canceled") return s.status;
  return "inactive";
}

export const planPaid = (s: StatusLike): boolean => {
  const p = planState(s);
  return p === "active" || p === "grace";
};

/** "$4.99" / "$49". */
export function usd(n: number): string {
  return Number.isInteger(n) ? `$${n}` : `$${n.toFixed(2)}`;
}

export const priceLabel = (p: PlanOffer, interval: Interval): string => (interval === "year" ? `${usd(p.year)}/yr` : `${usd(p.month)}/mo`);

/** "1 server · 10 GB offsite backup · 30 days of alarms". */
export function planSummary(p: PlanOffer): string {
  const parts: string[] = [];
  if (p.servers) parts.push(`${p.servers} ${p.servers === 1 ? "server" : "servers"}`);
  if (p.backupGB) parts.push(`${p.backupGB} GB offsite backup`);
  if (p.days) parts.push(`${p.days} days of alarms`);
  return parts.join(" · ");
}

// ----------------------------------------------------------- checkout poll ---

export const CHECKOUT_POLL_MS = 5_000;
export const CHECKOUT_POLL_FOR_MS = 120_000;

/** True for the page Stripe returns to after paying: `#/cloud?checkout=success`. */
export function isCheckoutReturn(hash: string): boolean {
  const m = hash.match(/^#\/?cloud\?(.*)$/);
  return !!m && new URLSearchParams(m[1]).get("checkout") === "success";
}

/** The same page without the Stripe marker, so a reload doesn't poll again. */
export function withoutCheckout(hash: string): string {
  const m = hash.match(/^(#\/?cloud)\?(.*)$/);
  if (!m) return hash;
  const q = new URLSearchParams(m[2]);
  q.delete("checkout");
  const rest = q.toString();
  return rest ? `${m[1]}?${rest}` : m[1];
}

/** After a refresh: stop (the plan shows), try again in 5 s, or give up after 2 minutes. */
export function checkoutPollNext(startedAt: number, now: number, s: StatusLike | null): "done" | "wait" | "timeout" {
  if (s && planPaid(s)) return "done";
  return now - startedAt + CHECKOUT_POLL_MS > CHECKOUT_POLL_FOR_MS ? "timeout" : "wait";
}

// ----------------------------------------------------------- state machine ---

/** What Subscribe and Manage plan do once the account JWT is at hand. */
export type AccountAction = { kind: "checkout"; plan: string; interval: Interval } | { kind: "portal" };

export type Phase = "loading" | "unavailable" | "viewer" | "notLinked" | "linking" | "linked";

export interface CardState {
  phase: Phase;
  /** While linking: which request is running (the button says so). */
  step: "signIn" | "signUp" | "link" | null;
  mode: Mode;
  /** After a sign-up that needs the email confirmed: where the email went. */
  confirmEmail: string;
  /** One sentence under the button. */
  error: string;
  /** A link next to the error (the plan upgrade page on cloud_server_limit). */
  errorURL: string;
  /** Linked: Subscribe or Manage plan waiting for the inline sign-in. */
  pending: AccountAction | null;
  /** Linked: the inline sign-in or a Stripe request is running. */
  busy: boolean;
  /** Back from Stripe Checkout: refreshing until the plan shows. */
  checkout: "idle" | "waiting" | "done" | "timeout";
  /** Stripe was opened in another tab: refresh the plan when this tab is focused again. */
  watchFocus: boolean;
}

export const initialCardState: CardState = {
  phase: "loading",
  step: null,
  mode: "signIn",
  confirmEmail: "",
  error: "",
  errorURL: "",
  pending: null,
  busy: false,
  checkout: "idle",
  watchFocus: false,
};

export type CardEvent =
  | { type: "loaded"; linked: boolean }
  | { type: "loadFailed"; message: string }
  | { type: "viewer" }
  | { type: "mode"; mode: Mode }
  | { type: "invalid"; message: string }
  | { type: "submit" }
  | { type: "authed" }
  | { type: "confirmNeeded"; email: string }
  | { type: "authFailed"; message: string; reason: AuthReason }
  | { type: "linked" }
  | { type: "linkFailed"; message: string; upgradeURL?: string }
  | { type: "needSignIn"; action: AccountAction; message?: string }
  | { type: "cancelSignIn" }
  | { type: "redirecting" }
  | { type: "redirected" }
  | { type: "redirectFailed"; message: string; signIn: boolean; action: AccountAction }
  | { type: "unlinked" }
  | { type: "checkoutReturn" }
  | { type: "checkoutDone" }
  | { type: "checkoutTimeout" }
  | { type: "focusSeen"; paid: boolean };

const clean = { error: "", errorURL: "" };

export function cardReducer(s: CardState, e: CardEvent): CardState {
  switch (e.type) {
    case "loaded":
      if (e.linked) return s.phase === "linked" ? s : { ...s, ...clean, phase: "linked", step: null, confirmEmail: "", mode: "signIn" };
      if (s.phase === "linking" || s.phase === "notLinked") return s; // a background read never yanks the form
      return { ...s, phase: "notLinked", step: null, pending: null, busy: false, watchFocus: false, ...(s.phase === "linked" || s.phase === "unavailable" ? clean : {}) };
    case "loadFailed":
      return s.phase === "loading" || s.phase === "unavailable" ? { ...s, phase: "unavailable", error: e.message, errorURL: "" } : s;
    case "viewer":
      return { ...s, ...clean, phase: "viewer" };
    case "mode":
      return s.phase === "notLinked" ? { ...s, ...clean, mode: e.mode, confirmEmail: "" } : s;
    case "invalid":
      return { ...s, error: e.message, errorURL: "" };
    case "submit":
      if (s.phase !== "notLinked") return s;
      return { ...s, ...clean, phase: "linking", step: s.confirmEmail || s.mode === "signIn" ? "signIn" : "signUp" };
    case "authed":
      return s.phase === "linking" ? { ...s, step: "link" } : s;
    case "confirmNeeded":
      return { ...s, ...clean, phase: "notLinked", step: null, mode: "signIn", confirmEmail: e.email || s.confirmEmail || "your email" };
    case "authFailed":
      if (s.phase === "linked") return { ...s, busy: false, error: e.message, errorURL: "" };
      return {
        ...s,
        phase: "notLinked",
        step: null,
        error: e.message,
        errorURL: "",
        mode: e.reason === "exists" ? "signIn" : s.mode,
        confirmEmail: e.reason === "exists" ? "" : s.confirmEmail,
      };
    case "linked":
      return { ...s, ...clean, phase: "linked", step: null, mode: "signIn", confirmEmail: "", pending: null, busy: false };
    case "linkFailed":
      return { ...s, phase: "notLinked", step: null, error: e.message, errorURL: e.upgradeURL ?? "" };
    case "needSignIn":
      return s.phase === "linked" ? { ...s, pending: e.action, busy: false, error: e.message ?? "", errorURL: "" } : s;
    case "cancelSignIn":
      return { ...s, ...clean, pending: null, busy: false };
    case "redirecting":
      return { ...s, ...clean, busy: true };
    case "redirected":
      return { ...s, ...clean, busy: false, pending: null, watchFocus: true };
    case "redirectFailed":
      return { ...s, busy: false, error: e.message, errorURL: "", pending: e.signIn ? e.action : null };
    case "unlinked":
      return { ...initialCardState, phase: "notLinked" };
    case "checkoutReturn":
      return { ...s, checkout: "waiting" };
    case "checkoutDone":
      return { ...s, checkout: "done", watchFocus: false };
    case "checkoutTimeout":
      return { ...s, checkout: "timeout" };
    case "focusSeen":
      return e.paid ? { ...s, watchFocus: false } : s;
  }
}

/** The big button's label. */
export function turnOnLabel(s: CardState): string {
  if (s.phase === "linking") return s.step === "signUp" ? "Creating your account…" : s.step === "link" ? "Turning on…" : "Signing in…";
  return s.confirmEmail ? "I confirmed it, sign me in" : "Turn on Sentinel Cloud";
}
