// WAVE20: the navigation rail (WAVE20-OPERATOR-LOOK.md rule 1), after the
// iPhone's SidebarShell: 84 px, the site and its online dot on top, one item
// per fixed workspace (icon over label, the selected one on a tinted rounded
// square, the open-alarm count as a badge, the Ctrl shortcut in the tooltip),
// and pinned at the bottom the clock, full screen and Settings (the account
// and site menu). Which items show is the workspace's call (paneGate).
// WAVE21: the rail itself is shared/Rail.tsx (the server dashboard uses it too).
import { useEffect, useState, type ReactNode } from "react";
import { Icon } from "../shared/Icons";
import SharedRail, { RailMenu } from "../shared/Rail";
import { badgeText, keyLabel, msToNextMinute, railLabel, type FixedId, type FixedWorkspace } from "./workspaces";

const icons: Record<FixedId, string> = {
  home: "home",
  live: "video",
  map: "map",
  search: "search",
  exports: "export",
  alarms: "bell",
  incidents: "incident",
  monitor: "monitor",
  reports: "reports",
  insights: "insights",
  rules: "rules",
  people: "people",
  plates: "plates",
};

export interface RailProps {
  fixed: FixedWorkspace[];
  /** The highlighted item (a saved-view tab counts as Live). */
  active: FixedId | null;
  alarmCount: number;
  siteName: string;
  online: boolean;
  /** The live-updates state, for the dot's tooltip. */
  status: string;
  onSelect: (id: FixedId) => void;
  /** The Settings menu's items (role="menuitem" buttons); the rail closes it on a pick. */
  settings: ReactNode;
  settingsLabel: string;
}

/** The wall clock; ticks on the minute. */
export function WallClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setTimeout(() => setNow(new Date()), msToNextMinute(now.getTime()));
    return () => clearTimeout(t);
  }, [now]);
  return (
    <time className="op-clock" dateTime={now.toISOString()} title={now.toLocaleDateString(undefined, { dateStyle: "full" })}>
      {now.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
    </time>
  );
}

function FullscreenToggle() {
  const [on, setOn] = useState(() => typeof document !== "undefined" && !!document.fullscreenElement);
  useEffect(() => {
    const f = () => setOn(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", f);
    return () => document.removeEventListener("fullscreenchange", f);
  }, []);
  if (typeof document === "undefined" || !document.fullscreenEnabled) return null;
  return (
    <button
      className={`op-rail-tool ${on ? "on" : ""}`}
      aria-pressed={on}
      title={on ? "Leave full screen" : "Full screen"}
      aria-label={on ? "Leave full screen" : "Full screen"}
      onClick={() => void (on ? document.exitFullscreen() : document.documentElement.requestFullscreen()).catch(() => {})}
    >
      <Icon name={on ? "shrink" : "expand"} size={16} />
    </button>
  );
}

export default function Rail(p: RailProps) {
  return (
    <SharedRail
      items={p.fixed.map((w) => {
        const badge = w.badge === "alarms" ? badgeText(p.alarmCount) : "";
        return { id: w.id, label: railLabel(w), icon: icons[w.id], title: `${w.label} (${keyLabel(w)})`, badge, ariaLabel: badge ? `${w.label}, ${p.alarmCount} open alarms` : w.label };
      })}
      active={p.active}
      siteName={p.siteName}
      online={p.online}
      status={p.status}
      onSelect={(id) => p.onSelect(id as FixedId)}
      foot={
        <>
          <WallClock />
          <FullscreenToggle />
          <RailMenu label={p.settingsLabel}>{p.settings}</RailMenu>
        </>
      }
    />
  );
}
