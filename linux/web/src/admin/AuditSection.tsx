import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { AuditEntry, AuditVerify } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { Empty, ErrorBanner, Loading } from "./ui";

const pageSize = 100;
const ALL = "*";

export default function AuditSection() {
  const servers = useLoad(() => v2.servers.list(), []);
  const [server, setServer] = useState(ALL);
  const [items, setItems] = useState<AuditEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const [verify, setVerify] = useState<AuditVerify | null>(null);
  const [verifying, setVerifying] = useState(false);
  const [verifyError, setVerifyError] = useState<unknown>(null);
  const seq = useRef(0);

  const load = useCallback(
    async (from: string | undefined) => {
      const n = ++seq.current;
      setLoading(true);
      setError(null);
      try {
        const p = await v2.audit.page({ serverId: server === ALL ? undefined : server, cursor: from, limit: pageSize });
        if (n !== seq.current) return;
        setItems((prev) => (from ? [...(prev ?? []), ...p.items] : p.items));
        setCursor(p.nextCursor);
      } catch (e) {
        if (n === seq.current) setError(e);
      } finally {
        if (n === seq.current) setLoading(false);
      }
    },
    [server],
  );
  useEffect(() => {
    setItems(null);
    void load(undefined);
  }, [load]);

  const serverName = useMemo(() => new Map((servers.data ?? []).map((s) => [s.id, s.name])), [servers.data]);
  const nameOf = (id: string) => (id === "" ? "Management server" : (serverName.get(id) ?? id));
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!items || !q) return items ?? [];
    return items.filter((e) => [e.user, e.area, e.action, e.detail].join(" ").toLowerCase().includes(q));
  }, [items, query]);

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Audit</h1>
          <div className="sub">Every entry is hash-chained to the one before it; recording servers forward their own chains.</div>
        </div>
        <input type="text" className="head-input" placeholder="Filter loaded entries…" aria-label="Filter loaded entries" value={query} onChange={(e) => setQuery(e.target.value)} />
        <select className="head-input" aria-label="Server" value={server} onChange={(e) => setServer(e.target.value)}>
          <option value={ALL}>All servers</option>
          <option value="">Management server</option>
          {(servers.data ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <button
          className="btn"
          disabled={verifying}
          onClick={async () => {
            setVerifying(true);
            setVerifyError(null);
            try {
              setVerify(await v2.audit.verify());
            } catch (e) {
              setVerifyError(e);
            } finally {
              setVerifying(false);
            }
          }}
        >
          {verifying ? "Verifying…" : "Verify"}
        </button>
      </div>
      <ErrorBanner error={verifyError} />
      {verify && (
        <div className={verify.ok && verify.servers.every((s) => s.ok) ? "notice success" : "error"} role="status">
          {verify.ok ? (
            <div>✓ Management server chain intact: {verify.entries} entries verified.</div>
          ) : (
            <div>
              Management server chain BROKEN{verify.brokenAt ? ` at entry ${verify.brokenAt}` : ""}: the audit database was changed outside Sentinel.
            </div>
          )}
          {verify.servers.map((s) => (
            <div key={s.serverId}>
              {s.ok ? "✓" : "✗"} {nameOf(s.serverId)}: {s.ok ? `${s.entries} entries verified` : "chain BROKEN"}
            </div>
          ))}
        </div>
      )}
      <ErrorBanner error={error} onRetry={() => load(items?.length ? cursor : undefined)} />
      <div className="card table-wrap">
        {items === null ? (
          error ? null : <Loading />
        ) : shown.length === 0 ? (
          <Empty icon="☰" title={items.length ? "No matching entries" : "No entries"}>
            {items.length && cursor ? "Load more to search further back." : undefined}
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Time</th>
                <th>User</th>
                <th>Server</th>
                <th>Area</th>
                <th>Action</th>
                <th>Detail</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((e) => (
                <tr key={`${e.serverId}/${e.id}`}>
                  <td className="mono nowrap">{new Date(e.at).toLocaleString()}</td>
                  <td>{e.user}</td>
                  <td>{nameOf(e.serverId)}</td>
                  <td>
                    <span className="badge blue">{e.area}</span>
                  </td>
                  <td>{e.action}</td>
                  <td className="mono">{e.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {items !== null && (
          <div className="load-more">
            {loading && items.length > 0 ? (
              <Loading label="Loading more…" />
            ) : cursor ? (
              <button className="btn" onClick={() => load(cursor)}>
                Load older entries
              </button>
            ) : items.length > 0 ? (
              <span className="muted small">Start of the log · {items.length} entries</span>
            ) : null}
          </div>
        )}
      </div>
    </>
  );
}
