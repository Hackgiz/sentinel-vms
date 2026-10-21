// Admin → People (wave 16, faces): status line, alert choices, form
// validation and patching, sorting and "last seen". Pure, so it is tested.
import type { FacesStatus, Person, PersonInput, WatchAlert } from "../detectionApi";

export const PRIVACY_NOTE =
  "Face recognition stores a numeric face vector and a small face crop for every face it sees, on this server only. Faces that match nobody are deleted after the days below. Deleting a person deletes their faces. Some places regulate biometric data; check what applies to you. Sentinel does not give legal advice.";

export const alertChoices: { value: WatchAlert; label: string; help: string }[] = [
  { value: "default", label: "Name them in the alarm", help: "The Person alarm keeps its severity; the name goes into its detail." },
  { value: "never", label: "Don't alarm (known person)", help: "The alarm is closed with a note, so family and staff don't raise alarms." },
  { value: "always", label: "Critical watchlist alarm", help: "A Critical Watchlist alarm is raised whenever they are seen." },
];

export const alertLabel = (a: WatchAlert | undefined): string => alertChoices.find((c) => c.value === (a ?? "default"))?.label ?? String(a);

export const DAYS_DEFAULT = 7;
export const DAYS_MIN = 1;
export const DAYS_MAX = 90;

/** Clamp the unassigned-face retention to what the server accepts. */
export function clampDays(v: number): number {
  if (!Number.isFinite(v)) return DAYS_DEFAULT;
  return Math.min(DAYS_MAX, Math.max(DAYS_MIN, Math.round(v)));
}

/** One line for the switch: on/off, counts, or why it can't run. */
export function facesStatusLine(s: FacesStatus): string {
  if (!s.available) return `Unavailable: ${s.reason || "the face models are not installed"}. Download "Faces (find)" and "Faces (recognize)" under System → Models.`;
  if (!s.enabled) return "Off. Turn it on to recognize the people you add here.";
  const n = (v: number, one: string, many: string) => `${v} ${v === 1 ? one : many}`;
  return `On · ${n(s.people, "person", "people")} · ${n(s.unassigned, "unassigned face", "unassigned faces")}`;
}

/** The switch can be turned on only when the models are there; it can always be turned off. */
export const canToggle = (s: FacesStatus): boolean => s.enabled || s.available;

/** People by name, case-insensitive. */
export const sortPeople = (people: Person[]): Person[] => [...people].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));

/** "5 min ago", "3 h ago", "2 days ago", or a date past a week. */
export function ago(t: number | undefined, now: number): string {
  if (!t) return "Never";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return "Just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  const d = Math.floor(s / 86400);
  if (d < 7) return `${d} day${d === 1 ? "" : "s"} ago`;
  return new Date(t).toLocaleDateString();
}

/** "3 h ago · Front door", or "Never". */
export function lastSeen(p: Pick<Person, "lastSeenAt" | "lastSeenCameraId">, cameraName: (id: string) => string | undefined, now: number): string {
  if (!p.lastSeenAt) return "Never";
  const cam = p.lastSeenCameraId ? cameraName(p.lastSeenCameraId) : undefined;
  return cam ? `${ago(p.lastSeenAt, now)} · ${cam}` : ago(p.lastSeenAt, now);
}

export interface PersonForm {
  name: string;
  notes: string;
  alert: WatchAlert;
}

export const personFormOf = (p: Person | null): PersonForm => ({ name: p?.name ?? "", notes: p?.notes ?? "", alert: p?.alert ?? "default" });

/** What's wrong with the form, or null. */
export function personProblem(f: PersonForm): string | null {
  const name = f.name.trim();
  if (!name) return "Enter a name.";
  if (name.length > 80) return "Use 80 characters or fewer for the name.";
  if (f.notes.length > 500) return "Use 500 characters or fewer for notes.";
  return null;
}

/** The POST body for a new person. */
export const personInput = (f: PersonForm): PersonInput => ({ name: f.name.trim(), notes: f.notes.trim(), alert: f.alert });

/** Only the fields that changed (PATCH). */
export function personPatch(p: Person, f: PersonForm): PersonInput {
  const out: PersonInput = {};
  const next = personInput(f);
  if (next.name !== p.name) out.name = next.name;
  if (next.notes !== (p.notes ?? "")) out.notes = next.notes;
  if (next.alert !== p.alert) out.alert = next.alert;
  return out;
}
