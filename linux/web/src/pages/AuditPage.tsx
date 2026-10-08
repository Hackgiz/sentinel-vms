import { useEffect, useMemo, useState } from "react";
import { api, type AuditEntry } from "../api";

export default function AuditPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [query, setQuery] = useState("");
  const [verify, setVerify] = useState<{ checked: number; intact: boolean } | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    api.audit().then(setEntries).catch((e) => setError(e.message));
  }, []);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return entries;
    return entries.filter((e) => [e.user, e.area, e.action, e.detail].join(" ").toLowerCase().includes(q));
  }, [entries, query]);

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Audit Log</h1>
          <div className="sub">Every entry is hash-chained to the one before it</div>
        </div>
        <input type="text" placeholder="Filter…" value={query} onChange={(e) => setQuery(e.target.value)} style={{ width: 220 }} />
        <button className="btn" onClick={() => api.verifyAudit().then(setVerify).catch((e) => setError(e.message))}>
          Verify chain
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      {verify &&
        (verify.intact ? (
          <div className="notice">✓ Chain intact — {verify.checked} entries verified.</div>
        ) : (
          <div className="error">Chain BROKEN — the audit database was modified outside Sentinel.</div>
        ))}
      <div className="card table-wrap">
        <table className="list">
          <thead>
            <tr>
              <th>Time</th>
              <th>User</th>
              <th>Area</th>
              <th>Action</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((e) => (
              <tr key={e.id}>
                <td className="mono">{new Date(e.time * 1000).toLocaleString()}</td>
                <td>{e.user}</td>
                <td>
                  <span className="badge blue">{e.area}</span>
                </td>
                <td>{e.action}</td>
                <td className="mono">{e.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
