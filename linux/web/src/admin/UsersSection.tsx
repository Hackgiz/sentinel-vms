import { useId, useMemo, useState } from "react";
import { roles, type CameraGroup, type Role, type RoleDefinition, type User, type UserScope } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { ChipPicker, Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";

export const roleHelp: Record<string, string> = {
  Admin: "Full control: servers, cameras, users, settings",
  Supervisor: "Operations plus audit log and health",
  Operator: "Live, playback, alarms, export",
  Viewer: "Live and playback only",
};

const sourceLabel: Record<User["source"], string> = { local: "Local", ldap: "LDAP", oidc: "Single sign-on" };

export default function UsersSection({ meId }: { meId: string }) {
  const users = useLoad(() => v2.users.list(), []);
  const groups = useLoad(() => v2.groups.list(), []);
  const roleDefs = useLoad(() => v2.roles.list(), []);
  const [editing, setEditing] = useState<User | "new" | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const groupName = useMemo(() => new Map((groups.data ?? []).map((g) => [g.id, g.name])), [groups.data]);
  const list = users.data ?? [];

  const scopeText = (u: User) =>
    u.role === "Admin" || u.scope.allCameras
      ? "All cameras"
      : u.scope.groupIds.length === 0
        ? "No cameras"
        : u.scope.groupIds.map((g) => groupName.get(g) ?? "Removed group").join(", ");

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Users</h1>
          <div className="sub">Who can sign in, what their role allows, and which cameras it applies to</div>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + Add user
        </button>
      </div>
      <ErrorBanner error={users.error} onRetry={users.reload} />
      <div className="card table-wrap">
        {users.data === null ? (
          users.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="◎" title="No users" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Role</th>
                <th>Cameras</th>
                <th>Created</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((u) => (
                <tr key={u.id}>
                  <td>
                    <strong>{u.name}</strong> {u.id === meId && <span className="badge blue">You</span>}{" "}
                    <span className={`badge ${u.source === "local" ? "blue" : "amber"}`} title={u.source === "local" ? "Password kept here" : "Signs in through the directory"}>
                      {sourceLabel[u.source]}
                    </span>
                  </td>
                  <td>
                    {u.role}
                    <div className="mono">{roleHelp[u.role] ?? roleDefs.data?.find((d) => d.name === u.role)?.description}</div>
                  </td>
                  <td>{scopeText(u)}</td>
                  <td className="mono">{new Date(u.createdAt).toLocaleDateString()}</td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(u)}>
                      Edit
                    </button>
                    {u.id !== meId && (
                      <button className="btn small danger" onClick={() => setDeleting(u)}>
                        Delete…
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {editing && (
        <UserEditor
          user={editing === "new" ? null : editing}
          isMe={editing !== "new" && editing.id === meId}
          groups={groups.data ?? []}
          roleDefs={roleDefs.data}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void users.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete user"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.users.remove(deleting.id);
            setDeleting(null);
            void users.reload();
          }}
        >
          <p>They are signed out everywhere and can't sign in again. Their actions stay in the audit log.</p>
          {deleting.source !== "local" && (
            <p className="muted small">They come back on their next sign-in while the directory still lets them in. Remove them there too.</p>
          )}
        </Confirm>
      )}
    </>
  );
}

function UserEditor({ user, isMe, groups, roleDefs, onClose, onSaved }: { user: User | null; isMe: boolean; groups: CameraGroup[]; roleDefs: RoleDefinition[] | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(user?.name ?? "");
  const [role, setRole] = useState<Role>(user?.role ?? "Operator");
  const [password, setPassword] = useState("");
  const [scope, setScope] = useState<UserScope>(user?.scope ?? { allCameras: true, groupIds: [] });
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const scopeName = useId();
  // Roles come from the server (built-ins and custom); the static four are the fallback.
  const roleOptions = useMemo(() => {
    const opts = roleDefs ? roleDefs.map((d) => ({ name: d.name, help: roleHelp[d.name] ?? d.description })) : roles.map((r) => ({ name: r, help: roleHelp[r] }));
    return opts.some((o) => o.name === role) ? opts : [...opts, { name: role, help: "" }];
  }, [roleDefs, role]);
  const local = !user || user.source === "local";
  const passwordProblem = password && password.length < 8 ? "At least 8 characters." : "";
  const scopeProblem = role !== "Admin" && !scope.allCameras && scope.groupIds.length === 0 ? "Pick at least one group, or all cameras." : "";

  async function save() {
    if (passwordProblem || scopeProblem) return;
    setBusy(true);
    setError(null);
    const sc: UserScope = role === "Admin" ? { allCameras: true, groupIds: [] } : scope;
    try {
      if (user) {
        const body: { role?: Role; password?: string; scope?: UserScope } = {};
        if (role !== user.role) body.role = role;
        if (local && password) body.password = password;
        if (sc.allCameras !== user.scope.allCameras || [...sc.groupIds].sort().join() !== [...user.scope.groupIds].sort().join()) body.scope = sc;
        if (Object.keys(body).length) await v2.users.update(user.id, body);
      } else {
        await v2.users.create({ name: name.trim(), role, password, scope: sc });
      }
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  return (
    <Modal
      title={user ? `Edit ${user.name}` : "Add user"}
      wide
      onClose={onClose}
      onSubmit={save}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !!passwordProblem || !!scopeProblem || (!user && (!name.trim() || !password))}>
            {user ? "Save" : "Add user"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      {!user && (
        <label className="field">
          <span>Name</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} required autoFocus autoComplete="off" />
          <small>Directory users don't need adding: they appear here after their first sign-in.</small>
        </label>
      )}
      {user && user.source !== "local" && (
        <div className="notice">
          {user.name} signs in through {sourceLabel[user.source]}. Their password is managed there, and their role may be set again from the directory's role mappings at
          the next sign-in.
        </div>
      )}
      <label className="field">
        <span>Role</span>
        <select value={role} onChange={(e) => setRole(e.target.value as Role)} disabled={isMe}>
          {roleOptions.map((r) => (
            <option key={r.name} value={r.name}>
              {r.name}
              {r.help ? ` — ${r.help}` : ""}
            </option>
          ))}
        </select>
        {isMe && <small>You can't change your own role.</small>}
      </label>
      <fieldset className="field scope-field">
        <legend>Cameras</legend>
        {role === "Admin" ? (
          <small>Admins always have every camera.</small>
        ) : (
          <>
            <label className="check">
              <input type="radio" name={scopeName} checked={scope.allCameras} onChange={() => setScope({ ...scope, allCameras: true })} />
              All cameras
            </label>
            <label className="check">
              <input type="radio" name={scopeName} checked={!scope.allCameras} onChange={() => setScope({ ...scope, allCameras: false })} />
              Only cameras in these groups
            </label>
            {!scope.allCameras && (
              <ChipPicker label="Groups" items={groups} selected={scope.groupIds} onChange={(ids) => setScope({ ...scope, groupIds: ids })} empty="No groups yet. Create them under Groups." />
            )}
            {scopeProblem && <small className="field-problem">{scopeProblem}</small>}
          </>
        )}
      </fieldset>
      {local && (
        <label className="field">
          <span>{user ? "New password" : "Password"}</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required={!user}
            placeholder={user ? "Leave empty to keep" : ""}
            aria-invalid={!!passwordProblem}
          />
          <small>{passwordProblem || (user ? "Changing it signs them out everywhere." : "At least 8 characters. Share it with them securely.")}</small>
        </label>
      )}
    </Modal>
  );
}
