import { useEffect, useMemo, useState } from "react";
import { api, type AuditEntry } from "../api";
import { PageHeader } from "../shared/Cards"; // WAVE21

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
      <PageHeader title="Audit Log" subtitle="Every entry is hash-chained to the one before it">
        <input type="text" placeholder="Filter…" value={query} onChange={(e) => setQuery(e.target.value)} style={{ width: 220 }} />
        <button className="btn" onClick={() => api.verifyAudit().then(setVerify).catch((e) => setError(e.message))}>
          Verify chain
        </button>
      </PageHeader>
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
