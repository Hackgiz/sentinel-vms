// Icons for alarm kinds that need to stand out in a list (wave 16: the
// Watchlist alarm a known person or plate raises). Other kinds show text
// only, as before. Pure, so it is tested.

const icons: Record<string, { icon: string; label: string }> = {
  Watchlist: { icon: "⚑", label: "Watchlist" },
};

/** The icon and its accessible label for a kind, or null for kinds without one. */
export function alarmKindIcon(kind: string | undefined): { icon: string; label: string } | null {
  return (kind && icons[kind]) || null;
}

/** Event chips (wave 16): the matched person's name and the plate read, in display order. */
export function eventTags(e: { personName?: string; plate?: string }): { kind: "person" | "plate"; text: string }[] {
  const out: { kind: "person" | "plate"; text: string }[] = [];
  if (e.personName?.trim()) out.push({ kind: "person", text: e.personName.trim() });
  if (e.plate?.trim()) out.push({ kind: "plate", text: e.plate.trim() });
  return out;
}
