// Typed client for the Sentinel API v2 (linux/api/openapi.yaml), shared by
// Sentinel Admin (browser, cookie session) and Sentinel Operator for Linux
// (Electron, bearer tokens). Types come from the generated src/api/v2.d.ts;
// regenerate with `npm run gen:api`, never edit it by hand.
//
//   const api = createClient();                                   // browser: same-origin cookie session
//   const api = createClient({ baseUrl: "https://ms.lan:8090",   // Operator: bearer tokens
//                              tokens: new MemoryTokenStore() });
//   await api.signIn("ana", "secret");
//   const cams = await api.cameras.list();
//   const page = await api.call("get", "/api/v2/alarms", { query: { state: "all" } });

import type { components, paths } from "../api/v2";

// ------------------------------------------------------------------ types ---

export type Schemas = components["schemas"];
export type Camera = Schemas["Camera"];
export type CameraInput = Schemas["CameraInput"];
export type CameraPatch = Schemas["CameraPatch"];
export type CameraGroup = Schemas["CameraGroup"];
export type CameraGroupInput = Schemas["CameraGroupInput"];
export type RecordingServer = Schemas["RecordingServer"];
export type EnrollmentToken = Schemas["EnrollmentToken"];
export type User = Schemas["User"];
export type UserScope = Schemas["UserScope"];
export type Me = Schemas["Me"];
export type Role = Schemas["Role"];
export type Permission = Schemas["Permission"];
export type Rule = Schemas["Rule"];
export type RuleInput = Schemas["RuleInput"];
export type Alarm = Schemas["Alarm"];
export type AlarmState = Schemas["AlarmState"];
export type Event = Schemas["Event"];
export type Evidence = Schemas["Evidence"];
export type Timeline = Schemas["Timeline"];
export type Span = Schemas["Span"];
export type Segment = Schemas["Segment"];
export type AuditEntry = Schemas["AuditEntry"];
export type AuditVerify = Schemas["AuditVerify"];
export type VideoToken = Schemas["VideoToken"];
export type VideoRight = Schemas["VideoRight"];
export type TokenResponse = Schemas["TokenResponse"];
export type DirectorySettings = Schemas["DirectorySettings"];
export type LDAPSettings = Schemas["LDAPSettings"];
export type OIDCSettings = Schemas["OIDCSettings"];
export type RoleMapping = Schemas["RoleMapping"];
export type AuthProviders = Schemas["AuthProviders"];
export type View = Schemas["View"];
export type ViewInput = Schemas["ViewInput"];
export type AlertOn = Schemas["AlertOn"];

export const roles: Role[] = ["Admin", "Supervisor", "Operator", "Viewer"];

/** A page of a paged list (`{items, nextCursor}`). */
export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

// ----------------------------------------------------------------- errors ---

/** Stable machine codes from the v2 error shape, plus `network` (no response). */
export type ErrorCode =
  | "bad_request"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "too_many_requests"
  | "unavailable"
  | "timeout"
  | "token_reused"
  | "internal"
  | "network"
  | (string & {});

/** Any failed v2 call. `message` is safe to show to a person. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;
  constructor(status: number, code: ErrorCode, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

const codeForStatus: Record<number, ErrorCode> = {
  400: "bad_request",
  401: "unauthorized",
  403: "forbidden",
  404: "not_found",
  409: "conflict",
  429: "too_many_requests",
  503: "unavailable",
  504: "timeout",
};

const messageForCode: Record<string, string> = {
  bad_request: "The request was not valid.",
  unauthorized: "You're signed out. Sign in again.",
  forbidden: "You don't have permission to do that.",
  not_found: "Not found. It may have been removed.",
  conflict: "That conflicts with the current state. Reload and try again.",
  too_many_requests: "Too many requests. Wait a moment and try again.",
  unavailable: "The recording server isn't reachable right now.",
  timeout: "The server took too long to answer.",
  internal: "Something went wrong on the server.",
};

/**
 * Turn a failed response into an ApiError. Understands the v2 shape
 * `{"error":{"code","message"}}`, the v1 shape `{"error":"…"}` and
 * anything else (proxies, HTML error pages) by the HTTP status.
 */
