// Pure helpers for the evidence "Share…" dialog and shared-links list.

import type { ShareLink } from "../shared/api";

export type SharePreset = "1h" | "24h" | "7d" | "custom";

export const sharePresets: { value: SharePreset; label: string }[] = [
  { value: "1h", label: "1 hour" },
  { value: "24h", label: "24 hours" },
  { value: "7d", label: "7 days" },
  { value: "custom", label: "Custom…" },
];

export const maxShareMs = 30 * 24 * 3600e3;

/** Why the dialog can't be submitted, or "" when it can. */
export function shareBlocker(preset: SharePreset, customAt: number, maxUses: string, now = Date.now()): string {
  if (preset === "custom") {
    if (!customAt) return "Pick an expiry date and time.";
    if (customAt <= now) return "The expiry must be in the future.";
    if (customAt > now + maxShareMs) return "A link can last at most 30 days.";
  }
  if (maxUses.trim() !== "") {
    const n = Number(maxUses);
    if (!Number.isInteger(n) || n < 0 || n > 1000) return "Max uses must be a whole number from 0 to 1000.";
  }
  return "";
}

/** The POST body for a dialog state (blank max uses = unlimited). */
export function shareBody(preset: SharePreset, customAt: number, maxUses: string, note: string) {
  return {
    expiry: preset,
    ...(preset === "custom" ? { expiresAt: customAt } : {}),
    maxUses: maxUses.trim() === "" ? 0 : Number(maxUses),
    ...(note.trim() ? { note: note.trim() } : {}),
  };
}

/** "3 of 5", or "3" for an unlimited link. */
export function usesText(l: Pick<ShareLink, "uses" | "maxUses">): string {
  return l.maxUses > 0 ? `${l.uses} of ${l.maxUses}` : `${l.uses}`;
}

/** Links that can still be opened come first, then by newest. */
export function sortShares(list: ShareLink[]): ShareLink[] {
  return [...list].sort((a, b) => Number(b.status === "active") - Number(a.status === "active") || b.createdAt - a.createdAt);
}
