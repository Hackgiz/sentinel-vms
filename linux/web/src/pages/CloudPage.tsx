// The Sentinel Cloud card (WAVE13-CLOUD-ONBOARDING.md, Stream C): one card,
// one sign-in, one button. The main sidebar's Sentinel Cloud page (#/cloud)
// and Sentinel Admin → Basics → Sentinel Cloud both render CloudPanel.
// The browser signs in to Supabase itself, so the password never reaches
// this server, and the session JWT lives only in this component's memory.
// The pure parts (requests, errors, lines, the state machine) are in
// ../cloudAccount.ts.
import { useCallback, useEffect, useMemo, useReducer, useRef, useState, type FormEvent, type ReactNode } from "react";
import { can, type User } from "../api";
import { Confirm, CopyButton, Loading } from "../admin/ui";
import { CloudError, MANAGE_URL, cloudApi, periodLabel, planLabel, statusLabel, type CloudSettings, type CloudStatusWithSignIn } from "../admin/cloud";
import { ApiError, errorMessage } from "../shared/api";
import {
  CHECKOUT_POLL_MS,
  authRequest,
  backupLine,
  cardReducer,
  checkoutPollNext,
  checkoutRequest,
  credentialsProblem,
  execute,
  initialCardState,
  isCheckoutReturn,
  jwtUsable,
  linkErrorMessage,
  oneSentence,
  parseAuthResponse,
  parseRedirectResponse,
  parseSignIn,
  planPaid,
  planState,
  planSummary,
  portalRequest,
  priceLabel,
  pushLine,
  remoteLine,
  remoteLineFromAddress,
  turnOnLabel,
  usd,
  withoutCheckout,
  type AccountAction,
  type AuthOutcome,
  type CardState,
  type Interval,
  type Mode,
  type SignInConfig,
  type Tone,
} from "../cloudAccount";
import { PageHeader } from "../shared/Cards"; // WAVE21

type Status = CloudStatusWithSignIn;
type RemoteAddress = { remoteURL?: string | null; via?: string | null };

export default function CloudPage({ user }: { user: User }) {
  return <CloudPanel canEdit={can.changeSettings(user)} />;
}

/** The page head and the card; Sentinel Admin's Basics → Sentinel Cloud renders the same. */
export function CloudPanel({ canEdit }: { canEdit: boolean }) {
  return (
    <>
      <PageHeader title="Sentinel Cloud" subtitle="Watch your cameras from anywhere, with push notifications and offsite backup. Optional: cameras and local use stay free." />
      <CloudCard canEdit={canEdit} />
    </>
  );
}

const browserFetch: typeof fetch = (input, init) => fetch(input, init);

/** Open the Stripe tab during the click (a tab opened after an await is blocked as a pop-up). */
function openTab(): Window | null {
  try {
    const w = window.open("", "_blank");
    if (w) {
      w.document.title = "Opening Stripe…";
      w.document.body.textContent = "Opening Stripe…";
    }
    return w;
  } catch {
    return null;
  }
}

/** Point the opened tab at Stripe; with pop-ups blocked, go in this tab (Stripe returns to #/cloud). */
function goTo(tab: Window | null, url: string) {
  if (tab && !tab.closed) {
    tab.opener = null;
    tab.location.replace(url);
    return;
  }
  window.location.assign(url);
}

function loadError(e: unknown): string {
  if (e instanceof ApiError && e.status === 404) return "Sentinel Cloud is turned on from the Management Server, not from this server.";
  return oneSentence(errorMessage(e), "Sentinel Cloud's status couldn't be loaded.");
}

