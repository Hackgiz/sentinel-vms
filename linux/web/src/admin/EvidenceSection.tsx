import { useState } from "react";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { formatBytes } from "./client";
import { Empty, ErrorBanner, Loading, useLiveReload } from "./ui";

type Check = "busy" | "ok" | "bad";

/** Evidence Locks: every locked clip and its recorded SHA-256, read-only. */
export default function EvidenceSection() {
  const evidence = useLoad(() => v2.evidence.list(), []);
  useLiveReload(evidence.reload, ["alarm.updated"]);
  const [checks, setChecks] = useState<Record<string, Check>>({});
  const [error, setError] = useState<unknown>(null);

  async function verify(id: string) {
    setChecks((m) => ({ ...m, [id]: "busy" }));
    setError(null);
    try {
      const r = await v2.evidence.verify(id);
      setChecks((m) => ({ ...m, [id]: r.ok ? "ok" : "bad" }));
    } catch (e) {
      setChecks((m) => {
        const { [id]: _gone, ...rest } = m;
        return rest;
      });
      setError(e);
    }
  }
  const list = evidence.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Evidence locks</h1>
          <div className="sub">Clips locked as evidence, read-only. Verify re-hashes a file on its recording server and compares it with the hash taken at lock time.</div>
        </div>
      </div>
      <ErrorBanner error={evidence.error ?? error} onRetry={evidence.error ? evidence.reload : undefined} />
      <div className="card table-wrap">
        {evidence.data === null ? (
          evidence.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="🔒" title="No evidence locked" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Case</th>
                <th>Camera</th>
                <th>Range</th>
                <th>Size</th>
                <th>Locked</th>
                <th>SHA-256</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((e) => (
                <tr key={e.id}>
                  <td className="mono nowrap">{e.caseId}</td>
                  <td>
                    {e.cameraName}
                    <div className="muted small">{e.title}</div>
                  </td>
                  <td className="nowrap">{e.range}</td>
                  <td className="nowrap">{formatBytes(e.size)}</td>
                  <td className="nowrap">
                    {new Date(e.lockedAt * 1000).toLocaleString()}
                    <div className="muted small">{e.lockedBy}</div>
                  </td>
                  <td className="mono small" title={e.sha256}>
                    {e.sha256.slice(0, 16)}…
                  </td>
                  <td className="nowrap">
                    <button className="btn small" disabled={checks[e.id] === "busy"} onClick={() => void verify(e.id)}>
                      {checks[e.id] === "busy" ? "Verifying…" : "Verify"}
                    </button>
                    {checks[e.id] === "ok" && (
                      <span className="badge green" role="status" title="The file matches its recorded SHA-256">
                        ✓ Intact
                      </span>
                    )}
                    {checks[e.id] === "bad" && (
                      <span className="badge red" role="status" title="The file does not match its recorded SHA-256, or is missing">
                        ✗ Mismatch
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
