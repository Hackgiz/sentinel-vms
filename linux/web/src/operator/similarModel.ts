// Appearance search ("Find similar", WAVE9(A)): which events can be searched
// by appearance, how the window picker maps to the request, how scores are
// shown, and what the banner says when no model is installed.
import { ApiError, errorMessage, type AppearanceBackfill, type AppearanceStatus, type Event, type SimilarEventsRequest } from "../shared/api";

/** Kinds that carry a person/vehicle/animal crop (motion and audio don't). */
const noAppearance = new Set(["motion", "audio"]);

/** Whether "Find similar" makes sense: a detection box on a snapshot. */
export function canFindSimilar(e: Pick<Event, "box" | "hasThumbnail" | "kind">): boolean {
  return !!e.hasThumbnail && Array.isArray(e.box) && e.box.length === 4 && e.box[2] > e.box[0] && e.box[3] > e.box[1] && !noAppearance.has(e.kind.toLowerCase());
}

export type SimilarWindow = "around" | "7d" | "30d" | "all";

export const similarWindows: { id: SimilarWindow; label: string }[] = [
  { id: "around", label: "±24 h around it" },
  { id: "7d", label: "Last 7 days" },
  { id: "30d", label: "Last 30 days" },
  { id: "all", label: "Any time" },
];

const day = 24 * 3600e3;

export interface SimilarOptions {
  window: SimilarWindow;
  /** Only the query's own camera. */
  sameCamera: boolean;
  /** Compare against every kind, not only the query's. */
  anyKind: boolean;
  limit: number;
}

export const defaultSimilar: SimilarOptions = { window: "7d", sameCamera: false, anyKind: false, limit: 48 };

/** The request body for POST /events/{id}/similar. */
export function similarRequest(query: Pick<Event, "createdAt" | "cameraId">, o: SimilarOptions, now: number): SimilarEventsRequest {
  const body: SimilarEventsRequest = { limit: Math.max(1, Math.min(100, Math.round(o.limit))) };
  switch (o.window) {
    case "around":
      body.from = Math.max(0, query.createdAt - day);
      body.to = query.createdAt + day;
      break;
    case "7d":
      body.from = now - 7 * day;
      break;
    case "30d":
      body.from = now - 30 * day;
      break;
  }
  if (o.sameCamera) body.cameras = [query.cameraId];
  if (o.anyKind) body.anyKind = true;
  return body;
}

/** A cosine score as a whole percentage (negative = 0). */
export function scorePercent(score: number): number {
  if (!Number.isFinite(score)) return 0;
  return Math.round(Math.max(0, Math.min(1, score)) * 100);
}

/** Badge color for a score: strong matches green, likely amber, weak grey. */
export function scoreTone(score: number): "green" | "amber" | "" {
  const p = scorePercent(score);
  return p >= 80 ? "green" : p >= 60 ? "amber" : "";
}

const installHint = "Put an OSNet re-identification model (ONNX) at <data>/models/reid.onnx, or point SENTINEL_REID_MODEL at one, and restart the server.";

/** The "no model" banner text, or null when appearance search works. */
export function unavailableText(status: AppearanceStatus | null): string | null {
  if (!status || status.available) return null;
  const reason = status.reason ? status.reason.charAt(0).toUpperCase() + status.reason.slice(1) : "No appearance model is installed";
  return `Appearance search is unavailable. ${reason}. ${installHint}`;
}

/** What to tell the operator when a search fails; banner = show it as the "no model" banner. */
export function similarError(e: unknown): { text: string; banner: boolean } {
  if (e instanceof ApiError && e.status === 503) {
    const reason = e.message.replace(/^appearance search is unavailable:\s*/i, "");
    return { text: unavailableText({ available: false, reason }) ?? e.message, banner: true };
  }
  return { text: errorMessage(e), banner: false };
}

/** One line about the index job. */
export function backfillLabel(b: AppearanceBackfill | undefined): string {
  if (!b) return "";
  const n = (v: number) => v.toLocaleString("en-US");
  switch (b.state) {
    case "running":
      return b.total ? `Indexing recent events: ${n(b.processed)} of ${n(b.total)}` : "Indexing recent events…";
    case "done":
      return `Index built: ${n(b.indexed)} of ${n(b.total)} events${b.skipped ? ` (${n(b.skipped)} skipped)` : ""}`;
    case "canceled":
      return `Indexing canceled after ${n(b.processed)} of ${n(b.total)} events`;
    default:
      return `Indexing failed${b.error ? `: ${b.error}` : ""}`;
  }
}
