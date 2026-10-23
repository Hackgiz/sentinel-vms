// WAVE21: the server dashboard's pages and its rail (App.tsx), after the
// operator's (WAVE20-OPERATOR-LOOK.md rule 1). Pure, so it is tested.
import { can, type User } from "./api";
import { sectionHash } from "./admin/navModel";
import { alarmPreRollMs, eventPreRollMs } from "./shared/time";
import { viewerHash } from "./viewer/viewerMath";

export type Page = "home" | "live" | "alarms" | "rules" | "events" | "recordings" | "cameras" | "phones" | "cloud" | "users" | "audit" | "system" | "admin";

export interface PageDef {
  id: Page;
  /** The full name (tooltips, the accessible name). */
  label: string;
  /** Under the icon on the 84 px rail. */
  short: string;
  icon: string;
  allowed: (u: User) => boolean;
}

const anyone = () => true;

export const pages: PageDef[] = [
  { id: "home", label: "Home", short: "Home", icon: "home", allowed: anyone },
  { id: "live", label: "Live", short: "Live", icon: "video", allowed: anyone },
  { id: "alarms", label: "Alarms", short: "Alarms", icon: "bell", allowed: anyone },
  { id: "rules", label: "Alarm Rules", short: "Rules", icon: "rules", allowed: anyone },
  { id: "events", label: "Events", short: "Events", icon: "sparkles", allowed: anyone },
  { id: "recordings", label: "Recordings", short: "Recordings", icon: "film", allowed: anyone },
  { id: "cameras", label: "Cameras", short: "Cameras", icon: "camera", allowed: can.manageCameras },
  { id: "phones", label: "iPhone", short: "iPhone", icon: "phone", allowed: can.manageUsers },
  { id: "cloud", label: "Sentinel Cloud", short: "Cloud", icon: "cloud", allowed: anyone }, // WAVE13(C)
  { id: "users", label: "Users", short: "Users", icon: "people", allowed: can.manageUsers },
  { id: "audit", label: "Audit Log", short: "Audit", icon: "list", allowed: can.viewAudit },
  { id: "system", label: "System", short: "System", icon: "monitor", allowed: anyone },
  // Sentinel Admin (API v2): Admins see everything, Supervisors Audit and Health.
  { id: "admin", label: "Admin", short: "Admin", icon: "key", allowed: can.viewAudit },
];

/** The page a hash names; Home for anything else (the landing route). */
export function pageFromHash(hash: string): Page {
  const id = hash.replace(/^#\/?/, "").split(/[/?]/)[0]; // WAVE13(C): "#/cloud?checkout=success" is the cloud page
  // Playback and Compare moved onto the Live page; old links open it in playback mode.
  if (id === "playback" || id === "compare") return "live";
  return pages.some((p) => p.id === id) ? (id as Page) : "home";
}

export const visiblePages = (u: User): PageDef[] => pages.filter((p) => p.allowed(u));

/** The page shown: the asked one if the user may open it, else Home. */
export const currentPage = (page: Page, visible: PageDef[]): Page => (visible.some((p) => p.id === page) ? page : "home");

/** The alarm badge: the New count, "9+" past nine, nothing at zero. */
export const alarmBadge = (n: number): string => (n <= 0 ? "" : n > 9 ? "9+" : String(n));

/** Where Home's links go on the dashboard. */
export const homeLinks = {
  live: "#/live",
  events: "#/events",
  alarms: "#/alarms",
  insights: sectionHash("insights"),
  people: sectionHash("people"),
  camera: (cameraId: string) => viewerHash("live", cameraId),
  playback: (cameraId: string, at: number) => viewerHash("live", cameraId, at),
};

// PARITY-342 §3: the Events and Alarms pages open the recording a little
// before the moment. Their v1 events and alarms carry Unix seconds.

/** An event's recording, from `createdAt - 5 s`. */
export const eventPlaybackLink = (e: { cameraID: string; createdAt: number }): string => homeLinks.playback(e.cameraID, e.createdAt * 1000 - eventPreRollMs);

/** An alarm's recording, from `createdAt - 10 s`; null for an alarm without a camera. */
export const alarmPlaybackLink = (a: { cameraID?: string; createdAt: number }): string | null =>
  a.cameraID ? homeLinks.playback(a.cameraID, a.createdAt * 1000 - alarmPreRollMs) : null;

/** Which of Home's links this user may follow (Insights and People live in Admin). */
export const homeCan = (u: User) => ({ alarms: true, insights: can.viewAudit(u), people: can.viewAudit(u) });
