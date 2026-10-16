import { useCallback, useEffect, useRef, useState } from "react";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { Empty, ErrorBanner, Loading } from "./ui";
import { saveBlob, systemApi, type LogLevel, type ServerLogs } from "./systemApi";

const LIMIT = 500;

export default function LogsSection() {
  const servers = useLoad(() => v2.servers.list(), []);
  const [server, setServer] = useState("local");
  const [level, setLevel] = useState<LogLevel | "">("");
  const [follow, setFollow] = useState(true);
  const [logs, setLogs] = useState<ServerLogs | null>(null);
  const [error, setError] = useState<unknown>(null);
  const seq = useRef(0);
  const pane = useRef<HTMLPreElement>(null);

  const load = useCallback(async () => {
    const n = ++seq.current;
    try {
      const l = await systemApi.logs(server, level, LIMIT);
      if (n !== seq.current) return;
      setLogs(l);
      setError(null);
    } catch (e) {
      if (n === seq.current) setError(e);
    }
  }, [server, level]);

  useEffect(() => {
    setLogs(null);
    void load();
    // The pane's one interval; it stops while the tab is hidden.
    const t = setInterval(() => !document.hidden && void load(), 5000);
    return () => clearInterval(t);
  }, [load]);
  useEffect(() => {
    if (follow && pane.current) pane.current.scrollTop = pane.current.scrollHeight;
  }, [logs, follow]);

  const list = (servers.data ?? []).filter((s) => !s.revoked);
  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Server logs</h1>
          <div className="sub">The latest log lines of each server, secrets stripped. Refreshes every few seconds.</div>
        </div>
        <select className="head-input" aria-label="Server" value={server} onChange={(e) => setServer(e.target.value)}>
          {!list.some((s) => s.id === "local") && <option value="local">Management server</option>}
          {list.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <select className="head-input" aria-label="Level" value={level} onChange={(e) => setLevel(e.target.value as LogLevel | "")}>
          <option value="">All levels</option>
          <option value="info">Info and above</option>
          <option value="warn">Warnings and errors</option>
          <option value="error">Errors only</option>
        </select>
        <label className="check">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          Follow
        </label>
        <button
          className="btn"
          onClick={async () => {
            try {
              saveBlob(await systemApi.logsText(server, level, 2000), `sentinel-${server}.log`);
            } catch (e) {
              setError(e);
            }
          }}
        >
          Download
        </button>
      </div>
      <ErrorBanner error={error} onRetry={load} />
      <div className="card">
        {logs === null ? (
          error ? null : <Loading />
        ) : logs.lines.length === 0 ? (
          <Empty icon="☰" title="No log lines" />
        ) : (
          <pre ref={pane} className="mono log-pane" aria-label={`${logs.name} log`} tabIndex={0}>
            {logs.lines.map((l, i) => (
              <div key={i} className={`log-line ${l.level.toLowerCase()}`}>
                {l.text}
              </div>
            ))}
          </pre>
        )}
      </div>
    </>
  );
}
