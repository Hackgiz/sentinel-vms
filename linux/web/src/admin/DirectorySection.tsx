import { useEffect, useState } from "react";
import { roles, type CameraGroup, type DirectorySettings, type LDAPSettings, type OIDCSettings, type Role, type RoleMapping } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { ChipPicker, ErrorBanner, Loading } from "./ui";

type TestResult = { ok: boolean; message: string; role?: Role; groups?: string[] };

export default function DirectorySection() {
  const loaded = useLoad(() => v2.directory.get(), []);
  const groups = useLoad(() => v2.groups.list(), []);
  const [d, setD] = useState<DirectorySettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (loaded.data) setD(loaded.data);
  }, [loaded.data]);

  if (!d) {
    return (
      <>
        <Head />
        <ErrorBanner error={loaded.error} onRetry={loaded.reload} />
        {!loaded.error && (
          <div className="card">
            <Loading />
          </div>
        )}
      </>
    );
  }

  const setLdap = (p: Partial<LDAPSettings>) => (setD({ ...d, ldap: { ...d.ldap, ...p } }), setSaved(false));
  const setOidc = (p: Partial<OIDCSettings>) => (setD({ ...d, oidc: { ...d.oidc, ...p } }), setSaved(false));

  async function save() {
    if (!d) return;
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const r = await v2.directory.save(d);
      setD(r);
      setSaved(true);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <Head />
      <ErrorBanner error={error} />
      {saved && (
        <div className="notice success" role="status">
          Saved. New sign-ins use these settings.
        </div>
      )}
      <div className="grid-2 directory-grid">
        <section className="card" aria-labelledby="ldap-h">
          <h2 id="ldap-h">LDAP / Active Directory</h2>
          <label className="check">
            <input type="checkbox" checked={d.ldap.enabled} onChange={(e) => setLdap({ enabled: e.target.checked })} />
            Check names and passwords against the directory
          </label>
          <label className="field">
            <span>Server URL</span>
            <input type="text" value={d.ldap.url} onChange={(e) => setLdap({ url: e.target.value })} placeholder="ldaps://dc1.corp.example:636" required={d.ldap.enabled} />
          </label>
          <label className="check">
            <input type="checkbox" checked={!!d.ldap.startTls} onChange={(e) => setLdap({ startTls: e.target.checked })} />
            Use StartTLS (for ldap:// addresses)
          </label>
          <label className="check">
            <input type="checkbox" checked={!!d.ldap.insecureSkipVerify} onChange={(e) => setLdap({ insecureSkipVerify: e.target.checked })} />
            Don't verify the server's certificate (testing only)
          </label>
          <label className="field">
            <span>Bind DN</span>
            <input type="text" value={d.ldap.bindDn ?? ""} onChange={(e) => setLdap({ bindDn: e.target.value })} placeholder="CN=sentinel,OU=Service,DC=corp,DC=example" autoComplete="off" />
          </label>
          <SecretField
            label="Bind password"
            has={!!d.ldap.hasBindPassword}
            value={d.ldap.bindPassword ?? ""}
            onChange={(v) => setLdap({ bindPassword: v })}
          />
          <label className="field">
            <span>User base DN</span>
            <input type="text" value={d.ldap.userBaseDn ?? ""} onChange={(e) => setLdap({ userBaseDn: e.target.value })} placeholder="DC=corp,DC=example" />
          </label>
          <label className="field">
            <span>User filter</span>
            <input type="text" value={d.ldap.userFilter ?? ""} onChange={(e) => setLdap({ userFilter: e.target.value })} placeholder="(&(objectClass=user)(sAMAccountName={name}))" />
            <small>{"{name}"} is replaced with the name typed at sign-in.</small>
          </label>
          <label className="field">
            <span>Group attribute</span>
            <input type="text" value={d.ldap.groupAttribute ?? ""} onChange={(e) => setLdap({ groupAttribute: e.target.value })} placeholder="memberOf" />
          </label>
          <RoleMappings
            what="LDAP group DN"
            placeholder="CN=VMS Operators,OU=Groups,DC=corp,DC=example"
            mappings={d.ldap.roleMappings ?? []}
            groups={groups.data ?? []}
            onChange={(m) => setLdap({ roleMappings: m })}
          />
          <LdapTest settings={d.ldap} />
        </section>

        <section className="card" aria-labelledby="oidc-h">
          <h2 id="oidc-h">Single sign-on (OpenID Connect)</h2>
          <label className="check">
            <input type="checkbox" checked={d.oidc.enabled} onChange={(e) => setOidc({ enabled: e.target.checked })} />
            Offer single sign-on on the sign-in screen
          </label>
          <label className="field">
            <span>Button text</span>
            <input type="text" value={d.oidc.displayName ?? ""} onChange={(e) => setOidc({ displayName: e.target.value })} placeholder="Sign in with your company account" />
          </label>
          <label className="field">
            <span>Issuer URL</span>
            <input type="text" value={d.oidc.issuer} onChange={(e) => setOidc({ issuer: e.target.value })} placeholder="https://login.example.com/tenant/v2.0" required={d.oidc.enabled} />
          </label>
          <label className="field">
            <span>Client ID</span>
            <input type="text" value={d.oidc.clientId} onChange={(e) => setOidc({ clientId: e.target.value })} required={d.oidc.enabled} autoComplete="off" />
          </label>
          <SecretField label="Client secret" has={!!d.oidc.hasClientSecret} value={d.oidc.clientSecret ?? ""} onChange={(v) => setOidc({ clientSecret: v })} />
          <label className="field">
            <span>Scopes</span>
            <input
              type="text"
              value={(d.oidc.scopes ?? []).join(" ")}
              onChange={(e) => setOidc({ scopes: e.target.value.split(/[\s,]+/).filter(Boolean) })}
              placeholder="openid profile email"
            />
          </label>
          <div className="grid-2">
            <label className="field">
              <span>Username claim</span>
              <input type="text" value={d.oidc.usernameClaim ?? ""} onChange={(e) => setOidc({ usernameClaim: e.target.value })} placeholder="preferred_username" />
            </label>
            <label className="field">
              <span>Groups claim</span>
              <input type="text" value={d.oidc.groupsClaim ?? ""} onChange={(e) => setOidc({ groupsClaim: e.target.value })} placeholder="groups" />
            </label>
          </div>
          <label className="field">
            <span>This server's public URL</span>
            <input type="text" value={d.oidc.publicUrl ?? ""} onChange={(e) => setOidc({ publicUrl: e.target.value })} placeholder="https://vms.example.com" />
            <small>
              Register <span className="mono">{(d.oidc.publicUrl || window.location.origin).replace(/\/+$/, "")}/api/v2/auth/oidc/callback</span> as the redirect URI
              with your identity provider. Empty: taken from each request.
            </small>
          </label>
          <RoleMappings what="Group or role claim value" placeholder="vms-operators" mappings={d.oidc.roleMappings ?? []} groups={groups.data ?? []} onChange={(m) => setOidc({ roleMappings: m })} />
          <p className="muted small">To try single sign-on, save, then sign out and use the button on the sign-in screen.</p>
        </section>
      </div>
      <div className="sticky-actions">
        <button type="button" className="btn" disabled={busy} onClick={() => (loaded.data && setD(loaded.data), setSaved(false), setError(null))}>
          Discard changes
        </button>
        <button className="btn primary" disabled={busy}>
          {busy ? "Saving…" : "Save settings"}
        </button>
      </div>
    </form>
  );
}

