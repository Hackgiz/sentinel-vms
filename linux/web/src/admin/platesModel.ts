// Admin → Plates (wave 16, plate reading): normalization as the server does
// it, validation, patching, sorting, filtering and the status line. Pure, so
// it is tested.
import type { LPRStatus, Plate, PlateInput, WatchAlert } from "../detectionApi";

/** Upper-case, A–Z and 0–9 only: what the server stores and matches. */
export const normalizePlate = (s: string): string => s.toUpperCase().replace(/[^A-Z0-9]/g, "");

export const PLATE_MIN = 2;
export const PLATE_MAX = 10;

export const alertChoices: { value: WatchAlert; label: string; help: string }[] = [
  { value: "default", label: "Put the plate in the alarm", help: "The Vehicle alarm keeps its severity; the name or plate goes into its detail." },
  { value: "never", label: "Don't alarm (known vehicle)", help: "The alarm is closed with a note, for your own cars and regular deliveries." },
  { value: "always", label: "Critical watchlist alarm", help: "A Critical Watchlist alarm is raised whenever the plate is read." },
];

export const alertLabel = (a: WatchAlert | undefined): string => alertChoices.find((c) => c.value === (a ?? "default"))?.label ?? String(a);

/** One line for the switch: on/off, counts, or why it can't run. */
export function lprStatusLine(s: LPRStatus): string {
  if (!s.available) return `Unavailable: ${s.reason || "the plate models are not installed"}. Download "Plates (find)" and "Plates (read)" under System → Models.`;
  if (!s.enabled) return "Off. Turn it on to read plates on vehicle events.";
  const read = s.readLast24h ?? 0;
  return `On · ${s.plates} on the watchlist · ${read} plate${read === 1 ? "" : "s"} read in the last 24 h`;
}

export const canToggle = (s: LPRStatus): boolean => s.enabled || s.available;

export interface PlateForm {
  plate: string;
  name: string;
  notes: string;
  alert: WatchAlert;
}

export const plateFormOf = (p: Plate | null): PlateForm => ({ plate: p?.plate ?? "", name: p?.name ?? "", notes: p?.notes ?? "", alert: p?.alert ?? "default" });

/** What's wrong with the form, or null. `existing` catches duplicates before the server's 409. */
export function plateProblem(f: PlateForm, existing: Plate[] = [], editingId?: string): string | null {
  const n = normalizePlate(f.plate);
  if (n.length < PLATE_MIN) return "Enter the plate: at least 2 letters or digits.";
  if (n.length > PLATE_MAX) return "A plate has at most 10 letters and digits.";
  if (existing.some((p) => p.plate === n && p.id !== editingId)) return `${n} is already on the watchlist.`;
  if (f.name.trim().length > 80) return "Use 80 characters or fewer for the name.";
  if (f.notes.length > 500) return "Use 500 characters or fewer for notes.";
  return null;
}

/** The POST body for a new entry. */
export const plateInput = (f: PlateForm): PlateInput => ({ plate: normalizePlate(f.plate), name: f.name.trim(), notes: f.notes.trim(), alert: f.alert });

/** PATCH: name, notes and alert only (the plate itself is fixed once listed). */
export function platePatch(p: Plate, f: PlateForm): PlateInput {
  const out: PlateInput = {};
  if (f.name.trim() !== (p.name ?? "")) out.name = f.name.trim();
  if (f.notes.trim() !== (p.notes ?? "")) out.notes = f.notes.trim();
  if (f.alert !== p.alert) out.alert = f.alert;
  return out;
}

export const sortPlates = (list: Plate[]): Plate[] => [...list].sort((a, b) => a.plate.localeCompare(b.plate));

/** Filter by plate (normalized prefix or substring) or name. */
export function filterPlates(list: Plate[], q: string): Plate[] {
  const t = q.trim();
  if (!t) return list;
  const n = normalizePlate(t);
  const lower = t.toLowerCase();
  return list.filter((p) => (n && p.plate.includes(n)) || (p.name ?? "").toLowerCase().includes(lower));
}

/** "AB12 · Delivery van", or the plate alone. */
export const plateTitle = (p: Pick<Plate, "plate" | "name">): string => (p.name ? `${p.plate} · ${p.name}` : p.plate);
