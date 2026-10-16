// Wave 6 stream D calls: site info, monitor thresholds, tasks, server logs
// and the configuration report. Typed from the generated contract.
import type { components } from "../api/v2";
import { v2 } from "./client";

type S = components["schemas"];
export type SiteInfo = S["SiteInfo"];
export type MonitorThresholds = S["MonitorThresholds"];
export type SystemTask = S["SystemTask"];
export type ServerLogs = S["ServerLogs"];
export type LogLevel = "debug" | "info" | "warn" | "error";

function query(q: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== "") p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const systemApi = {
  site: {
    get: () => v2.call("get", "/api/v2/site"),
    put: (body: SiteInfo) => v2.call("put", "/api/v2/site", { body }),
  },
  thresholds: {
    get: () => v2.call("get", "/api/v2/system/thresholds"),
    put: (body: MonitorThresholds) => v2.call("put", "/api/v2/system/thresholds", { body }),
  },
  tasks: async (): Promise<SystemTask[]> => (await v2.call("get", "/api/v2/system/tasks")).items,
  logs: (server: string, level: LogLevel | "", limit: number) => v2.call("get", "/api/v2/system/logs", { query: { server, level: level || undefined, limit } }),
  /** The text download variant, as a Blob with the client's auth. */
  logsText: (server: string, level: LogLevel | "", limit: number) => v2.blob(`/api/v2/system/logs${query({ server, level, limit, format: "text" })}`),
  report: {
    json: () => v2.blob("/api/v2/system/config-report"),
    html: () => v2.blob("/api/v2/system/config-report?format=html"),
  },
};

/** Save a Blob through the browser's download flow. */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}