/** The card with its state, requests and timers. */
export function CloudCard({ canEdit }: { canEdit: boolean }) {
  const [state, dispatch] = useReducer(cardReducer, initialCardState);
  const [status, setStatus] = useState<Status | null>(null);
  const [address, setAddress] = useState<RemoteAddress | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [plan, setPlan] = useState("");
  const [billing, setBilling] = useState<Interval>("month");
  const [confirmOff, setConfirmOff] = useState(false);
  // The account's Supabase session: this component's memory only, never storage or the server.
  const session = useRef<{ token: string; expiresAt: number | null } | null>(null);
  // Read once, so StrictMode's second effect run still knows Stripe sent us here.
  const [fromCheckout] = useState(() => isCheckoutReturn(window.location.hash));
  const signIn = status?.signIn;
  const config = useMemo(() => parseSignIn(signIn), [signIn]);

  const show = useCallback((s: Status) => {
    setStatus(s);
    dispatch({ type: "loaded", linked: !!s.linked });
  }, []);

  const load = useCallback(async (): Promise<Status | null> => {
    try {
      const s = await cloudApi.get();
      show(s);
      return s;
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) {
        // No changeSettings: the remote line from the address phones use.
        dispatch({ type: "viewer" });
        cloudApi.remote().then(setAddress, () => setAddress(null));
        return null;
      }
      dispatch({ type: "loadFailed", message: loadError(e) });
      return null;
    }
  }, [show]);

  /** An immediate entitlement refresh; GET on a server without the route. */
  const refreshNow = useCallback(async (): Promise<Status | null> => {
    try {
      const s = await cloudApi.refresh();
      show(s);
      return s;
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return load();
      return null; // throttled (one per 10 s) or a blip: the caller tries again
    }
  }, [show, load]);

  useEffect(() => {
    void load();
  }, [load]);

  // WAVE13: the server fetches its sign-in config in the background on the
  // first status read after a start, so a missing one is usually seconds
  // away. Read again a few times before leaving the "can't reach" notice up.
  const configRetries = useRef(0);
  useEffect(() => {
    if (!status || config || configRetries.current >= 4) return;
    configRetries.current += 1;
    const t = window.setTimeout(() => void load(), 3_000);
    return () => window.clearTimeout(t);
  }, [status, config, load]);

  // Re-read while linked: every 5 s while the relay connects, else every 30 s.
  const remoteState = status?.remote?.state;
  const every = state.phase === "linked" ? (remoteState === "connecting" || remoteState === "retrying" ? 5_000 : 30_000) : null;
  useEffect(() => {
    if (!every) return;
    const t = window.setInterval(() => void load(), every);
    return () => window.clearInterval(t);
  }, [every, load]);

  // Back from Stripe Checkout: refresh every 5 s for up to 2 minutes until the plan is active.
  useEffect(() => {
    if (!fromCheckout) return;
    if (isCheckoutReturn(window.location.hash)) history.replaceState(null, "", withoutCheckout(window.location.hash));
    dispatch({ type: "checkoutReturn" });
    const started = Date.now();
    let alive = true;
    let timer = 0;
    const tick = async () => {
      const s = await refreshNow();
      if (!alive) return;
      const next = checkoutPollNext(started, Date.now(), s);
      if (next === "done") dispatch({ type: "checkoutDone" });
      else if (next === "timeout") dispatch({ type: "checkoutTimeout" });
      else timer = window.setTimeout(tick, CHECKOUT_POLL_MS);
    };
    void tick();
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [fromCheckout, refreshNow]);

  // Stripe opened in another tab: coming back to this one refreshes the plan.
  useEffect(() => {
    if (!state.watchFocus) return;
    const onFocus = () =>
      void refreshNow().then((s) => {
        if (s) dispatch({ type: "focusSeen", paid: planPaid(s) });
      });
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [state.watchFocus, refreshNow]);

  async function authenticate(cfg: SignInConfig, mode: Mode): Promise<AuthOutcome> {
    const r = await execute(authRequest(cfg, mode, email, password), browserFetch);
    return parseAuthResponse(mode, r.status, r.text);
  }

  async function turnOn(e: FormEvent) {
    e.preventDefault();
    if (!config || state.phase !== "notLinked") return;
    const mode: Mode = state.confirmEmail ? "signIn" : state.mode;
    const problem = credentialsProblem(email, password, mode);
    if (problem) return dispatch({ type: "invalid", message: problem });
    dispatch({ type: "submit" });
    const out = await authenticate(config, mode);
    if (out.kind === "confirm") return dispatch({ type: "confirmNeeded", email: out.email || email.trim() });
    if (out.kind === "error") return dispatch({ type: "authFailed", message: out.message, reason: out.reason });
    session.current = { token: out.accessToken, expiresAt: out.expiresAt };
    dispatch({ type: "authed" });
    try {
      const s = await cloudApi.linkAccount(out.accessToken);
      setStatus(s);
      if (!s.linked) return dispatch({ type: "linkFailed", message: "Sentinel Cloud didn't finish linking this server. Try again." });
      setPassword("");
      dispatch({ type: "linked" });
    } catch (err) {
      const ae = err instanceof ApiError ? err : null;
      const code = ae?.code ?? "";
      if (code === "cloud_sign_in") session.current = null;
      if (code === "cloud_already_linked" || ae?.status === 409) {
        const s = await load();
        if (s?.linked) return dispatch({ type: "linked" });
      }
      dispatch({
        type: "linkFailed",
        message: linkErrorMessage({ status: ae?.status ?? 0, code, message: ae?.message ?? errorMessage(err) }),
        upgradeURL: err instanceof CloudError ? err.upgradeURL : "",
      });
    }
  }

  async function redirect(action: AccountAction, token: string, tab: Window | null) {
    if (!config) return;
    dispatch({ type: "redirecting" });
    const origin = window.location.origin;
    const req = action.kind === "checkout" ? checkoutRequest(config, token, action.plan, action.interval, origin) : portalRequest(config, token, origin);
    const r = await execute(req, browserFetch);
    const out = parseRedirectResponse(r.status, r.text);
    if (out.kind === "url") {
      goTo(tab, out.url);
      return dispatch({ type: "redirected" });
    }
    tab?.close();
    if (out.signIn) session.current = null;
    dispatch({ type: "redirectFailed", message: out.message, signIn: out.signIn, action });
    // Already subscribed but this server still shows the old plan: fetch it now.
    if (out.manage) void refreshNow();
  }

  /** Subscribe and Manage plan: straight to Stripe with the JWT in memory, else the inline sign-in first. */
  function act(action: AccountAction) {
    if (!config || state.busy) return;
    const s = session.current;
    if (!s || !jwtUsable(s.expiresAt)) {
      session.current = null;
      return dispatch({ type: "needSignIn", action });
    }
    void redirect(action, s.token, openTab());
  }

  async function signInAndContinue(e: FormEvent) {
    e.preventDefault();
    const action = state.pending;
    if (!config || !action || state.busy) return;
    const problem = credentialsProblem(email, password, "signIn");
    if (problem) return dispatch({ type: "invalid", message: problem });
    const tab = openTab();
    dispatch({ type: "redirecting" });
    const out = await authenticate(config, "signIn");
    if (out.kind !== "session") {
      tab?.close();
      return dispatch(
        out.kind === "error"
          ? { type: "authFailed", message: out.message, reason: out.reason }
          : { type: "authFailed", message: "Confirm your email first, then sign in.", reason: "unconfirmed" },
      );
    }
    session.current = { token: out.accessToken, expiresAt: out.expiresAt };
    setPassword("");
    await redirect(action, out.accessToken, tab);
  }

  async function put(body: CloudSettings) {
    try {
      show(await cloudApi.put(body));
    } catch (e) {
      dispatch({ type: "invalid", message: oneSentence(errorMessage(e)) });
    }
  }

  async function cancelCode() {
    try {
      show(await cloudApi.unlink());
    } catch (e) {
      dispatch({ type: "invalid", message: oneSentence(errorMessage(e)) });
    }
  }

  return (
    <>
      <CardView
        state={state}
        status={status}
        address={address}
        config={config}
        canEdit={canEdit}
        email={email}
        password={password}
        plan={plan}
        billing={billing}
        on={{
          email: setEmail,
          password: setPassword,
          mode: (mode) => dispatch({ type: "mode", mode }),
          plan: setPlan,
          billing: setBilling,
          turnOn,
          act,
          signInAndContinue,
          cancelSignIn: () => dispatch({ type: "cancelSignIn" }),
          turnOff: () => setConfirmOff(true),
          retry: () => void load(),
          put: (b) => void put(b),
          cancelCode: () => void cancelCode(),
        }}
      />
      {confirmOff && (
        <Confirm
          title="Turn off Sentinel Cloud?"
          confirmLabel="Turn off"
          onCancel={() => setConfirmOff(false)}
          run={async () => {
            const s = await cloudApi.unlink();
            setStatus(s);
            setConfirmOff(false);
            dispatch({ type: "unlinked" });
          }}
        >
          <p>Remote access through Sentinel Cloud stops, push notifications go back to off, and new alarms are no longer backed up. Backups already stored are kept for 7 days.</p>
          <p>Your subscription continues; use Manage plan to change or cancel it.</p>
        </Confirm>
      )}
    </>
  );
}

// ------------------------------------------------------------------- view ---

export interface CardHandlers {
  email: (v: string) => void;
  password: (v: string) => void;
  mode: (m: Mode) => void;
  plan: (id: string) => void;
  billing: (i: Interval) => void;
  turnOn: (e: FormEvent) => void;
  act: (a: AccountAction) => void;
  signInAndContinue: (e: FormEvent) => void;
  cancelSignIn: () => void;
  turnOff: () => void;
  retry: () => void;
  put: (b: CloudSettings) => void;
  cancelCode: () => void;
}

export interface CardViewProps {
  state: CardState;
  status: Status | null;
  address: RemoteAddress | null;
  config: SignInConfig | null;
  canEdit: boolean;
  email: string;
  password: string;
  plan: string;
  billing: Interval;
  on: CardHandlers;
}

function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span className={`badge ${tone || "blue"}`}>
      {tone && <span className="dot" />}
      {children}
    </span>
  );
}

