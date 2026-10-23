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

// WAVE20: the colours and glyphs of the iPhone's chips and alarm cards
// (SentinelTheme.kindColor / kindSymbol / severityColor / stateColor, and
// AlarmCard.symbol(for:severity:)), as CSS colours and Icons.tsx names.

/** The palette tokens in styles.css, plus the iPhone's extra kind colours. */
export const tones = {
  accent: "var(--accent)",
  amber: "var(--amber)",
  green: "var(--green)",
  red: "var(--red)",
  grey: "rgba(242, 244, 248, 0.5)",
  orange: "#ff8c1a",
  sky: "#3399ff",
  purple: "#cc4dd9",
  teal: "#2dd4bf",
  tan: "#c8955a",
  pink: "#f2559a",
  indigo: "#8b7cf6",
  slate: "#94a3b8",
} as const;

const kindTones: Record<string, string> = {
  Person: tones.teal,
  Face: tones.orange,
  Vehicle: tones.sky,
  "License Plate": tones.amber,
  Animal: tones.green,
  Package: tones.tan,
  Loitering: tones.purple,
  "Line crossing": tones.pink,
  Intrusion: tones.pink,
  audio: tones.indigo,
  Sound: tones.indigo,
  Watchlist: tones.red,
};

/** A detection kind's chip colour; Motion and anything unlisted are slate. */
export const kindTone = (kind: string | undefined): string => (kind && kindTones[kind]) || tones.slate;

const kindGlyphs: Record<string, string> = {
  Person: "walk",
  Face: "face",
  Vehicle: "car",
  "License Plate": "plate",
  Animal: "paw",
  Loitering: "clock",
  Watchlist: "flag",
  Motion: "motion",
  "Line crossing": "arrows",
  audio: "sound",
};

/** A detection kind's glyph. */
export const kindGlyph = (kind: string | undefined): string => (kind && kindGlyphs[kind]) || "sparkles";

const alarmGlyphs: Record<string, string> = {
  Person: "walk",
  Vehicle: "car",
  Motion: "motion",
  "Camera Offline": "videoOff",
  Recording: "record",
  Storage: "drive",
  "Line crossing": "arrows",
  Credential: "key",
  Watchlist: "flag",
  Loitering: "clock",
  audio: "sound",
};

/** An alarm's glyph: by kind, else by severity. */
export const alarmGlyph = (kind: string | undefined, severity: string): string => (kind && alarmGlyphs[kind]) || (severity === "Critical" ? "octagon" : "alert");

export const severityTone = (severity: string): string => (severity === "Critical" ? tones.red : severity === "Warning" ? tones.amber : tones.accent);

/** An alarm's accent: a Watchlist alarm keeps its kind colour whatever its severity. */
export const alarmTone = (a: { kind?: string; severity: string }): string => (a.kind === "Watchlist" ? kindTone("Watchlist") : severityTone(a.severity));

const stateTones: Record<string, string> = {
  New: tones.red,
  Acknowledged: tones.accent,
  Investigating: tones.amber,
  Snoozed: tones.grey,
  Resolved: tones.green,
  "False Alarm": tones.green,
};

export const stateTone = (state: string): string => stateTones[state] ?? tones.grey;
