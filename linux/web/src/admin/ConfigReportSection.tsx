import { useState } from "react";
import { ErrorBanner, Loading } from "./ui";
import { saveBlob, systemApi } from "./systemApi";

type Report = Record<string, unknown> & { generatedAt?: string };

const counted = ["servers", "cameras", "groups", "users", "roles", "rules", "timeProfiles", "webhooks"] as const;
const labels: Record<(typeof counted)[number], string> = {
  servers: "Recording servers",
  cameras: "Cameras",
  groups: "Camera groups",
  users: "Users",
  roles: "Roles",
  rules: "Rules",
  timeProfiles: "Time profiles",
  webhooks: "Webhooks",
};

export default function ConfigReportSection() {
  const [report, setReport] = useState<{ text: string; data: Report } | null>(null);
  const [busy, setBusy] = useState<"" | "preview" | "json" | "html">("");
  const [error, setError] = useState<unknown>(null);

  async function run(kind: "preview" | "json" | "html") {
    setBusy(kind);
    setError(null);
    try {
      if (kind === "html") {
        const blob = await systemApi.report.html();
        saveBlob(blob, `sentinel-configuration-${new Date().toISOString().slice(0, 10)}.html`);
        return;
      }
      const blob = await systemApi.report.json();
      const text = await blob.text();
      if (kind === "json") {
        saveBlob(new Blob([text], { type: "application/json" }), `sentinel-configuration-${new Date().toISOString().slice(0, 10)}.json`);
      } else {
        setReport({ text, data: JSON.parse(text) as Report });
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Configuration report</h1>
          <div className="sub">A snapshot of the site's configuration. Passwords, password hashes, camera credentials and webhook secrets are never included.</div>
        </div>
        <button className="btn" disabled={!!busy} onClick={() => void run("preview")}>
          {busy === "preview" ? "Generating…" : "Preview"}
        </button>
        <button className="btn" disabled={!!busy} onClick={() => void run("json")}>
          Download JSON
        </button>
        <button className="btn" disabled={!!busy} onClick={() => void run("html")}>
          Download printable HTML
        </button>
      </div>
      <ErrorBanner error={error} />
      {busy === "preview" && !report && (
        <div className="card">
          <Loading />
        </div>
      )}
      {report && (
        <>
          <div className="grid-2">
            <section className="card" aria-label="Report summary">
              <h2>Summary</h2>
              <div className="stat">
                <span>Generated</span>
                <span className="mono">{report.data.generatedAt ? new Date(report.data.generatedAt).toLocaleString() : "—"}</span>
              </div>
              {counted.map((k) => (
                <div className="stat" key={k}>
                  <span>{labels[k]}</span>
                  <span>{Array.isArray(report.data[k]) ? (report.data[k] as unknown[]).length : 0}</span>
                </div>
              ))}
            </section>
          </div>
          <section className="card" aria-label="Report contents">
            <h2>Contents</h2>
            <pre className="mono log-pane" tabIndex={0}>
              {JSON.stringify(report.data, null, 2)}
            </pre>
          </section>
        </>
      )}
    </>
  );
}
