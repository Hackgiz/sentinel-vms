import { useState } from "react";
import { useLoad } from "../shared/hooks";
import { isUpload, toMs, uploadSubject, type SourcedEvidence } from "../shared/phoneCameras";
import UploadLabel from "../shared/UploadLabel";
import { v2 } from "./client";
import { formatBytes } from "./client";
import EvidencePlayer, { evidenceFileName } from "./EvidencePlayer";
import { Empty, ErrorBanner, Loading, useLiveReload } from "./ui";

type Check = "busy" | "ok" | "bad";

/** The evidence file on this server; the page's session cookie authorizes it. */
export const evidenceFileUrl = (id: string) => `/api/v2/evidence/${encodeURIComponent(id)}/download`;

/** Evidence Locks: every locked clip and uploaded video, and its recorded SHA-256, read-only. */
export default function EvidenceSection() {
  const evidence = useLoad(() => v2.evidence.list() as Promise<SourcedEvidence[]>, []);
  useLiveReload(evidence.reload, ["alarm.updated"]);
  const [checks, setChecks] = useState<Record<string, Check>>({});
  const [error, setError] = useState<unknown>(null);
  const [playing, setPlaying] = useState<SourcedEvidence | null>(null);

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
          <div className="sub">Clips locked as evidence and videos uploaded from iPhones, read-only. Verify re-hashes a file on its recording server and compares it with the hash taken at lock time.</div>
        </div>
      </div>
      <ErrorBanner error={evidence.error ?? error} onRetry={evidence.error ? evidence.reload : undefined} />
      <div className="card table-wrap">
        {evidence.data === null ? (
          evidence.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="🔒" title="No evidence locked" />
        ) : (
          <EvidenceTable list={list} checks={checks} onVerify={(id) => void verify(id)} onPlay={setPlaying} />
        )}
      </div>
      {playing && <EvidencePlayer title={playing.title || playing.caseId} src={evidenceFileUrl(playing.id)} onClose={() => setPlaying(null)} />}
    </>
  );
}

export function EvidenceTable({
  list,
  checks,
  onVerify,
  onPlay,
}: {
  list: SourcedEvidence[];
  checks: Record<string, Check>;
  onVerify: (id: string) => void;
  onPlay: (e: SourcedEvidence) => void;
}) {
  return (
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
        {list.map((e) => {
          const upload = isUpload(e);
          const recorded = upload ? toMs(e.recordedAt) : 0;
          return (
            <tr key={e.id}>
              <td className="mono nowrap">{e.caseId}</td>
              <td>
                {upload ? (
                  <>
                    {uploadSubject(e)}
                    <div>
                      <UploadLabel evidence={e} />
                    </div>
                    {e.title && e.cameraName && <div className="muted small">{e.title}</div>}
                  </>
                ) : (
                  <>
                    {e.cameraName}
                    <div className="muted small">{e.title}</div>
                  </>
                )}
              </td>
              <td className="nowrap">{upload ? (recorded ? new Date(recorded).toLocaleString() : e.range || "—") : e.range}</td>
              <td className="nowrap">{formatBytes(e.size)}</td>
              <td className="nowrap">
                {new Date(e.lockedAt * 1000).toLocaleString()}
                <div className="muted small">{e.lockedBy}</div>
              </td>
              <td className="mono small" title={e.sha256}>
                {e.sha256.slice(0, 16)}…
              </td>
              <td className="nowrap">
                <button className="btn small" onClick={() => onPlay(e)}>
                  Play
                </button>{" "}
                <a className="btn small" href={evidenceFileUrl(e.id)} download={evidenceFileName(e.caseId, e.fileName)}>
                  Download
                </a>{" "}
                <button className="btn small" disabled={checks[e.id] === "busy"} onClick={() => onVerify(e.id)}>
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
          );
        })}
      </tbody>
    </table>
  );
}
