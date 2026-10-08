import { useEffect, useState, type FormEvent } from "react";
import { api, roles, type Role, type User } from "../api";

const roleHelp: Record<Role, string> = {
  Admin: "Full control: cameras, users, settings",
  Supervisor: "Operations + audit log",
  Operator: "Live, playback, alarms",
  Viewer: "Live and playback only",
};

export default function UsersPage({ me }: { me: User }) {
  const [users, setUsers] = useState<User[]>([]);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("Operator");
  const [password, setPassword] = useState("");

  const load = () => api.users().then(setUsers).catch((e) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  async function add(e: FormEvent) {
    e.preventDefault();
    setError("");
    try {
      await api.addUser(name, role, password);
      setAdding(false);
      setName("");
      setPassword("");
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function remove(u: User) {
    setError("");
    try {
      await api.deleteUser(u.id);
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Users</h1>
          <div className="sub">Roles match the Mac app</div>
        </div>
        <button className="btn primary" onClick={() => setAdding(true)}>
          + Add user
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      <div className="card table-wrap">
        <table className="list">
          <thead>
            <tr>
              <th>Name</th>
              <th>Role</th>
              <th>Created</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>
                  <strong>{u.name}</strong> {u.id === me.id && <span className="badge blue">You</span>}
                </td>
                <td>
                  {u.role}
                  <div className="mono">{roleHelp[u.role]}</div>
                </td>
                <td className="mono">{new Date(u.createdAt * 1000).toLocaleDateString()}</td>
                <td style={{ textAlign: "right" }}>
                  {u.id !== me.id && (
                    <button className="btn small danger" onClick={() => remove(u)}>
                      Remove
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {adding && (
        <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && setAdding(false)}>
          <form className="modal" onSubmit={add}>
            <h2>Add user</h2>
            {error && <div className="error">{error}</div>}
            <label className="field">
              <span>Name</span>
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} required autoFocus />
            </label>
            <label className="field">
              <span>Role</span>
              <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
                {roles.map((r) => (
                  <option key={r} value={r}>
                    {r} — {roleHelp[r]}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>Password</span>
              <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
              <small>At least 8 characters. Share it with them securely.</small>
            </label>
            <div className="actions">
              <button type="button" className="btn" onClick={() => setAdding(false)}>
                Cancel
              </button>
              <button className="btn primary">Add user</button>
            </div>
          </form>
        </div>
      )}
    </>
  );
}