export function errorFromResponse(status: number, bodyText: string): ApiError {
  let code: ErrorCode = codeForStatus[status] ?? (status >= 500 ? "internal" : "bad_request");
  let message = "";
  try {
    const data = JSON.parse(bodyText) as unknown;
    const err = (data as { error?: unknown } | null)?.error;
    if (err && typeof err === "object") {
      const e = err as { code?: unknown; message?: unknown };
      if (typeof e.code === "string" && e.code) code = e.code;
      if (typeof e.message === "string") message = e.message;
    } else if (typeof err === "string") {
      message = err;
    }
  } catch {
    /* not JSON */
  }
  return new ApiError(status, code, message || messageForCode[code] || `Request failed (${status})`);
}

/** Message for any thrown value, for an error banner. */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e);
}

// ----------------------------------------------------------------- tokens ---

export interface TokenPair {
  accessToken: string;
  /** Unix ms */
  expiresAt: number;
  refreshToken: string;
  /** Unix ms */
  refreshExpiresAt: number;
}

/** Where a bearer-token client keeps its sign-in. Persist it however suits the app. */
export interface TokenStore {
  get(): TokenPair | null;
  set(tokens: TokenPair | null): void;
  /**
   * Optional, for a sign-in shared by several windows (Sentinel Operator's
   * pop-outs): re-read the pair from where it is kept before a refresh…
   */
  reload?(): Promise<void>;
  /** …and run the refresh exclusively across them, so a rotated refresh token is never presented twice. */
  exclusive?<T>(fn: () => Promise<T>): Promise<T>;
}

export class MemoryTokenStore implements TokenStore {
  private tokens: TokenPair | null;
  constructor(initial: TokenPair | null = null) {
    this.tokens = initial;
  }
  get() {
    return this.tokens;
  }
  set(t: TokenPair | null) {
    this.tokens = t;
  }
}

/** Refresh the access token this long before it expires. */
export const refreshMarginMs = 60_000;

/**
 * Coalesces refreshes: refresh tokens rotate, and presenting one that was
 * already used revokes the whole sign-in, so two concurrent refreshes must
 * share one request.
 */
export class TokenRefresher {
  private inflight: Promise<TokenPair> | null = null;
  constructor(
    private readonly store: TokenStore,
    private readonly exchange: (refreshToken: string) => Promise<TokenPair>,
    private readonly now: () => number = Date.now,
  ) {}

  /** True when the access token is missing or expires within the margin. */
  needsRefresh(): boolean {
    const t = this.store.get();
    return !!t && t.expiresAt - this.now() < refreshMarginMs;
  }

  /** A fresh pair; one network call however many callers ask at once. */
  refresh(): Promise<TokenPair> {
    if (this.inflight) return this.inflight;
    const seen = this.store.get()?.refreshToken;
    const run = async (): Promise<TokenPair> => {
      if (this.store.reload) await this.store.reload();
      const current = this.store.get();
      if (!current) throw new ApiError(401, "unauthorized", messageForCode.unauthorized);
      // Another window sharing the store rotated it while this one waited: use that pair.
      if (seen !== undefined && current.refreshToken !== seen && current.expiresAt - this.now() >= refreshMarginMs) return current;
      if (current.refreshExpiresAt <= this.now()) {
        this.store.set(null);
        throw new ApiError(401, "unauthorized", "Your sign-in has expired. Sign in again.");
      }
      try {
        const pair = await this.exchange(current.refreshToken);
        this.store.set(pair);
        return pair;
      } catch (e) {
        // A definite "no" ends the sign-in; a network blip keeps it for a retry.
        if (e instanceof ApiError && (e.status === 400 || e.status === 401)) this.store.set(null);
        throw e;
      }
    };
    this.inflight = (this.store.exclusive ? this.store.exclusive(run) : run()).finally(() => {
      this.inflight = null;
    });
    return this.inflight;
  }

  /** A usable access token, refreshing first when it's about to expire. */
  async accessToken(): Promise<string | null> {
    const t = this.store.get();
    if (!t) return null;
    if (this.needsRefresh()) return (await this.refresh()).accessToken;
    return t.accessToken;
  }
}

// The generated grant types carry the schema name as `grantType` (the
// discriminator in openapi.yaml has no `mapping`), so the wire shapes are
// spelled out here.
type Grant =
  | { grantType: "password"; name: string; password: string; client?: string }
  | { grantType: "refresh"; refreshToken: string }
  | { grantType: "oidc_code"; code: string; client?: string };