function ErrorLine({ text, url }: { text: string; url?: string }) {
  if (!text) return null;
  return (
    <div className="error cloud-error" role="alert">
      {text}
      {url && (
        <>
          {" "}
          <a href={url} target="_blank" rel="noopener noreferrer">
            Upgrade the plan
          </a>
        </>
      )}
    </div>
  );
}

/** The card as a pure function of its state (rendered in tests without a browser). */
export function CardView(p: CardViewProps) {
  const { state, status: s } = p;
  const linked = state.phase === "linked" && !!s;
  return (
    <section className="card cloud-card" aria-labelledby="cloud-card-h" aria-busy={state.phase === "loading" || state.phase === "linking" || state.busy}>
      <div className="cloud-head">
        <h2 id="cloud-card-h">Sentinel Cloud</h2>
        {linked && <PlanBadge s={s} />}
      </div>
      <CheckoutNotice state={state} s={s} />
      {state.phase === "loading" ? (
        <Loading />
      ) : state.phase === "unavailable" ? (
        <div className="error admin-error" role="alert">
          <span className="grow">{state.error}</span>
          <button type="button" className="btn small" onClick={p.on.retry}>
            Try again
          </button>
        </div>
      ) : state.phase === "viewer" ? (
        <>
          <StatusLines remote={remoteLineFromAddress(p.address)} />
          <p className="muted small cloud-foot">An administrator manages Sentinel Cloud for this site.</p>
        </>
      ) : linked ? (
        <>
          {s.accountEmailMasked && <p className="muted small cloud-account">Linked to {s.accountEmailMasked}</p>}
          <StatusLines remote={remoteLine(s)} s={s} />
          {p.canEdit && <LinkedActions {...p} s={s} />}
        </>
      ) : p.canEdit ? (
        <NotLinked {...p} />
      ) : (
        <p className="muted">Sentinel Cloud is off on this server. An administrator can turn it on here.</p>
      )}
    </section>
  );
}

