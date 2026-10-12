// Sign in to a site: name and password (local or directory users), and
// single sign-on when the site has it. Desktop SSO goes through the system
// browser and a loopback redirect (desktop.oidcSignIn); a browser on
// 127.0.0.1/localhost can do the same with its own page as the redirect.
import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, type AuthProviders } from "../shared/api";
import { desktop, type PendingCertificate } from "./bridge";
import { isSafeRedirectUri, newState, oidcStartUrl } from "./oidc";
import { refusedCertificate, type Session } from "./session";

const browserOidcKey = "operator.oidc";

/** Where a browser on a loopback address sends SSO back to (this page). */
function browserRedirectUri(): string | null {
  if (typeof location === "undefined") return null;
  const u = `${location.origin}${location.pathname}`;
  return isSafeRedirectUri(u) ? u : null;
}

/** A browser SSO round trip that just came back to this page: its code, once. */
export function takeBrowserOidcReturn(): { siteId: string; code: string } | { siteId: string; error: string } | null {
  const q = new URLSearchParams(location.search);
  if (!q.has("state")) return null;
  let saved: { state: string; siteId: string } | null = null;
  try {
    saved = JSON.parse(sessionStorage.getItem(browserOidcKey) ?? "null");
    sessionStorage.removeItem(browserOidcKey);
  } catch {
    /* storage blocked */
  }
  history.replaceState(null, "", location.pathname + location.hash);
  if (!saved || saved.state !== q.get("state")) return null;
  const code = q.get("code");
  if (code) return { siteId: saved.siteId, code };
  return { siteId: saved.siteId, error: q.get("error_description") || "Sign-in was refused." };
}

export default function SignIn({
  session,
  initialError,
  onSignedIn,
  onCertificate,
  onBack,
}: {
  session: Session;
  initialError?: string;
  onSignedIn: (name: string) => void;
  onCertificate: (c: PendingCertificate) => void;
  onBack: () => void;
}) {
  const { site, client } = session;
  const [providers, setProviders] = useState<AuthProviders | null>(null);
  const [name, setName] = useState(site.lastUser ?? "");
  const [password, setPassword] = useState("");
  const [error, setError] = useState(initialError ?? "");
  const [busy, setBusy] = useState<"" | "password" | "sso">("");

  async function failed(e: unknown) {
    const cert = await refusedCertificate(site, e);
    if (cert) onCertificate(cert);
    else setError(errorMessage(e));
  }

  useEffect(() => {
    let alive = true;
    client
      .providers()
      .then((p) => alive && setProviders(p))
      .catch((e) => {
        if (!alive) return;
        // Older servers: password only.
        setProviders({ local: true, ldap: false, oidc: { enabled: false } });
        void failed(e);
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy("password");
    setError("");
    try {
      const u = await client.signIn(name.trim(), password);
      setPassword("");
      onSignedIn(u.name);
    } catch (err) {
      await failed(err);
    } finally {
      setBusy("");
    }
  }

  const browserRedirect = desktop ? null : browserRedirectUri();
  async function sso() {
    setError("");
    if (desktop) {
      setBusy("sso");
      try {
        const { code } = await desktop.oidcSignIn(site.address);
        const u = await client.signInWithOidcCode(code);
        onSignedIn(u.name);
      } catch (err) {
        await failed(err);
      } finally {
        setBusy("");
      }
    } else if (browserRedirect) {
      const state = newState();
      try {
        sessionStorage.setItem(browserOidcKey, JSON.stringify({ state, siteId: site.id }));
      } catch {
        /* without storage the return can't be matched; it will be ignored */
      }
      location.assign(oidcStartUrl(site.address, browserRedirect, state));
    }
  }

  const ssoLabel = providers?.oidc.displayName || "Sign in with single sign-on";
  const passwords = !providers || providers.local || providers.ldap;
  return (
    <div className="auth">
      <div className="card">
        <div className="logo">
          <img src="/favicon.svg" alt="" />
        </div>
        <h1>{site.name}</h1>
        <p className="lead mono">{site.address}</p>
        {error && (
          <div className="error" role="alert">
            {error}
          </div>
        )}
        {passwords && (
          <form onSubmit={submit}>
            <label className="field">
              <span>Name</span>
              <input type="text" autoFocus={!name} value={name} onChange={(e) => setName(e.target.value)} autoComplete="username" autoCapitalize="off" spellCheck={false} />
            </label>
            <label className="field">
              <span>Password</span>
              <input type="password" autoFocus={!!name} value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
            </label>
            <button className="btn primary" type="submit" disabled={!!busy || !name.trim() || !password}>
              {busy === "password" ? "Signing in…" : "Sign in"}
            </button>
          </form>
        )}
        {providers?.oidc.enabled && (
          <>
            {passwords && <div className="op-or">or</div>}
            <button className="btn" disabled={!!busy || (!desktop && !browserRedirect)} onClick={sso}>
              {busy === "sso" ? "Waiting for the browser…" : ssoLabel}
            </button>
            {busy === "sso" && desktop && (
              <button className="btn op-mt" onClick={() => void desktop!.oidcCancel()}>
                Cancel single sign-on
              </button>
            )}
            {!desktop && !browserRedirect && <small className="muted op-note">Single sign-on needs the Sentinel Operator app.</small>}
          </>
        )}
        <button className="btn op-mt" onClick={onBack} disabled={busy === "password"}>
          Other site
        </button>
      </div>
    </div>
  );
}
