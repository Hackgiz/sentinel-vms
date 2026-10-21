// Semantic search in Search ("Describe it", wave 16): whether the box is
// usable and why not, the request built from the filter bar's time range
// and cameras, how scores show as bars, and what errors say. Pure, so it is
// tested.
import { ApiError, errorMessage } from "../shared/api";
import type { SemanticHit, SemanticRequest, SemanticStatus } from "../detectionApi";
import { emptySearch, windowOf, type SearchState } from "./searchModel";

export const TEXT_MIN = 2;
export const TEXT_MAX = 200;
export const DEFAULT_LIMIT = 30;

/** Whether "Describe it" can run, with the tooltip that says why not. */
export function availability(st: SemanticStatus | null): { enabled: boolean; tooltip: string } {
  if (!st) return { enabled: false, tooltip: "Checking whether semantic search is available…" };
  if (st.available) {
    const n = st.indexed ?? 0;
    return { enabled: true, tooltip: `Searches snapshots by what they show, on this server. ${n.toLocaleString("en-US")} event${n === 1 ? "" : "s"} indexed.` };
  }
  const reason = st.reason || "no semantic search model is installed";
  return { enabled: false, tooltip: `Semantic search is unavailable: ${reason}. Download the "Semantic search" models under System → Models.` };
}

/** What's wrong with the text, or null. */
export function textProblem(text: string): string | null {
  const t = text.trim();
  if (t.length < TEXT_MIN) return "Describe what you're looking for in a few words.";
  if (t.length > TEXT_MAX) return `Keep the description under ${TEXT_MAX} characters.`;
  return null;
}

/** The request body, scoped like the filter bar: its time range and cameras. */
export function semanticRequest(text: string, s: Pick<SearchState, "range" | "from" | "to" | "cameras">, now: number, limit = DEFAULT_LIMIT): SemanticRequest {
  const { from, to } = windowOf({ ...emptySearch, ...s }, now);
  const body: SemanticRequest = { text: text.trim().slice(0, TEXT_MAX), limit: Math.max(1, Math.min(100, Math.round(limit))) };
  if (s.cameras.length) body.cameras = s.cameras;
  if (from) body.from = from;
  if (to) body.to = to;
  return body;
}

/**
 * Bar width (0–100) for each hit. Image-text cosines sit in a narrow band
 * (about 0.2–0.35), so bars are relative to the best hit; the number shown
 * is the raw score.
 */
export function scoreBars(hits: Pick<SemanticHit, "score">[]): number[] {
  const top = Math.max(0, ...hits.map((h) => (Number.isFinite(h.score) ? h.score : 0)));
  return hits.map((h) => (top > 0 && Number.isFinite(h.score) ? Math.round((Math.max(0, h.score) / top) * 100) : 0));
}

export const scoreText = (score: number): string => (Number.isFinite(score) ? score.toFixed(2) : "—");

/** What to tell the operator when the search fails; unavailable = the model is missing. */
export function semanticError(e: unknown): { text: string; unavailable: boolean } {
  if (e instanceof ApiError && e.status === 503) {
    const reason = e.message.replace(/^semantic search is unavailable:\s*/i, "");
    return { text: `Semantic search is unavailable: ${reason}`, unavailable: true };
  }
  return { text: errorMessage(e), unavailable: false };
}
