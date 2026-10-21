// System → Detection card (wave 16): the detector model picker, the rate
// selector, the status line and the Models table. Pure, so it is tested.
import type { DetectorSettings, Model, ModelPurpose } from "../detectionApi";

export const DEFAULT_DETECTOR = "yolox-tiny";

export const purposeLabel: Record<ModelPurpose, string> = {
  detector: "Object detection",
  reid: "Appearance search",
  audio: "Audio classes",
  "face-detect": "Faces (find)",
  "face-embed": "Faces (recognize)",
  "plate-det": "Plates (find)",
  "plate-rec": "Plates (read)",
  "clip-image": "Semantic search (images)",
  "clip-text": "Semantic search (text)",
};

export const purposeOf = (p: string): string => purposeLabel[p as ModelPurpose] ?? p;

/** The saved model id with "" meaning the bundled default. */
export const savedModel = (s: Pick<DetectorSettings, "model">): string => s.model || DEFAULT_DETECTOR;

export interface DetectorChoice {
  id: string;
  title: string;
  note: string;
  /** false only for the saved model when its file is gone (shown so the select is honest). */
  installed: boolean;
}

/** Installed detector models, the saved one first-class even when it is missing. */
export function detectorChoices(models: Model[], saved: string): DetectorChoice[] {
  const out: DetectorChoice[] = models
    .filter((m) => m.purpose === "detector" && (m.installed || m.id === saved))
    .map((m) => ({ id: m.id, title: m.title, note: m.note ?? "", installed: m.installed }));
  if (!out.some((c) => c.id === saved)) out.unshift({ id: saved, title: saved, note: "", installed: false });
  return out;
}

export const rateOptions: { value: number; label: string }[] = [
  { value: 0, label: "Auto (1/s on the CPU, 5/s with an accelerator)" },
  ...[1, 2, 3, 4, 5].map((n) => ({ value: n, label: `${n} per second` })),
];

/** The status line: model, engine, device, effective rate, inference time. */
export function statusLine(s: DetectorSettings, models: Model[]): string {
  const st = s.status;
  const title = models.find((m) => m.id === st.model)?.title ?? (st.model || DEFAULT_DETECTOR);
  const parts = [title, st.engine + (st.device ? ` (${st.device})` : ""), `${st.effectiveRate}/s per camera`];
  if (st.inferMs !== undefined && st.inferMs > 0) parts.push(`${st.inferMs.toFixed(1)} ms per frame`);
  return parts.join(" · ");
}

/** "Restart to apply" note when the saved model is not the one running, with the reason when known. */
export function pendingNote(s: DetectorSettings): string | null {
  if (s.status.reason) return `The chosen model is not running: ${s.status.reason}.`;
  if (s.status.pending) return "Restart Sentinel to apply the model change. The rate has already changed.";
  return null;
}

/** Download progress 0–100, or null when unknown or not downloading. */
export function downloadPercent(m: Pick<Model, "state" | "download">): number | null {
  const d = m.download;
  if (m.state !== "downloading" || !d || d.state !== "running" || !d.total) return null;
  return Math.max(0, Math.min(100, Math.round((d.done / d.total) * 100)));
}

export type ModelAction = "download" | "cancel" | "delete";

/**
 * What a row can do. Bundled models never; the selected detector can't be
 * deleted; a missing or corrupt file can be downloaded when the catalog
 * names a source.
 */
export function modelActions(m: Model, selected: string, canChange: boolean): ModelAction[] {
  if (!canChange || m.bundled) return [];
  if (m.state === "downloading") return ["cancel"];
  const out: ModelAction[] = [];
  if ((m.state === "missing" || m.state === "corrupt") && m.source) out.push("download");
  if ((m.state === "installed" || m.state === "corrupt") && !(m.purpose === "detector" && m.id === selected)) out.push("delete");
  return out;
}

export const stateTone: Record<Model["state"], string> = { installed: "green", downloading: "blue", missing: "", corrupt: "red" };

export function stateLabel(m: Pick<Model, "state" | "bundled" | "download">): string {
  if (m.bundled && m.state === "installed") return "Bundled";
  if (m.state === "missing" && m.download?.state === "failed") return "Download failed";
  return { installed: "Installed", downloading: "Downloading", missing: "Not installed", corrupt: "Damaged" }[m.state];
}

/** Any download running: the card polls while true. */
export const anyDownloading = (models: Model[]): boolean => models.some((m) => m.state === "downloading");

/** Catalog order, grouped by purpose (detector first, as the catalog lists them). */
export function sortModels(models: Model[]): Model[] {
  const order = Object.keys(purposeLabel);
  return [...models].sort((a, b) => order.indexOf(a.purpose) - order.indexOf(b.purpose));
}
