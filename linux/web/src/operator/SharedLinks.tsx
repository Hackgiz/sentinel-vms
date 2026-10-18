// Shared links: every expiring link made from evidence, with its use count
// and a revoke button. Loads on mount and when `refreshKey` changes (no polling).
import { useCallback, useEffect, useRef, useState } from "react";
import { Confirm } from "../admin/ui";
import { errorMessage, type ShareLink } from "../shared/api";
import { useSigned } from "./session";
import { sortShares, usesText } from "./shareModel";

export default function SharedLinks({ refreshKey }: { refreshKey: number }) {
  const { session, can } = useSigned();
  const client = session.client;
  const [links, setLinks] = useState<ShareLink[] | null>(null);
  const [error, setError] = useState("");
  const [revoking, setRevoking] = useState<ShareLink | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const load = useCallback(() => {
    client.shareLinks
      .list()
      .then((l) => {
        if (!alive.current) return;
        setLinks(sortShares(l));
        setError("");
      })
      .catch((e) => alive.current && setError(errorMessage(e)));
  }, [client]);
  useEffect(load, [load, refreshKey]);

  if (!can("exportEvidence")) return null;
  return (
    <section className="op-shared-links" aria-label="Shared links">
      <h2>Shared links</h2>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <div className="card table-wrap">
        <table className="list">
          <thead>
            <tr>
              <th>Case</th>
              <th>Created</th>
              <th>Expires</th>
              <th>Uses</th>
              <th>Status</th>
              <th>Note</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {links === null && !error && (
              <tr>
                <td colSpan={7} className="muted">
                  Loading…
                </td>
              </tr>
            )}
            {links?.length === 0 && (
              <tr>
                <td colSpan={7} className="muted">
                  No shared links. Use “Share…” on a case above.
                </td>
              </tr>
            )}
            {links?.map((l) => (
              <tr key={l.id}>
                <td>
                  <strong className="mono">{l.caseId || "(released)"}</strong>
                  {l.title && <div className="muted small">{l.title}</div>}
                </td>
                <td>
                  <div>{new Date(l.createdAt).toLocaleString()}</div>
                  <div className="muted small">{l.createdBy}</div>
                </td>
                <td>{new Date(l.expiresAt).toLocaleString()}</td>
                <td>{usesText(l)}</td>
                <td>
                  <span className={`badge ${l.status === "active" ? "green" : "amber"}`}>{l.status}</span>
                </td>
                <td className="small">{l.note}</td>
                <td>
                  {l.status === "active" && (
                    <button className="btn small danger" onClick={() => setRevoking(l)}>
                      Revoke
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {revoking && (
        <Confirm
          title="Revoke this link?"
          confirmLabel="Revoke link"
          onCancel={() => setRevoking(null)}
          run={async () => {
            await client.shareLinks.revoke(revoking.id);
            setRevoking(null);
            load();
          }}
        >
          Anyone holding the link to {revoking.caseId} will get “no longer available” straight away.
        </Confirm>
      )}
    </section>
  );
}