function pairOf(r: TokenResponse): TokenPair {
  return { accessToken: r.accessToken, expiresAt: r.expiresAt, refreshToken: r.refreshToken, refreshExpiresAt: r.refreshExpiresAt };
}

// ------------------------------------------------------- typed operations ---

export type Method = "get" | "post" | "put" | "patch" | "delete";

/** Paths that have an operation for `M`. */
export type PathsWith<M extends Method> = {
  [P in keyof paths]: undefined extends paths[P][M] ? never : P;
}[keyof paths];

type Op<P extends keyof paths, M extends Method> = NonNullable<paths[P][M]>;

type JsonOf<R> = R extends { content: { "application/json": infer B } } ? B : void;

/** The success body of an operation (200 or 201 JSON; void for 204). */
export type ResponseOf<P extends keyof paths, M extends Method> =
  Op<P, M> extends { responses: infer R }
    ? R extends { 200: infer A }
      ? JsonOf<A>
      : R extends { 201: infer A }
        ? JsonOf<A>
        : void
    : never;

type BodyOf<O> = O extends { requestBody: { content: { "application/json": infer B } } }
  ? { body: B }
  : O extends { requestBody?: { content: { "application/json": infer B } } }
    ? { body?: B }
    : { body?: undefined };

type ParamsOf<O> = O extends { parameters: infer Prm }
  ? (Prm extends { path: infer PP } ? { path: PP } : { path?: undefined }) &
      (Prm extends { query?: infer Q } ? ([Q] extends [never] | [undefined] ? { query?: undefined } : { query?: Q }) : { query?: undefined })
  : { path?: undefined; query?: undefined };

/** Options for `call`: path parameters, query and JSON body, typed per operation. */
export type CallOptions<P extends keyof paths, M extends Method> = ParamsOf<Op<P, M>> & BodyOf<Op<P, M>> & { signal?: AbortSignal };

type OptionalArg<T> = Partial<T> extends T ? [opts?: T] : [opts: T];

// ----------------------------------------------------------------- client ---

export interface ClientOptions {
  /** Management server origin, e.g. "https://ms.lan:8090". Default "": this page's origin. */
  baseUrl?: string;
  /** Bearer-token mode (Electron Operator). Without it, the browser's session cookie is used. */
  tokens?: TokenStore;
  /** Label for the audit log when signing in, e.g. "Sentinel Operator (Linux)". */
  clientName?: string;
  /** Called when the sign-in is gone (401 that a refresh can't fix). */
  onUnauthorized?: () => void;
  /** Injected for tests. */
  fetch?: typeof fetch;
  now?: () => number;
}

function buildPath(template: string, params: Record<string, unknown> | undefined): string {
  return template.replace(/\{(\w+)\}/g, (_, k: string) => {
    const v = params?.[k];
    if (v === undefined || v === null || v === "") throw new Error(`missing path parameter ${k}`);
    return encodeURIComponent(String(v));
  });
}

function buildQuery(q: Record<string, unknown> | undefined): string {
  if (!q) return "";
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null) continue;
    sp.set(k, Array.isArray(v) ? v.join(",") : String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}

export type ApiClient = ReturnType<typeof createClient>;

