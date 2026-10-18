// Exports: lock any camera and time range as evidence, and manage the cases
// (including those locked from alarms). The create POST is synchronous, so a
// busy row stands in for the case while the recording server cuts the clip;
// finishing emits a notice for the shell's banner strip.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Confirm } from "../admin/ui";
import { errorMessage, type EvidenceCase } from "../shared/api";
import { useData } from "./data";
import { exportReencodes } from "../shared/zones";
import { saveBlob } from "./export";
import { batchLabel, composeNote, exportBlocker, formatBytes, fromLocalInput, rangeText, toLocalInput } from "./exportsModel";
import ExportOptionsFields, { ExportOptionsNote } from "./ExportOptionsFields";
import { noOptions, optionsBody, type ExportOptions } from "./exportOptionsModel";
import { notify } from "./notices";
import { useSigned } from "./session";
import ShareDialog from "./ShareDialog";
import SharedLinks from "./SharedLinks";

type Check = "busy" | "ok" | "bad";

interface Busy {
  index: number;
  total: number;
  camera: string;
}

const toggle = (a: string[], v: string) => (a.includes(v) ? a.filter((x) => x !== v) : [...a, v]);

export default function ExportsView() {
  const { session, can } = useSigned();
  const client = session.client;
  const data = useData();
  const { exportDraft, setExportDraft } = data;

  const [cases, setCases] = useState<EvidenceCase[] | null>(null);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const [cameraIds, setCameraIds] = useState<string[]>([]);
  const [from, setFrom] = useState(0);
  const [to, setTo] = useState(0);
  const [title, setTitle] = useState("");
  const [note, setNote] = useState("");
  const [options, setOptions] = useState<ExportOptions>(noOptions);
  const [busy, setBusy] = useState<Busy | null>(null);
  const [checks, setChecks] = useState<Record<string, Check>>({});
  const [working, setWorking] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<EvidenceCase | null>(null);
  const [copied, setCopied] = useState("");
  const [sharing, setSharing] = useState<EvidenceCase | null>(null);
  const [shareTick, setShareTick] = useState(0);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const load = useCallback(() => {
    client.evidence
      .list()
      .then((c) => {
        if (!alive.current) return;
        setCases(c);
        setError("");
      })
      .catch((e) => alive.current && setError(errorMessage(e)));
  }, [client]);
  useEffect(load, [load]);

  // Arriving from Playback's "Lock as evidence…": take the range, once.
  useEffect(() => {
    if (!exportDraft) return;
    setCameraIds([exportDraft.cameraId]);
    setFrom(exportDraft.from);
    setTo(exportDraft.to);
    setExportDraft(null);
  }, [exportDraft, setExportDraft]);

  const cameras = data.cameras ?? [];
  const nameOf = (id: string) => data.camera(id)?.name ?? "Camera";
  const blocker = exportBlocker(cameraIds, from, to);
  const canExport = can("exportEvidence");
  const canDelete = can("manageUsers");
  const sorted = useMemo(() => [...cameras].sort((a, b) => a.name.localeCompare(b.name)), [cameras]);

  async function create() {
    if (blocker || busy) return;
    const ids = cameraIds;
    const label = batchLabel(ids.length, Date.now());
    const fullNote = composeNote(label, note);
    const caseTitle = title.trim() || "Clip Export";
    setError("");
    setStatus("");
    let made = 0;
    const failures: string[] = [];
    for (let i = 0; i < ids.length; i++) {
      if (!alive.current) return;
      setBusy({ index: i + 1, total: ids.length, camera: nameOf(ids[i]) });
      try {
        await client.evidence.create({ cameraId: ids[i], fromMs: from, toMs: to, title: caseTitle, note: fullNote || undefined, ...optionsBody(options) });
        made++;
      } catch (e) {
        failures.push(`${nameOf(ids[i])}: ${errorMessage(e)}`);
      }
    }
    if (!alive.current) {
      notify(failures.length ? "error" : "success", summary(made, failures.length));
      return;
    }
    setBusy(null);
    if (failures.length) setError(failures.join("\n"));
    const text = summary(made, failures.length);
    setStatus(text);
    notify(failures.length ? "error" : "success", text);
    if (made) {
      setTitle("");
      setNote("");
    }
    load();
  }

  async function download(c: EvidenceCase) {
    setWorking(c.id);
    try {
      saveBlob(`${c.caseId}.mp4`, await client.evidence.download(c.id));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setWorking(null);
    }
  }

  async function downloadPackage(c: EvidenceCase) {
    setWorking(c.id);
    try {
      saveBlob(`${c.caseId}-package.zip`, await client.evidence.package(c.id));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setWorking(null);
    }
  }

  async function verify(c: EvidenceCase) {
    setChecks((m) => ({ ...m, [c.id]: "busy" }));
    try {
      const r = await client.evidence.verify(c.id);
      setChecks((m) => ({ ...m, [c.id]: r.ok ? "ok" : "bad" }));
      if (!r.ok) notify("error", `Evidence ${c.caseId} does not match its recorded SHA-256.`);
    } catch (e) {
      setChecks((m) => {
        const { [c.id]: _gone, ...rest } = m;
        return rest;
      });
      setError(errorMessage(e));
    }
  }

  async function copy(c: EvidenceCase) {
    try {
      await navigator.clipboard.writeText(c.sha256);
      setCopied(c.id);
      setTimeout(() => setCopied((id) => (id === c.id ? "" : id)), 1500);
    } catch {
      setError("Couldn't copy to the clipboard.");
    }
  }

  return (
    <div className="op-events op-exports">
      {canExport ? (
        <section className="op-export-create" aria-label="New export">
          <h2>Lock footage as evidence</h2>
          <div className="op-toolbar">
            <details className="op-camsel">
              <summary className="btn small">{cameraIds.length ? `${cameraIds.length} camera${cameraIds.length > 1 ? "s" : ""}` : "Pick cameras"}</summary>
              <div className="op-menu op-camsel-list">
                {sorted.map((c) => (
                  <label key={c.id} className="check">
                    <input type="checkbox" checked={cameraIds.includes(c.id)} onChange={() => setCameraIds(toggle(cameraIds, c.id))} />
                    {c.name}
                  </label>
                ))}
                {!sorted.length && <span className="muted small">No cameras.</span>}
                {cameraIds.length > 0 && (
                  <button className="btn small" onClick={() => setCameraIds([])}>
                    Clear
                  </button>
                )}
              </div>
            </details>
            <input type="datetime-local" step={1} aria-label="From" value={toLocalInput(from)} onChange={(e) => setFrom(fromLocalInput(e.target.value))} />
            <span className="muted">to</span>
            <input type="datetime-local" step={1} aria-label="To" value={toLocalInput(to)} onChange={(e) => setTo(fromLocalInput(e.target.value))} />
          </div>
          <div className="op-toolbar">
            <input type="text" className="op-export-title" placeholder="Title (optional)" aria-label="Title" maxLength={120} value={title} onChange={(e) => setTitle(e.target.value)} />
            <input type="text" className="grow" placeholder="Note (optional)" aria-label="Note" maxLength={400} value={note} onChange={(e) => setNote(e.target.value)} />
            <ExportOptionsFields
              value={options}
              onChange={setOptions}
              cameraId={cameraIds.length === 1 ? cameraIds[0] : null}
              cameraName={cameraIds.length === 1 ? nameOf(cameraIds[0]) : undefined}
              disabled={!!busy}
            />
            <button className="btn primary" disabled={!!blocker || !!busy} title={blocker || "Cut each camera's range into the evidence vault"} onClick={() => void create()}>
              {busy ? "Working…" : cameraIds.length > 1 ? `Lock ${cameraIds.length} clips` : "Lock clip"}
            </button>
          </div>
          {cameraIds.length > 0 && (
            <div className="muted small op-pad">
              {cameraIds.map(nameOf).join(", ")}
              {blocker && ` — ${blocker}`}
            </div>
          )}
          {cameraIds.some((id) => exportReencodes(data.camera(id)?.masks)) && (
            <div className="muted small op-pad">Cameras with privacy masks are re-encoded to burn the masks in, so their clips take longer.</div>
          )}
          <ExportOptionsNote value={options} />
          {status && !error && <div className="muted small op-pad" role="status">{status}</div>}
        </section>
      ) : (
        <div className="muted op-pad">Your role can view cases but not create exports.</div>
      )}

      {error && (
        <div className="error" role="alert" style={{ whiteSpace: "pre-line" }}>
          {error}
        </div>
      )}

      <div className="card table-wrap">
        <table className="list">
          <thead>
            <tr>
              <th>Locked</th>
              <th>Camera and range</th>
              <th>Size</th>
              <th>SHA-256</th>
              <th>By</th>
              <th>Note</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {busy && (
              <tr className="op-export-busy" aria-live="polite">
                <td colSpan={7}>
                  <span className="spinner" aria-hidden="true" /> Locking {busy.camera} ({busy.index} of {busy.total})… the recording server is cutting the clip.
                </td>
              </tr>
            )}
            {cases === null && !error && (
              <tr>
                <td colSpan={7} className="muted">
                  Loading…
                </td>
              </tr>
            )}
            {cases?.length === 0 && !busy && (
              <tr>
                <td colSpan={7} className="muted">
                  No evidence yet. Lock a range above, or use “Lock as evidence…” on the Playback timeline.
                </td>
              </tr>
            )}
            {cases?.map((c) => (
              <tr key={c.id}>
                <td>
                  <div>{new Date(c.lockedAt).toLocaleString()}</div>
                  <div className="muted small mono">{c.caseId}</div>
                </td>
                <td>
                  <strong>{c.cameraName}</strong>
                  {c.alarmId ? <span className="badge amber op-ml">alarm</span> : null}
                  <div className="muted small">{rangeText(c)}</div>
                  {c.title && <div className="muted small">{c.title}</div>}
                </td>
                <td>{formatBytes(c.size)}</td>
                <td>
                  <code className="mono small" title={c.sha256}>
                    {c.sha256.slice(0, 16)}…
                  </code>{" "}
                  <button className="btn small" onClick={() => void copy(c)} aria-label={`Copy SHA-256 of ${c.caseId}`}>
                    {copied === c.id ? "Copied" : "Copy"}
                  </button>
                </td>
                <td>{c.lockedBy}</td>
                <td className="small">{c.note}</td>
                <td className="op-export-actions">
                  <button className="btn small" disabled={working === c.id} onClick={() => void download(c)}>
                    {working === c.id ? "Downloading…" : "Download"}
                  </button>{" "}
                  <button className="btn small" onClick={() => setSharing(c)} title="Create an expiring link anyone on this network can open">
                    Share…
                  </button>{" "}
                  <button className="btn small" disabled={working === c.id} onClick={() => void downloadPackage(c)} title="A zip with the video, its manifest and an offline player that verifies it">
                    Package
                  </button>{" "}
                  <button className="btn small" disabled={checks[c.id] === "busy"} onClick={() => void verify(c)}>
                    {checks[c.id] === "busy" ? "Verifying…" : "Verify"}
                  </button>
                  {checks[c.id] === "ok" && (
                    <span className="op-verify ok" role="status" title="The file matches its recorded SHA-256">
                      {" "}
                      ✓
                    </span>
                  )}
                  {checks[c.id] === "bad" && (
                    <span className="op-verify bad" role="status" title="The file does not match its recorded SHA-256, or is missing">
                      {" "}
                      ✗
                    </span>
                  )}
                  {canDelete && (
                    <>
                      {" "}
                      <button className="btn small danger" onClick={() => setDeleting(c)}>
                        Delete
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <SharedLinks refreshKey={shareTick} />
      {sharing && <ShareDialog evidence={sharing} onClose={() => setSharing(null)} onCreated={() => setShareTick((n) => n + 1)} />}

      {deleting && (
        <Confirm
          title="Release this evidence?"
          confirmLabel="Delete evidence"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await client.evidence.remove(deleting.id);
            setDeleting(null);
            load();
          }}
        >
          {deleting.caseId} ({deleting.cameraName}) and its video file will be deleted from the recording server. This can't be undone.
        </Confirm>
      )}
    </div>
  );
}

function summary(made: number, failed: number): string {
  const ok = made ? `Locked ${made} clip${made > 1 ? "s" : ""} as evidence.` : "";
  const bad = failed ? `${failed} export${failed > 1 ? "s" : ""} failed.` : "";
  return [ok, bad].filter(Boolean).join(" ");
}
