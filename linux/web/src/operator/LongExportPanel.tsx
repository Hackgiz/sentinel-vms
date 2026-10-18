// Long export: "give me the whole night". A range of up to 12 hours on one
// camera is handed to an export job on the server (cut in keyframe-aligned
// pieces and joined); this panel shows its progress and, when it is done,
// offers the download or notes the evidence case. Progress is read with one
// timer that runs only while a job is running and the page is visible (the
// System Monitor precedent).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage, type ExportJob } from "../shared/api";
import { usePageVisible } from "./CameraGrid";
import { useData } from "./data";
import { saveBlob } from "./export";
import { fromLocalInput, toLocalInput } from "./exportsModel";
import { jobActive, jobText, longExportBlocker, spanText } from "./longExportModel";
import ExportOptionsFields, { ExportOptionsNote } from "./ExportOptionsFields";
import { noOptions, optionsBody, type ExportOptions } from "./exportOptionsModel";
import { notify } from "./notices";
import { useSigned } from "./session";

export const jobIntervalMs = 1500;

export default function LongExportPanel({ onFiled }: { onFiled?: () => void }) {
  const { session } = useSigned();
  const client = session.client;
  const data = useData();
  const visible = usePageVisible();
  const [cameraId, setCameraId] = useState("");
  const [from, setFrom] = useState(0);
  const [to, setTo] = useState(0);
  const [dest, setDest] = useState<"evidence" | "download">("evidence");
  const [title, setTitle] = useState("");
  const [options, setOptions] = useState<ExportOptions>(noOptions);
  const [job, setJob] = useState<ExportJob | null>(null);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState("");
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const sorted = useMemo(() => [...(data.cameras ?? [])].sort((a, b) => a.name.localeCompare(b.name)), [data.cameras]);
  const blocker = longExportBlocker(cameraId, from, to);
  const running = jobActive(job);

  // A job that just finished: tell the operator, and refresh the case list.
  const finished = useCallback(
    (j: ExportJob) => {
      notify(j.state === "failed" ? "error" : "success", jobText(j));
      if (j.state === "done" && j.dest === "evidence") onFiled?.();
    },
    [onFiled],
  );

  const taskId = job?.taskId;
  useEffect(() => {
    if (!running || !visible || !taskId) return;
    const poll = () =>
      client.exportJobs
        .get(taskId)
        .then((j) => {
          if (!alive.current) return;
          setJob(j);
          if (!jobActive(j)) finished(j);
        })
        .catch((e) => alive.current && setError(errorMessage(e)));
    const t = setInterval(() => void poll(), jobIntervalMs);
    return () => clearInterval(t);
  }, [running, visible, taskId, client, finished]);

  async function start() {
    if (blocker || starting || running) return;
    setStarting(true);
    setError("");
    try {
      const j = await client.exportJobs.start({ cameraId, fromMs: from, toMs: to, dest, title: title.trim() || undefined, ...optionsBody(options) });
      if (alive.current) setJob(j);
    } catch (e) {
      if (alive.current) setError(errorMessage(e));
    } finally {
      if (alive.current) setStarting(false);
    }
  }

  async function cancel() {
    if (!job) return;
    try {
      const j = await client.exportJobs.cancel(job.taskId);
      if (alive.current) setJob(j);
    } catch (e) {
      if (alive.current) setError(errorMessage(e));
    }
  }

  async function download() {
    if (!job) return;
    try {
      saveBlob(job.filename ?? "export.mp4", await client.exportJobs.download(job.taskId));
    } catch (e) {
      if (alive.current) setError(errorMessage(e));
    }
  }

  return (
    <section className="op-export-create op-long-export" aria-label="Long export">
      <h2>Long export</h2>
      <p className="muted small">Export up to 12 hours from one camera as a single file. The server cuts it in pieces and joins them (without re-encoding unless a watermark or pixelated regions are on); it needs about twice the file's size free on the recording server.</p>
      <div className="op-toolbar">
        <select aria-label="Camera" value={cameraId} onChange={(e) => setCameraId(e.target.value)} disabled={running}>
          <option value="">Pick a camera</option>
          {sorted.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
        <input type="datetime-local" step={60} aria-label="Long export from" value={toLocalInput(from)} onChange={(e) => setFrom(fromLocalInput(e.target.value))} disabled={running} />
        <span className="muted">to</span>
        <input type="datetime-local" step={60} aria-label="Long export to" value={toLocalInput(to)} onChange={(e) => setTo(fromLocalInput(e.target.value))} disabled={running} />
        {from > 0 && to > from && <span className="muted small">{spanText(to - from)}</span>}
      </div>
      <div className="op-toolbar">
        <select aria-label="Destination" value={dest} onChange={(e) => setDest(e.target.value as "evidence" | "download")} disabled={running}>
          <option value="evidence">Evidence vault</option>
          <option value="download">Download</option>
        </select>
        {dest === "evidence" && (
          <input type="text" className="op-export-title" placeholder="Title (optional)" aria-label="Long export title" maxLength={120} value={title} onChange={(e) => setTitle(e.target.value)} disabled={running} />
        )}
        <ExportOptionsFields value={options} onChange={setOptions} cameraId={cameraId || null} cameraName={sorted.find((c) => c.id === cameraId)?.name} disabled={running} />
        <button className="btn primary" disabled={!!blocker || starting || running} title={blocker || "Start the export"} onClick={() => void start()}>
          {starting ? "Starting…" : "Start long export"}
        </button>
      </div>
      <ExportOptionsNote value={options} />
      {blocker && cameraId && from > 0 && to > 0 && <div className="muted small op-pad">{blocker}</div>}
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      {job && (
        <div className="op-pad op-long-job" role="status" aria-live="polite">
          {running && <progress max={100} value={job.progress} aria-label="Export progress" />} <span>{jobText(job)}</span>{" "}
          {running && (
            <button className="btn small danger" onClick={() => void cancel()}>
              Cancel
            </button>
          )}
          {job.state === "done" && job.dest === "download" && (
            <button className="btn small primary" onClick={() => void download()}>
              Download
            </button>
          )}
          {job.state === "done" && job.dest === "download" && <span className="muted small"> The file is kept for 30 minutes.</span>}
        </div>
      )}
    </section>
  );
}
