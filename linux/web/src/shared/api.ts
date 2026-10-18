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
export type Floorplan = Schemas["Floorplan"];
export type FloorplanPin = Schemas["FloorplanPin"];
export type RecordingServer = Schemas["RecordingServer"];
export type EnrollmentToken = Schemas["EnrollmentToken"];
export type ServerHealth = Schemas["ServerHealth"];
export type CameraHealth = Schemas["CameraHealth"];
export type User = Schemas["User"];
export type UserScope = Schemas["UserScope"];
export type Me = Schemas["Me"];
export type Role = Schemas["Role"];
export type Permission = Schemas["Permission"];
export type RoleDefinition = Schemas["RoleDefinition"];
export type RoleInput = Schemas["RoleInput"];
export type PermissionInfo = Schemas["PermissionInfo"];
export type Rule = Schemas["Rule"];
export type RuleInput = Schemas["RuleInput"];
export type Alarm = Schemas["Alarm"];
export type AlarmState = Schemas["AlarmState"];
export type Event = Schemas["Event"];
export type Evidence = Schemas["Evidence"];
export type EvidenceCase = Schemas["EvidenceCase"];
export type ShareLink = Schemas["ShareLink"];
export type ExportJob = Schemas["ExportJob"];
export type ExportRegion = Schemas["ExportRegion"];
export type ExportSchedule = Schemas["ExportSchedule"];
export interface ExportScheduleInput {
  name: string;
  cameraId: string;
  days: number[];
  start: string;
  end: string;
  enabled?: boolean;
}
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
export type ViewGroup = Schemas["ViewGroup"];
export type ViewGroupInput = Schemas["ViewGroupInput"];
export type AlertOn = Schemas["AlertOn"];
export type DiscoveredDevice = Schemas["DiscoveredDevice"];
export type DiscoverResult = Schemas["DiscoverResult"];
export type OnvifProfile = Schemas["OnvifProfile"];
export type ProbeOutcome = Schemas["ProbeOutcome"];
export type PTZState = Schemas["PTZState"];
export type PTZPreset = Schemas["PTZPreset"];
export type Bookmark = Schemas["Bookmark"];
export type BookmarkInput = Schemas["BookmarkInput"];
export type BookmarkPatch = Schemas["BookmarkPatch"];
export type BookmarkColor = Schemas["BookmarkColor"];
export type CameraZone = Schemas["CameraZone"];
export type CameraZoneInput = Schemas["CameraZoneInput"];

export type EventSearchFilter = Schemas["EventSearchFilter"];
export type EventFacets = Schemas["EventFacets"];
export type EventSearchQuery = NonNullable<paths["/api/v2/events/search"]["get"]["parameters"]["query"]>;
// Appearance search (WAVE9(A))
export type SimilarEventsRequest = Schemas["SimilarEventsRequest"];
export type SimilarEvents = Schemas["SimilarEvents"];
export type SimilarEventHit = Schemas["SimilarEventHit"];
export type AppearanceStatus = Schemas["AppearanceStatus"];
export type AppearanceBackfill = Schemas["AppearanceBackfill"];