function Head() {
  return (
    <div className="page-head">
      <div className="grow">
        <h1>Directory</h1>
        <div className="sub">Let people sign in with their company accounts. Local users keep working.</div>
      </div>
    </div>
  );
}

/** Write-only secret: never shown; empty keeps the saved one. */
function SecretField({ label, has, value, onChange }: { label: string; has: boolean; value: string; onChange: (v: string) => void }) {
  return (
    <label className="field">
      <span>{label}</span>
      <input type="password" value={value} onChange={(e) => onChange(e.target.value)} placeholder={has ? "Saved — type to replace" : "Not set"} autoComplete="new-password" />
      <small>{has ? "Stored encrypted and never shown. Leave empty to keep it." : "Stored encrypted and never shown again."}</small>
    </label>
  );
}

function RoleMappings({
  what,
  placeholder,
  mappings,
  groups,
  onChange,
}: {
  what: string;
  placeholder: string;
  mappings: RoleMapping[];
  groups: CameraGroup[];
  onChange: (m: RoleMapping[]) => void;
}) {
  const update = (i: number, p: Partial<RoleMapping>) => onChange(mappings.map((m, j) => (j === i ? { ...m, ...p } : m)));
  return (
    <fieldset className="field mappings">
      <legend>Role mappings</legend>
      <small className="hint">The first mapping that matches one of the user's groups decides their role and cameras. No match: they can't sign in.</small>
      {mappings.map((m, i) => (
        <div key={i} className="mapping">
          <div className="mapping-row">
            <label className="field grow">
              <span>{what}</span>
              <input type="text" value={m.group} placeholder={placeholder} onChange={(e) => update(i, { group: e.target.value })} required />
            </label>
            <label className="field">
              <span>Role</span>
              <select value={m.role} onChange={(e) => update(i, { role: e.target.value as Role })}>
                {roles.map((r) => (
                  <option key={r}>{r}</option>
                ))}
              </select>
            </label>
            <button type="button" className="btn small danger" aria-label={`Remove mapping ${i + 1}`} onClick={() => onChange(mappings.filter((_, j) => j !== i))}>
              Remove
            </button>
          </div>
          {m.role !== "Admin" && (
            <div className="field">
              <span className="field-label">Camera groups (none = all cameras)</span>
              <ChipPicker label="Camera groups" items={groups} selected={m.groupIds ?? []} onChange={(ids) => update(i, { groupIds: ids })} empty="No camera groups; this mapping gives all cameras." />
            </div>
          )}
        </div>
      ))}
      <button type="button" className="btn small" onClick={() => onChange([...mappings, { group: "", role: "Viewer", groupIds: [] }])}>
        + Add mapping
      </button>
    </fieldset>
  );
}

function LdapTest({ settings }: { settings: LDAPSettings }) {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);
  const [error, setError] = useState<unknown>(null);
  return (
    <fieldset className="field ldap-test">
      <legend>Test with a user</legend>
      <small className="hint">Tries the settings above, without saving them, by signing this user in.</small>
      <div className="mapping-row">
        <label className="field grow">
          <span>Name</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
        </label>
        <label className="field grow">
          <span>Password</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" />
        </label>
        <button
          type="button"
          className="btn"
          disabled={busy || !name || !password || !settings.url}
          onClick={async () => {
            setBusy(true);
            setError(null);
            setResult(null);
            try {
              setResult(await v2.directory.testLdap(settings, name, password));
            } catch (e) {
              setError(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          {busy ? "Testing…" : "Test"}
        </button>
      </div>
      <div aria-live="polite">
        <ErrorBanner error={error} />
        {result && (
          <div className={result.ok ? "notice success" : "error"}>
            {result.message}
            {result.ok && result.role && <div>Role: {result.role}</div>}
            {result.groups && result.groups.length > 0 && <div className="mono">Groups: {result.groups.join("; ")}</div>}
          </div>
        )}
      </div>
    </fieldset>
  );
}
