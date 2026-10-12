// Sentinel Admin: the site's configuration on API v2, under #/admin/<section>.
// Admins see every section; Supervisors see Audit and Health (read-only).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { User as V1User } from "../api";
import { useEventStream } from "../shared/hooks";
import type { StreamState } from "../shared/events";
import { v2 } from "./client";
import { Live, type LiveMessage } from "./ui";
import OverviewSection from "./OverviewSection";
import ServersSection from "./ServersSection";
import CamerasSection from "./CamerasSection";
import GroupsSection from "./GroupsSection";
import UsersSection from "./UsersSection";
import RulesSection from "./RulesSection";
import DirectorySection from "./DirectorySection";
import AuditSection from "./AuditSection";
import HealthSection from "./HealthSection";
import "./admin.css";

export type Section = "overview" | "servers" | "cameras" | "groups" | "users" | "rules" | "directory" | "audit" | "health";

const sections: { id: Section; label: string; adminOnly: boolean }[] = [
  { id: "overview", label: "Overview", adminOnly: true },
  { id: "servers", label: "Recording servers", adminOnly: true },
  { id: "cameras", label: "Cameras", adminOnly: true },
  { id: "groups", label: "Groups", adminOnly: true },
  { id: "users", label: "Users", adminOnly: true },
  { id: "rules", label: "Rules", adminOnly: true },
  { id: "directory", label: "Directory", adminOnly: true },
  { id: "audit", label: "Audit", adminOnly: false },
  { id: "health", label: "Health", adminOnly: false },
];

function sectionFromHash(): string {
  return window.location.hash.replace(/^#\/?/, "").split("/")[1] ?? "";
}

const streamLabel: Record<StreamState, string> = {
  connecting: "Connecting…",
  open: "Live",
  reconnecting: "Reconnecting…",
  stopped: "Not live",
  forbidden: "Not live",
};

export default function AdminPage({ user }: { user: V1User }) {
  const isAdmin = user.role === "Admin";
  const visible = sections.filter((s) => isAdmin || !s.adminOnly);
  const [hashSection, setHashSection] = useState(sectionFromHash);
  useEffect(() => {
    const on = () => setHashSection(sectionFromHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const current = visible.find((s) => s.id === hashSection)?.id ?? visible[0].id;

  // One event stream for every section; sections subscribe through <Live>.
  const subscribers = useRef(new Set<(m: LiveMessage) => void>());
  const publish = useCallback((m: LiveMessage) => subscribers.current.forEach((fn) => fn(m)), []);
  const { state } = useEventStream(v2, {
    topics: ["alarm", "camera", "config"],
    // recorder.status (topic `servers`) is in the contract but not yet in
    // every server's topic list; an unknown topic refuses the upgrade.
    optionalTopics: ["servers"],
    onMessage: publish,
    onResync: () => publish({ type: "resync", at: Date.now() }),
  });
  const live = useMemo(
    () => ({
      state,
      subscribe: (fn: (m: LiveMessage) => void) => {
        subscribers.current.add(fn);
        return () => {
          subscribers.current.delete(fn);
        };
      },
    }),
    [state],
  );

  return (
    <Live.Provider value={live}>
      <div className="admin-head">
        <div className="grow">
          <div className="admin-kicker">Sentinel Admin</div>
        </div>
        <span className={`badge ${state === "open" ? "green" : "amber"}`} title="Live updates from the management server" role="status">
          <span className="dot" />
          {streamLabel[state]}
        </span>
      </div>
      <nav className="admin-nav" aria-label="Sentinel Admin sections">
        {visible.map((s) => (
          <a key={s.id} href={`#/admin/${s.id}`} className={current === s.id ? "on" : ""} aria-current={current === s.id ? "page" : undefined}>
            {s.label}
          </a>
        ))}
      </nav>
      {current === "overview" && <OverviewSection />}
      {current === "servers" && <ServersSection />}
      {current === "cameras" && <CamerasSection />}
      {current === "groups" && <GroupsSection />}
      {current === "users" && <UsersSection meId={user.id} />}
      {current === "rules" && <RulesSection />}
      {current === "directory" && <DirectorySection />}
      {current === "audit" && <AuditSection />}
      {current === "health" && <HealthSection canManage={isAdmin} />}
    </Live.Provider>
  );
}