function PlanBadge({ s }: { s: Status }) {
  const ps = planState(s);
  if (ps === "active" || ps === "grace") return <Badge tone={ps === "active" ? "green" : "amber"}>{`${planLabel(s.plan)} · ${statusLabel(s.status)}`}</Badge>;
  return <Badge tone="">{ps === "canceled" ? "Canceled" : "Free"}</Badge>;
}

function CheckoutNotice({ state, s }: { state: CardState; s: Status | null }) {
  switch (state.checkout) {
    case "waiting":
      return (
        <div className="notice cloud-notice" role="status">
          <span className="spinner" aria-hidden="true" />
          Thanks! Waiting for Sentinel Cloud to confirm your subscription…
        </div>
      );
    case "done":
      return (
        <div className="notice success" role="status">
          ✓ {planLabel(s?.plan)} is active on this server.
        </div>
      );
    case "timeout":
      return (
        <div className="notice" role="status">
          Your payment went through, but the plan hasn't reached this server yet. It shows up here within a few minutes.
        </div>
      );
    default:
      return null;
  }
}

/** Remote address (+ Copy), push notifications, offsite backup. */
export function StatusLines({ remote, s }: { remote: { text: string; url: string; tone: Tone }; s?: Status }) {
  const remoteProblem = s && remote.text !== "connected" && s.remote?.enabled && s.remote?.error ? oneSentence(s.remote.error) : "";
  return (
    <div className="cloud-lines">
      <div className="stat">
        <span>Remote address</span>
        <span className="cloud-value">
          <Badge tone={remote.tone}>{remote.text}</Badge>
          {remote.url && (
            <>
              <span className="muted" aria-hidden="true">
                ·
              </span>
              <code className="cloud-url">{remote.url}</code>
              <CopyButton text={remote.url} />
            </>
          )}
        </span>
      </div>
      {remoteProblem && <p className="muted small cloud-problem">{remoteProblem}</p>}
      {s && (
        <div className="stat">
          <span>Push notifications</span>
          <span>{pushLine(s)}</span>
        </div>
      )}
      {s && (
        <div className="stat">
          <span>Offsite backup</span>
          <span>{backupLine(s)}</span>
        </div>
      )}
    </div>
  );
}