export type AlarmQuery = NonNullable<paths["/api/v2/alarms"]["get"]["parameters"]["query"]>;
export type Incident = Schemas["Incident"];
export type IncidentItem = Schemas["IncidentItem"];
export type IncidentStatus = Schemas["IncidentStatus"];
export type IncidentSeverity = Schemas["IncidentSeverity"];
export type EdgeRecovery = Schemas["EdgeRecovery"]; // WAVE9(C)

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
    const form = typeof FormData !== "undefined" && body instanceof FormData;
    if (form) headers["X-Sentinel-Upload"] = "1"; // multipart: the browser sets Content-Type; this header lets a cookie session past the CSRF guard
    else if (body !== undefined || method !== "get") headers["Content-Type"] = "application/json";
    if (auth) headers.Authorization = `Bearer ${auth}`;
    try {
      return await doFetch(baseUrl + url, {
        method: method.toUpperCase(),
        headers,
        credentials: tokens ? "omit" : "same-origin",
        body: body === undefined ? undefined : form ? (body as FormData) : JSON.stringify(body),
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
  type Call = <P extends PathsWith<M>, M extends Method>(method: M, path: P, ...args: OptionalArg<CallOptions<P, M>>) => Promise<ResponseOf<P, M>>;
  // The contract has grown past what TypeScript will check inside a generic
  // body, so the implementation is untyped and only its signature is typed.
  const call = ((method: Method, path: string, opts: { path?: Record<string, unknown>; query?: Record<string, unknown>; body?: unknown; signal?: AbortSignal } = {}) =>
    send(method, buildPath(path, opts.path) + buildQuery(opts.query), opts.body, opts.signal)) as Call;

  async function fetchBlob(url: string, signal?: AbortSignal): Promise<Blob> {
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
    blob: fetchBlob,
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
    ptz: {
      state: (cameraId: string) => call("get", "/api/v2/cameras/{cameraId}/ptz", { path: { cameraId } }),
      /** Continuous move; velocities -1..1 (the server clamps). Follow with `stop`. */
      move: (cameraId: string, pan: number, tilt: number, zoom: number) =>
        call("post", "/api/v2/cameras/{cameraId}/ptz/move", { path: { cameraId }, body: { pan, tilt, zoom } }),
      stop: (cameraId: string) => call("post", "/api/v2/cameras/{cameraId}/ptz/stop", { path: { cameraId } }),
      savePreset: (cameraId: string, name: string) => call("post", "/api/v2/cameras/{cameraId}/ptz/presets", { path: { cameraId }, body: { name } }),
      gotoPreset: (cameraId: string, token: string) =>
        call("post", "/api/v2/cameras/{cameraId}/ptz/presets/{token}/goto", { path: { cameraId, token } }),
      removePreset: (cameraId: string, token: string) =>
        call("delete", "/api/v2/cameras/{cameraId}/ptz/presets/{token}", { path: { cameraId, token } }),
    },
    zones: {
      list: async (cameraId: string) => (await call("get", "/api/v2/cameras/{cameraId}/zones", { path: { cameraId } })).items,
      /** Replaces the camera's whole set of detection zones and privacy masks. */
      put: async (cameraId: string, zones: CameraZoneInput[]) => (await call("put", "/api/v2/cameras/{cameraId}/zones", { path: { cameraId }, body: { zones } })).items,
    },
    groups: {
      list: async () => (await call("get", "/api/v2/camera-groups")).items,
      create: (body: CameraGroupInput) => call("post", "/api/v2/camera-groups", { body }),
      update: (groupId: string, body: CameraGroupInput) => call("put", "/api/v2/camera-groups/{groupId}", { path: { groupId }, body }),
      remove: (groupId: string) => call("delete", "/api/v2/camera-groups/{groupId}", { path: { groupId } }),
    },
    floorplans: {
      list: async () => (await call("get", "/api/v2/floorplans")).items,
      /** Upload a PNG, JPEG or WebP (up to 8 MB). */
      create: (name: string, image: Blob) => {
        const form = new FormData();
        form.set("name", name);
        form.set("image", image);
        return send<Floorplan>("post", "/api/v2/floorplans", form);
      },
      update: (planId: string, body: { name?: string; sortOrder?: number }) => call("put", "/api/v2/floorplans/{planId}", { path: { planId }, body }),
      setPins: (planId: string, body: FloorplanPin[]) => call("put", "/api/v2/floorplans/{planId}/pins", { path: { planId }, body }),
      remove: (planId: string) => call("delete", "/api/v2/floorplans/{planId}", { path: { planId } }),
      /** The image with the client's auth (an <img src> can't send a bearer token). */
      image: (planId: string, signal?: AbortSignal) => fetchBlob(`/api/v2/floorplans/${encodeURIComponent(planId)}/image`, signal),
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
      /** Scan that server's LAN for cameras; sweep TCP-checks its whole subnet. */
      discover: (serverId: string, sweep = false) => call("post", "/api/v2/recording-servers/{serverId}/discover", { path: { serverId }, body: { sweep } }),
      /** Sign in to one camera over ONVIF from that server; camera failures come back inside the outcome. */
      probe: (serverId: string, body: { host: string; serviceURL?: string; username?: string; password?: string }) =>
        call("post", "/api/v2/recording-servers/{serverId}/discover/probe", { path: { serverId }, body }),
    },
    system: {
      /** System Monitor feed: every recording server and its cameras (camera rows follow the caller's scope). */
      health: async () => (await call("get", "/api/v2/system/health")).servers,
    },
    users: {
      list: async () => (await call("get", "/api/v2/users")).items,
      create: (body: { name: string; role: Role; password: string; scope?: UserScope }) => call("post", "/api/v2/users", { body }),
      update: (userId: string, body: { role?: Role; password?: string; scope?: UserScope }) => call("patch", "/api/v2/users/{userId}", { path: { userId }, body }),
      remove: (userId: string) => call("delete", "/api/v2/users/{userId}", { path: { userId } }),
    },
    roles: {
      list: async () => (await call("get", "/api/v2/roles")).items,
      create: (body: RoleInput) => call("post", "/api/v2/roles", { body }),
      update: (roleId: string, body: RoleInput) => call("put", "/api/v2/roles/{roleId}", { path: { roleId }, body }),
      remove: (roleId: string) => call("delete", "/api/v2/roles/{roleId}", { path: { roleId } }),
      permissions: async () => (await call("get", "/api/v2/permissions")).items,
    },
    rules: {
      list: async () => (await call("get", "/api/v2/rules")).items,
      create: (body: RuleInput) => call("post", "/api/v2/rules", { body }),
      update: (ruleId: string, body: RuleInput) => call("put", "/api/v2/rules/{ruleId}", { path: { ruleId }, body: ruleInputOf(body) }),
      remove: (ruleId: string) => call("delete", "/api/v2/rules/{ruleId}", { path: { ruleId } }),
    },
    alarms: {
      page: (q: AlarmQuery = {}) => call("get", "/api/v2/alarms", { query: q }),
      /** Alarm Manager: per-state counts under the other filters. */
      counts: (q: { severities?: string; cameras?: string; owner?: string; from?: number; to?: number; q?: string } = {}) => call("get", "/api/v2/alarms/counts", { query: q }),
      events: async (alarmId: string) => (await call("get", "/api/v2/alarms/{alarmId}/events", { path: { alarmId } })).items,
      assign: (alarmId: string, owner: string) => call("post", "/api/v2/alarms/{alarmId}/assign", { path: { alarmId }, body: { owner } }),
      setSeverity: (alarmId: string, severity: "Critical" | "Warning" | "Info") => call("post", "/api/v2/alarms/{alarmId}/severity", { path: { alarmId }, body: { severity } }),
      /** AI triage (409 when AI is off, 429 at the daily limit). Advice only. */
      triage: (alarmId: string) => call("post", "/api/v2/alarms/{alarmId}/triage", { path: { alarmId } }),
      get: (alarmId: string) => call("get", "/api/v2/alarms/{alarmId}", { path: { alarmId } }),
      setState: (alarmId: string, state: AlarmState, note?: string, snoozeMinutes?: number) =>
        call("post", "/api/v2/alarms/{alarmId}/state", { path: { alarmId }, body: { state, note, snoozeMinutes } }),
      lockEvidence: (alarmId: string) => call("post", "/api/v2/alarms/{alarmId}/evidence", { path: { alarmId } }),
    },
    incidents: {
      page: (q: { status?: IncidentStatus; q?: string; limit?: number; before?: string } = {}) => call("get", "/api/v2/incidents", { query: q }),
      get: (id: string) => call("get", "/api/v2/incidents/{id}", { path: { id } }),
      create: (title: string, severity?: IncidentSeverity) => call("post", "/api/v2/incidents", { body: { title, severity } }),
      update: (id: string, body: { title?: string; status?: IncidentStatus; severity?: IncidentSeverity; summary?: string }) => call("patch", "/api/v2/incidents/{id}", { path: { id }, body }),
      addItem: (id: string, body: { kind: "alarm" | "event" | "evidence" | "note"; refId?: string; note?: string }) => call("post", "/api/v2/incidents/{id}/items", { path: { id }, body }),
      removeItem: (id: string, itemId: string) => call("delete", "/api/v2/incidents/{id}/items/{itemId}", { path: { id, itemId } }),
      /** AI-drafted markdown report, stored in `summary` (409 when AI is off). */
      report: (id: string) => call("post", "/api/v2/incidents/{id}/report", { path: { id } }),
    },
    events: {
      page: (q: { cameraId?: string; cursor?: string; limit?: number } = {}) => call("get", "/api/v2/events", { query: q }),
      get: (eventId: string) => call("get", "/api/v2/events/{eventId}", { path: { eventId } }),
      /** Forensic search (list filters are comma-separated; times are Unix ms). */
      search: (q: EventSearchQuery = {}) => call("get", "/api/v2/events/search", { query: q }),
      facets: (q: { cameras?: string; from?: number; to?: number } = {}) => call("get", "/api/v2/events/facets", { query: q }),
      /** Natural-language search (409 when AI is off). */
      ask: (query: string) => call("post", "/api/v2/events/ask", { body: { query } }),
      /** For <img src>: cookie mode only (an <img> can't send a bearer token). */
      thumbnailUrl: (eventId: string) => `${baseUrl}/api/v2/events/${encodeURIComponent(eventId)}/thumbnail.jpg`,
      /** Appearance search: events showing the same-looking person/vehicle (503 when no model is installed). */
      similar: (eventId: string, body: SimilarEventsRequest = {}) => call("post", "/api/v2/events/{eventId}/similar", { path: { eventId }, body }),
    },
    appearance: {
      /** Whether appearance search is available (and why not), plus the latest index job. */
      status: () => call("get", "/api/v2/appearance"),
      startBackfill: () => call("post", "/api/v2/appearance/backfill"),
      cancelBackfill: () => call("delete", "/api/v2/appearance/backfill"),
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
    bookmarks: {
      list: async (cameraId: string, from?: number, to?: number) => (await call("get", "/api/v2/cameras/{cameraId}/bookmarks", { path: { cameraId }, query: { from, to } })).items,
      create: (cameraId: string, body: BookmarkInput) => call("post", "/api/v2/cameras/{cameraId}/bookmarks", { path: { cameraId }, body }),
      update: (bookmarkId: string, body: BookmarkPatch) => call("patch", "/api/v2/bookmarks/{bookmarkId}", { path: { bookmarkId }, body }),
      remove: (bookmarkId: string) => call("delete", "/api/v2/bookmarks/{bookmarkId}", { path: { bookmarkId } }),
    },
    evidence: {
      list: async () => (await call("get", "/api/v2/evidence")).items,
      /** Lock one camera's range; answers when the clip is in the vault (up to a few minutes). */
      create: (body: { cameraId: string; fromMs: number; toMs: number; title: string; note?: string; watermark?: boolean; blurRegions?: ExportRegion[] }) => call("post", "/api/v2/evidence", { body }),
      verify: (evidenceId: string) => call("post", "/api/v2/evidence/{evidenceId}/verify", { path: { evidenceId } }),
      remove: (evidenceId: string) => call("delete", "/api/v2/evidence/{evidenceId}", { path: { evidenceId } }),
      /** The case's MP4 with the client's auth. */
      download: (evidenceId: string, signal?: AbortSignal) => fetchBlob(`/api/v2/evidence/${encodeURIComponent(evidenceId)}/download`, signal),
      /** The self-verifying package zip (media, manifest.json, notes, offline player.html). */
      package: (evidenceId: string, signal?: AbortSignal) => fetchBlob(`/api/v2/evidence/${encodeURIComponent(evidenceId)}/package`, signal),
      /** Create an expiring share link; the response's url/token are shown once and never again. */
      share: (evidenceId: string, body: { expiry: "1h" | "24h" | "7d" | "custom"; expiresAt?: number; maxUses?: number; note?: string }) =>
        call("post", "/api/v2/evidence/{evidenceId}/share", { path: { evidenceId }, body }),
    },
    shareLinks: {
      list: async () => (await call("get", "/api/v2/share-links")).items,
      revoke: (shareLinkId: string) => call("delete", "/api/v2/share-links/{shareLinkId}", { path: { shareLinkId } }),
    },
    /** Long (chunked) exports of up to 12 hours; progress also shows in the tasks list. */
    exportJobs: {
      start: (body: { cameraId: string; fromMs: number; toMs: number; dest: "download" | "evidence"; title?: string; note?: string; watermark?: boolean; blurRegions?: ExportRegion[] }) => call("post", "/api/v2/export-jobs", { body }),
      get: (taskId: string) => call("get", "/api/v2/export-jobs/{taskId}", { path: { taskId } }),
      cancel: (taskId: string) => call("delete", "/api/v2/export-jobs/{taskId}", { path: { taskId } }),
      /** A finished download-destination job's MP4 with the client's auth. */
      download: (taskId: string, signal?: AbortSignal) => fetchBlob(`/api/v2/export-jobs/${encodeURIComponent(taskId)}/download`, signal),
    },
    exportSchedules: {
      list: async () => (await call("get", "/api/v2/export-schedules")).items,
      create: (body: ExportScheduleInput) => call("post", "/api/v2/export-schedules", { body }),
      update: (scheduleId: string, body: Partial<ExportScheduleInput>) => call("patch", "/api/v2/export-schedules/{scheduleId}", { path: { scheduleId }, body }),
      remove: (scheduleId: string) => call("delete", "/api/v2/export-schedules/{scheduleId}", { path: { scheduleId } }),
    },
    views: {
      list: async () => (await call("get", "/api/v2/views")).items,
      create: (body: ViewInput) => call("post", "/api/v2/views", { body }),
      update: (viewId: string, body: ViewInput) => call("put", "/api/v2/views/{viewId}", { path: { viewId }, body }),
      remove: (viewId: string) => call("delete", "/api/v2/views/{viewId}", { path: { viewId } }),
    },
    /** The Shared tree: folders (flat, with parentId) and shared views. */
    viewGroups: {
      tree: () => call("get", "/api/v2/view-groups"),
      create: (body: ViewGroupInput) => call("post", "/api/v2/view-groups", { body }),
      update: (groupId: string, body: ViewGroupInput) => call("put", "/api/v2/view-groups/{groupId}", { path: { groupId }, body }),
      remove: (groupId: string) => call("delete", "/api/v2/view-groups/{groupId}", { path: { groupId } }),
    },
    /** WAVE9(C): pull a camera's SD-card footage (ONVIF Profile G) into recording gaps; progress also shows in the tasks list. */
    edgeRecovery: {
      start: (cameraId: string, fromMs: number, toMs: number) => call("post", "/api/v2/cameras/{cameraId}/edge-recovery", { path: { cameraId }, body: { fromMs, toMs } }),
      get: (taskId: string) => call("get", "/api/v2/edge-recovery/{taskId}", { path: { taskId } }),
      cancel: (taskId: string) => call("delete", "/api/v2/edge-recovery/{taskId}", { path: { taskId } }),
    },
  };
}

/** Strip the read-only fields a Rule carries (RuleInput rejects unknown fields). */
export function ruleInputOf(r: RuleInput | Rule): RuleInput {
  const { name, enabled, cameraIds, kinds, minSeverity, schedule, fromMinute, toMinute, days, delivery, instructions, timeProfileId, actions } = r;
  return { name, enabled, cameraIds, kinds, minSeverity, schedule, fromMinute, toMinute, days, delivery, instructions, ...(timeProfileId ? { timeProfileId } : {}), ...(actions?.length ? { actions } : {}) };
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
      return ["manageCameras", "manageUsers", "changeSettings", "viewAudit", "acknowledgeAlarms", "exportEvidence", "viewLive", "controlPtz", "manageViews"];
    case "Supervisor":
      return ["viewAudit", "acknowledgeAlarms", "exportEvidence", "viewLive", "controlPtz"];
    case "Operator":
      return ["acknowledgeAlarms", "exportEvidence", "viewLive", "controlPtz"];
    default:
      return ["viewLive"];
  }
}
