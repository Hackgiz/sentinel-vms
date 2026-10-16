import { useMemo, useState } from "react";
import type { PermissionInfo, RoleDefinition } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";

/** Reveals the Roles page: the server requires ManageUsers. */
export const rolesPermission = "manageUsers";

/** Catalog entries grouped, keeping the catalog's order. */
export function groupPermissions(catalog: PermissionInfo[]): [string, PermissionInfo[]][] {
  const groups = new Map<string, PermissionInfo[]>();
  for (const p of catalog) groups.set(p.group, [...(groups.get(p.group) ?? []), p]);
  return [...groups.entries()];
}

export default function RolesSection() {
  const roles = useLoad(() => v2.roles.list(), []);
  const catalog = useLoad(() => v2.roles.permissions(), []);
  const [editing, setEditing] = useState<RoleDefinition | "new" | null>(null);
  const [deleting, setDeleting] = useState<RoleDefinition | null>(null);
  const list = roles.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Roles</h1>
          <div className="sub">What each role may do. The four built-in roles are fixed; add your own for finer control.</div>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + New role
        </button>
      </div>
      <ErrorBanner error={roles.error || catalog.error} onRetry={() => (roles.reload(), catalog.reload())} />
      <div className="card table-wrap">
        {roles.data === null ? (
          roles.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="◎" title="No roles" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Role</th>
                <th>Description</th>
                <th>Permissions</th>
                <th>Users</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((r) => (
                <tr key={r.id}>
                  <td>
                    <strong>{r.name}</strong>{" "}
                    {r.builtin && (
                      <span className="badge blue" title="Built-in roles can't be changed or deleted">
                        <span aria-hidden="true">🔒 </span>Built-in
                      </span>
                    )}
                  </td>
                  <td>{r.description}</td>
                  <td className="mono">{r.permissions.length}</td>
                  <td className="mono">{r.userCount}</td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(r)}>
                      {r.builtin ? "View" : "Edit"}
                    </button>
                    {!r.builtin && (
                      <button className="btn small danger" onClick={() => setDeleting(r)}>
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
        <RoleEditor
          role={editing === "new" ? null : editing}
          catalog={catalog.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void roles.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete role"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.roles.remove(deleting.id);
            setDeleting(null);
            void roles.reload();
          }}
        >
          <p>Roles that users still hold can't be deleted: the server refuses and says how many users to reassign first.</p>
        </Confirm>
      )}
    </>
  );
}

function RoleEditor({ role, catalog, onClose, onSaved }: { role: RoleDefinition | null; catalog: PermissionInfo[]; onClose: () => void; onSaved: () => void }) {
  const readOnly = !!role?.builtin;
  const [name, setName] = useState(role?.name ?? "");
  const [description, setDescription] = useState(role?.description ?? "");
  const [perms, setPerms] = useState<Set<string>>(new Set(role?.permissions ?? ["viewLive"]));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const grouped = useMemo(() => groupPermissions(catalog), [catalog]);

  const toggle = (id: string) =>
    setPerms((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  async function save() {
    if (readOnly) return onClose();
    setBusy(true);
    setError(null);
    const body = { name: name.trim(), description: description.trim(), permissions: catalog.filter((p) => perms.has(p.id)).map((p) => p.id) };
    try {
      if (role) await v2.roles.update(role.id, body);
      else await v2.roles.create(body);
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  return (
    <Modal
      title={role ? (readOnly ? `${role.name} (built-in)` : `Edit ${role.name}`) : "New role"}
      wide
      onClose={onClose}
      onSubmit={save}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {readOnly ? "Close" : "Cancel"}
          </button>
          {!readOnly && (
            <button className="btn primary" disabled={busy || !name.trim()}>
              {role ? "Save" : "Create role"}
            </button>
          )}
        </>
      }
    >
      <ErrorBanner error={error} />
      {readOnly && <div className="notice">Built-in roles can't be changed. Create a custom role to adjust permissions.</div>}
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} onChange={(e) => setName(e.target.value)} required autoFocus={!readOnly} disabled={readOnly} autoComplete="off" />
        {role && !readOnly && <small>Renaming moves the users who hold this role along.</small>}
      </label>
      <label className="field">
        <span>Description</span>
        <input type="text" value={description} onChange={(e) => setDescription(e.target.value)} disabled={readOnly} autoComplete="off" />
      </label>
      {grouped.map(([group, items]) => (
        <fieldset key={group} className="field">
          <legend>{group}</legend>
          {items.map((p) => (
            <label key={p.id} className="check">
              <input type="checkbox" checked={perms.has(p.id)} onChange={() => toggle(p.id)} disabled={readOnly} />
              <span>
                {p.label} <small className="muted">{p.description}</small>
              </span>
            </label>
          ))}
        </fieldset>
      ))}
    </Modal>
  );
}