function CredentialFields({ p, disabled, signUp }: { p: CardViewProps; disabled: boolean; signUp: boolean }) {
  return (
    <>
      <label className="field">
        <span>Email</span>
        <input type="email" autoComplete="username" inputMode="email" spellCheck={false} value={p.email} disabled={disabled} onChange={(e) => p.on.email(e.target.value)} required />
      </label>
      <label className="field">
        <span>Password</span>
        <input
          type="password"
          autoComplete={signUp ? "new-password" : "current-password"}
          value={p.password}
          disabled={disabled}
          onChange={(e) => p.on.password(e.target.value)}
          required
          minLength={signUp ? 6 : undefined}
        />
        {signUp && <small>At least 6 characters.</small>}
      </label>
    </>
  );
}

function NotLinked(p: CardViewProps) {
  const { state, status: s, config } = p;
  const linking = state.phase === "linking";
  const signUp = state.mode === "signUp" && !state.confirmEmail;
  return (
    <form className="cloud-form" onSubmit={p.on.turnOn} noValidate>
      <p className="muted">
        Sign in with your Sentinel Cloud account, or create one, and this server links itself: remote access from anywhere, push notifications and offsite backup, with nothing to install.
      </p>
      {s?.link && (
        <div className="notice cloud-notice" role="status">
          <span className="grow">A link code ({s.link.userCode}) is still waiting for approval on sentvms.com.</span>
          <button type="button" className="btn small" onClick={p.on.cancelCode} disabled={linking}>
            Cancel the code
          </button>
        </div>
      )}
      {!config ? (
        <div className="notice cloud-notice" role="status">
          <span className="grow">This server can't reach Sentinel Cloud right now, so signing in isn't available.</span>
          <button type="button" className="btn small" onClick={p.on.retry}>
            Try again
          </button>
        </div>
      ) : (
        <>
          <div className="seg cloud-mode" role="group" aria-label="Account">
            <button type="button" className={signUp ? "" : "on"} aria-pressed={!signUp} disabled={linking} onClick={() => p.on.mode("signIn")}>
              Sign in
            </button>
            <button type="button" className={signUp ? "on" : ""} aria-pressed={signUp} disabled={linking} onClick={() => p.on.mode("signUp")}>
              Create account
            </button>
          </div>
          {state.confirmEmail && (
            <div className="notice cloud-notice" role="status">
              Check your email: a confirmation link went to {state.confirmEmail}. Open it, then sign in here.
            </div>
          )}
          <CredentialFields p={p} disabled={linking} signUp={signUp} />
          <div className="cloud-actions">
            <button type="submit" className="btn primary" disabled={linking}>
              {linking && <span className="spinner" aria-hidden="true" />}
              {turnOnLabel(state)}
            </button>
            <a className="small" href={`${config.siteBase}/pricing`} target="_blank" rel="noopener noreferrer">
              Plans and pricing
            </a>
          </div>
          <ErrorLine text={state.error} url={state.errorURL} />
          <p className="muted small cloud-foot">Your password goes straight to Sentinel Cloud over HTTPS; this server never sees it. Cameras and local use stay free either way.</p>
        </>
      )}
      {!config && <ErrorLine text={state.error} url={state.errorURL} />}
    </form>
  );
}

