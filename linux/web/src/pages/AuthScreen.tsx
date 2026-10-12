import { useEffect, useState, type FormEvent } from "react";
import { api, type User } from "../api";
import type { components } from "../api/v2";

type AuthProviders = components["schemas"]["AuthProviders"];

// ssoError is set by the server when single sign-on comes back refused.
function takeSSOError(): string {
  const params = new URLSearchParams(window.location.search);
  const msg = params.get("ssoError") ?? "";
  if (msg) {
    params.delete("ssoError");
    const q = params.toString();
    window.history.replaceState(null, "", window.location.pathname + (q ? "?" + q : "") + window.location.hash);
  }
  return msg;
}

export default function AuthScreen({ setup, onSignedIn }: { setup: boolean; onSignedIn: (u: User) => void }) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState(() => (setup ? "" : takeSSOError()));
  const [busy, setBusy] = useState(false);
  const [providers, setProviders] = useState<AuthProviders | null>(null);

  useEffect(() => {
    if (setup) return;
    fetch("/api/v2/auth/providers")
      .then((r) => (r.ok ? (r.json() as Promise<AuthProviders>) : null))
      .then(setProviders)
      .catch(() => setProviders(null));
  }, [setup]);

  function signInWithSSO() {
    const returnTo = window.location.pathname + window.location.search + window.location.hash;
    window.location.assign("/api/v2/auth/oidc/start?returnTo=" + encodeURIComponent(returnTo));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (setup && password !== confirm) {
      setError("Passwords don't match.");
      return;
    }
    setBusy(true);
    try {
      const res = setup ? await api.setup(name, password) : await api.login(name, password);
      onSignedIn(res.user);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth">
      <form className="card" onSubmit={submit}>
        <div className="logo">
          <img src="/favicon.svg" alt="" />
        </div>
        <h1>{setup ? "Welcome to Sentinel" : "Sign in"}</h1>
        <p className="lead">{setup ? "Create the administrator account for this server." : "Sentinel VMS"}</p>
        {error && <div className="error">{error}</div>}
        <label className="field">
          <span>Name</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} autoFocus autoComplete="username" required />
        </label>
        <label className="field">
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete={setup ? "new-password" : "current-password"}
            required
          />
          {setup && <small>At least 8 characters.</small>}
        </label>
        {setup && (
          <label className="field">
            <span>Confirm password</span>
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required />
          </label>
        )}
        <button className="btn primary" disabled={busy}>
          {busy ? "…" : setup ? "Create administrator" : "Sign in"}
        </button>
        {!setup && providers?.oidc.enabled && (
          <button type="button" className="btn" style={{ marginTop: 10 }} onClick={signInWithSSO}>
            {providers.oidc.displayName || "Sign in with single sign-on"}
          </button>
        )}
      </form>
    </div>
  );
}