export function createClient(options: ClientOptions = {}) {
  const baseUrl = (options.baseUrl ?? "").replace(/\/+$/, "");
  const doFetch = options.fetch ?? ((input: RequestInfo | URL, init?: RequestInit) => fetch(input, init));
  const tokens = options.tokens;
  let onUnauthorized = options.onUnauthorized ?? (() => {});

  async function raw(method: Method, url: string, body: unknown, auth: string | null, signal?: AbortSignal): Promise<Response> {
    const headers: Record<string, string> = { Accept: "application/json" };
    // State-changing cookie requests must send JSON (CSRF rule), even without a body.
    if (body !== undefined || method !== "get") headers["Content-Type"] = "application/json";
    if (auth) headers.Authorization = `Bearer ${auth}`;
    try {
      return await doFetch(baseUrl + url, {
        method: method.toUpperCase(),
        headers,
        credentials: tokens ? "omit" : "same-origin",
        body: body === undefined ? undefined : JSON.stringify(body),
        signal,
      });
    } catch (e) {
      if ((e as Error)?.name === "AbortError") throw e;
      throw new ApiError(0, "network", "Can't reach the server. Check the connection.");
    }
  }

  async function parse<T>(res: Response): Promise<T> {
    const text = await res.text();
    if (!res.ok) throw errorFromResponse(res.status, text);
    if (!text) return undefined as T;
    const type = res.headers.get("Content-Type") ?? "";
    return (type.includes("json") || /^[[{]/.test(text) ? JSON.parse(text) : text) as T;
  }

  async function exchange(grant: Grant): Promise<TokenResponse> {
    const res = await raw("post", "/api/v2/auth/token", grant, null);
    return parse<TokenResponse>(res);
  }

  const refresher = tokens ? new TokenRefresher(tokens, async (rt) => pairOf(await exchange({ grantType: "refresh", refreshToken: rt })), options.now) : null;

  /** One request with auth, refresh-and-retry once on 401 in token mode. */
  async function send<T>(method: Method, url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    return parse<T>(await authed(method, url, body, signal));
  }

  /** A GET whose body is plain text, never parsed as JSON (logs). */
  async function sendText(url: string, signal?: AbortSignal): Promise<string> {
    const res = await authed("get", url, undefined, signal);
    const text = await res.text();
    if (!res.ok) throw errorFromResponse(res.status, text);
    return text;
  }

  async function authed(method: Method, url: string, body: unknown, signal?: AbortSignal): Promise<Response> {
    let auth = refresher ? await refresher.accessToken().catch(() => null) : null;
    let res = await raw(method, url, body, auth, signal);
    if (res.status === 401 && refresher && tokens?.get()) {
      try {
        auth = (await refresher.refresh()).accessToken;
        res = await raw(method, url, body, auth, signal);
      } catch {
        /* fall through with the 401 */
      }
    }
    if (res.status === 401) {
      tokens?.set(null);
      onUnauthorized();
    }
    return res;
  }

  /** Call any operation in the contract, fully typed. */
  function call<P extends PathsWith<M>, M extends Method>(method: M, path: P, ...args: OptionalArg<CallOptions<P, M>>): Promise<ResponseOf<P, M>> {
    const opts = (args[0] ?? {}) as { path?: Record<string, unknown>; query?: Record<string, unknown>; body?: unknown; signal?: AbortSignal };
    return send(method, buildPath(path as string, opts.path) + buildQuery(opts.query), opts.body, opts.signal);
  }

  return {
    baseUrl,
    call,
    /** Untyped escape hatch (text responses, routes not in the contract yet). */
    request: send,
    /**
     * A binary resource on the management server (an event thumbnail) with
     * the client's auth, for token mode where an <img src> can't carry it.
     */
    async blob(url: string, signal?: AbortSignal): Promise<Blob> {
      const auth = refresher ? await refresher.accessToken().catch(() => null) : null;
      let res: Response;
      try {
        res = await doFetch(baseUrl + url, {
          headers: auth ? { Authorization: `Bearer ${auth}` } : {},
          credentials: tokens ? "omit" : "same-origin",
          signal,
        });
      } catch (e) {
        if ((e as Error)?.name === "AbortError") throw e;
        throw new ApiError(0, "network", "Can't reach the server. Check the connection.");
      }
      if (!res.ok) throw errorFromResponse(res.status, await res.text());
      return res.blob();
    },
    /** Replace the 401 handler (the app wires it to its sign-in screen). */
    setUnauthorizedHandler(fn: () => void) {
      onUnauthorized = fn;
    },
    /** Token mode: the current access token (refreshed when near expiry), for the event stream. */
    accessToken: (): Promise<string | null> => (refresher ? refresher.accessToken() : Promise.resolve(null)),
    /** Token mode: force a refresh (e.g. the event stream closed with 4401). */
    refreshTokens: (): Promise<TokenPair | null> => (refresher ? refresher.refresh() : Promise.resolve(null)),

    async signIn(name: string, password: string): Promise<User> {
      const r = await exchange({ grantType: "password", name, password, client: options.clientName });
      tokens?.set(pairOf(r));
      return r.user;
    },
    async signInWithOidcCode(code: string): Promise<User> {
      const r = await exchange({ grantType: "oidc_code", code, client: options.clientName });
      tokens?.set(pairOf(r));
      return r.user;
    },
    async signOut(): Promise<void> {
      const t = tokens?.get();
      tokens?.set(null);
      if (t) await raw("post", "/api/v2/auth/revoke", { refreshToken: t.refreshToken }, null).catch(() => {});
    },

    me: () => call("get", "/api/v2/me"),
    providers: () => call("get", "/api/v2/auth/providers"),

    cameras: {
      list: async () => (await call("get", "/api/v2/cameras")).items,
      get: (cameraId: string) => call("get", "/api/v2/cameras/{cameraId}", { path: { cameraId } }),
      create: (body: CameraInput) => call("post", "/api/v2/cameras", { body }),
      update: (cameraId: string, body: CameraPatch) => call("patch", "/api/v2/cameras/{cameraId}", { path: { cameraId }, body }),
      remove: (cameraId: string) => call("delete", "/api/v2/cameras/{cameraId}", { path: { cameraId } }),
      timeline: (cameraId: string, from: number, to: number) => call("get", "/api/v2/cameras/{cameraId}/timeline", { path: { cameraId }, query: { from, to } }),
      recordings: async (cameraId: string) => (await call("get", "/api/v2/cameras/{cameraId}/recordings", { path: { cameraId } })).items,
      videoToken: (cameraId: string, rights: VideoRight[] = ["live"], recorderId?: string) =>
        call("post", "/api/v2/cameras/{cameraId}/video-token", { path: { cameraId }, body: { rights, recorderId } }),
    },
    groups: {
      list: async () => (await call("get", "/api/v2/camera-groups")).items,
      create: (body: CameraGroupInput) => call("post", "/api/v2/camera-groups", { body }),
      update: (groupId: string, body: CameraGroupInput) => call("put", "/api/v2/camera-groups/{groupId}", { path: { groupId }, body }),
      remove: (groupId: string) => call("delete", "/api/v2/camera-groups/{groupId}", { path: { groupId } }),
    },
    servers: {
      list: async () => (await call("get", "/api/v2/recording-servers")).items,
      get: (serverId: string) => call("get", "/api/v2/recording-servers/{serverId}", { path: { serverId } }),
      update: (serverId: string, body: { name?: string; publicUrl?: string }) => call("patch", "/api/v2/recording-servers/{serverId}", { path: { serverId }, body }),
      revoke: (serverId: string) => call("delete", "/api/v2/recording-servers/{serverId}", { path: { serverId } }),
      enroll: (name?: string) => call("post", "/api/v2/recording-servers/enrollment-tokens", { body: name ? { name } : {} }),
      /** Plain-text log (secrets stripped), oldest line first; lines 1…2000. */
      log: (serverId: string, lines = 300, signal?: AbortSignal) =>
        sendText(`/api/v2/recording-servers/${encodeURIComponent(serverId)}/log${buildQuery({ lines })}`, signal),
    },
    users: {
      list: async () => (await call("get", "/api/v2/users")).items,
      create: (body: { name: string; role: Role; password: string; scope?: UserScope }) => call("post", "/api/v2/users", { body }),
      update: (userId: string, body: { role?: Role; password?: string; scope?: UserScope }) => call("patch", "/api/v2/users/{userId}", { path: { userId }, body }),
      remove: (userId: string) => call("delete", "/api/v2/users/{userId}", { path: { userId } }),
    },
    rules: {
      list: async () => (await call("get", "/api/v2/rules")).items,
      create: (body: RuleInput) => call("post", "/api/v2/rules", { body }),
      update: (ruleId: string, body: RuleInput) => call("put", "/api/v2/rules/{ruleId}", { path: { ruleId }, body: ruleInputOf(body) }),
      remove: (ruleId: string) => call("delete", "/api/v2/rules/{ruleId}", { path: { ruleId } }),
    },
    alarms: {
      page: (q: { state?: "open" | "all"; cameraId?: string; cursor?: string; limit?: number } = {}) => call("get", "/api/v2/alarms", { query: q }),
      get: (alarmId: string) => call("get", "/api/v2/alarms/{alarmId}", { path: { alarmId } }),
      setState: (alarmId: string, state: AlarmState, note?: string, snoozeMinutes?: number) =>
        call("post", "/api/v2/alarms/{alarmId}/state", { path: { alarmId }, body: { state, note, snoozeMinutes } }),
      lockEvidence: (alarmId: string) => call("post", "/api/v2/alarms/{alarmId}/evidence", { path: { alarmId } }),
    },
    events: {
      page: (q: { cameraId?: string; cursor?: string; limit?: number } = {}) => call("get", "/api/v2/events", { query: q }),
      get: (eventId: string) => call("get", "/api/v2/events/{eventId}", { path: { eventId } }),
      /** For <img src>: cookie mode only (an <img> can't send a bearer token). */
      thumbnailUrl: (eventId: string) => `${baseUrl}/api/v2/events/${encodeURIComponent(eventId)}/thumbnail.jpg`,
    },
    audit: {
      page: (q: { serverId?: string; cursor?: string; limit?: number } = {}) => call("get", "/api/v2/audit", { query: q }),
      verify: () => call("get", "/api/v2/audit/verify"),
    },
    directory: {
      get: () => call("get", "/api/v2/directory"),
      save: (body: DirectorySettings) => call("put", "/api/v2/directory", { body: directoryForSave(body) }),
      testLdap: (settings: LDAPSettings, name: string, password: string) =>
        call("post", "/api/v2/directory/test", { body: { settings: ldapForSave(settings), name, password } }),
    },
    views: {
      list: async () => (await call("get", "/api/v2/views")).items,
      create: (body: ViewInput) => call("post", "/api/v2/views", { body }),
      update: (viewId: string, body: ViewInput) => call("put", "/api/v2/views/{viewId}", { path: { viewId }, body }),
      remove: (viewId: string) => call("delete", "/api/v2/views/{viewId}", { path: { viewId } }),
    },
  };
}

/** Strip the read-only fields a Rule carries (RuleInput rejects unknown fields). */
export function ruleInputOf(r: RuleInput | Rule): RuleInput {
  const { name, enabled, cameraIds, kinds, minSeverity, schedule, fromMinute, toMinute, days, delivery, instructions } = r;
  return { name, enabled, cameraIds, kinds, minSeverity, schedule, fromMinute, toMinute, days, delivery, instructions };
}

/** LDAP settings as sent: read-only `hasBindPassword` dropped; an empty secret means "keep". */
export function ldapForSave(s: LDAPSettings): LDAPSettings {
  const { hasBindPassword: _h, bindPassword, ...rest } = s;
  return bindPassword ? { ...rest, bindPassword } : rest;
}

export function oidcForSave(s: OIDCSettings): OIDCSettings {
  const { hasClientSecret: _h, clientSecret, ...rest } = s;
  return clientSecret ? { ...rest, clientSecret } : rest;
}

export function directoryForSave(d: DirectorySettings): DirectorySettings {
  return { ldap: ldapForSave(d.ldap), oidc: oidcForSave(d.oidc) };
}

// ----------------------------------------------------------------- paging ---

/** Walk a cursor-paged list page by page. */
export async function* pages<T>(fetchPage: (cursor: string | undefined) => Promise<Page<T>>, maxPages = Infinity): AsyncGenerator<T[], void> {
  let cursor: string | undefined;
  for (let n = 0; n < maxPages; n++) {
    const p = await fetchPage(cursor);
    yield p.items;
    if (!p.nextCursor || p.nextCursor === cursor) return;
    cursor = p.nextCursor;
  }
}

/** Every item of a paged list, up to `max` items. */
export async function fetchAll<T>(fetchPage: (cursor: string | undefined) => Promise<Page<T>>, max = 10_000): Promise<T[]> {
  const out: T[] = [];
  for await (const items of pages(fetchPage)) {
    out.push(...items);
    if (out.length >= max) return out.slice(0, max);
  }
  return out;
}

/** Permissions a role has (mirrors the server's role table; prefer `Me.permissions` when you have it). */
export function permissionsOf(role: Role): Permission[] {
  switch (role) {
    case "Admin":
      return ["manageCameras", "manageUsers", "changeSettings", "viewAudit", "acknowledgeAlarms", "exportEvidence", "viewLive"];
    case "Supervisor":
      return ["viewAudit", "acknowledgeAlarms", "exportEvidence", "viewLive"];
    case "Operator":
      return ["acknowledgeAlarms", "exportEvidence", "viewLive"];
    default:
      return ["viewLive"];
  }
}