function PlanPicker({ p, offer }: { p: CardViewProps; offer: SignInConfig["plans"][number] }) {
  const plans = p.config?.plans ?? [];
  return (
    <div className="cloud-plans">
      <div className="cloud-plan-row">
        <select aria-label="Plan" value={offer.id} disabled={p.state.busy} onChange={(e) => p.on.plan(e.target.value)}>
          {plans.map((x) => (
            <option key={x.id} value={x.id}>
              {`${x.name} · ${usd(x.month)}/mo · ${usd(x.year)}/yr`}
            </option>
          ))}
        </select>
        <div className="seg" role="group" aria-label="Billing">
          <button type="button" className={p.billing === "month" ? "on" : ""} aria-pressed={p.billing === "month"} disabled={p.state.busy} onClick={() => p.on.billing("month")}>
            Monthly
          </button>
          <button type="button" className={p.billing === "year" ? "on" : ""} aria-pressed={p.billing === "year"} disabled={p.state.busy} onClick={() => p.on.billing("year")}>
            Yearly
          </button>
        </div>
      </div>
      <div className="muted small">
        {[planSummary(offer), priceLabel(offer, p.billing)].filter(Boolean).join(" · ")}
      </div>
    </div>
  );
}

function LinkedActions(p: CardViewProps & { s: Status }) {
  const { state, s, config } = p;
  const ps = planState(s);
  const paid = ps === "active" || ps === "grace";
  const offer = config?.plans.find((x) => x.id === p.plan) ?? config?.plans[0];
  const renew = periodLabel(s.status, s.periodEnd);
  const opening = state.busy && !state.pending;
  return (
    <>
      {renew && <p className={`small cloud-renew ${ps === "grace" ? "" : "muted"}`}>{renew}</p>}
      {!paid && config && offer && <PlanPicker p={p} offer={offer} />}
      <div className="cloud-actions">
        {paid ? (
          <button type="button" className="btn primary" disabled={!config || state.busy} onClick={() => p.on.act({ kind: "portal" })}>
            {opening ? "Opening Stripe…" : "Manage plan"}
          </button>
        ) : (
          <button type="button" className="btn primary" disabled={!config || !offer || state.busy} onClick={() => offer && p.on.act({ kind: "checkout", plan: offer.id, interval: p.billing })}>
            {opening ? "Opening Stripe…" : ps === "canceled" ? "Subscribe again" : "Subscribe"}
          </button>
        )}
        <button type="button" className="btn danger" disabled={state.busy} onClick={p.on.turnOff}>
          Turn off
        </button>
        {!config && (
          <a className="btn" href={MANAGE_URL} target="_blank" rel="noopener noreferrer">
            Plans and billing on sentvms.com
          </a>
        )}
      </div>
      {state.pending ? <InlineSignIn {...p} /> : <ErrorLine text={state.error} />}
      {paid && (
        <details className="cloud-more">
          <summary>Settings</summary>
          <label className="check">
            <input type="checkbox" checked={!!s.remote?.enabled} onChange={(e) => p.on.put({ remote: e.target.checked })} />
            Allow remote access through Sentinel Cloud
          </label>
          <label className="check">
            <input type="checkbox" checked={!!s.backup?.enabled} onChange={(e) => p.on.put({ backup: e.target.checked })} />
            Back up new alarms' snapshots and locked clips
          </label>
          {s.serverId && (
            <div className="muted small">
              This server: {s.serverName || "—"} ({s.serverId})
            </div>
          )}
        </details>
      )}
    </>
  );
}

/** Subscribe or Manage plan after a reload: the JWT is gone, so ask for the password again, right here. */
function InlineSignIn(p: CardViewProps & { s: Status }) {
  const { state, s } = p;
  const what = state.pending?.kind === "portal" ? "manage the plan" : "subscribe";
  return (
    <form className="cloud-form cloud-resign" onSubmit={p.on.signInAndContinue} noValidate>
      <p className="small">
        Sign in to Sentinel Cloud{s.accountEmailMasked ? ` as ${s.accountEmailMasked}` : ""} to {what}.
      </p>
      <CredentialFields p={p} disabled={state.busy} signUp={false} />
      <div className="cloud-actions">
        <button type="submit" className="btn primary" disabled={state.busy}>
          {state.busy && <span className="spinner" aria-hidden="true" />}
          {state.busy ? "Opening Stripe…" : "Sign in and continue"}
        </button>
        <button type="button" className="btn" disabled={state.busy} onClick={p.on.cancelSignIn}>
          Cancel
        </button>
      </div>
      <ErrorLine text={state.error} />
    </form>
  );
}
