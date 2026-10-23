// Sentinel Admin: the site's configuration on API v2, under #/admin/<section>.
// Admins see every section; Supervisors see Insights, Audit and Health (read-only).
// Navigation is the site tree in navModel.ts / NavTree.tsx; to add a page,
// append to `sections` below + a render line, and place it in navModel's catalog.
// A "?…" after a section's hash is a deep link into it (navModel.hashParams).
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
import FloorplansSection from "./FloorplansSection";
import ZonesSection from "./ZonesSection";
import UsersSection from "./UsersSection";
import RulesSection from "./RulesSection";
import DirectorySection from "./DirectorySection";
import AuditSection from "./AuditSection";
import HealthSection from "./HealthSection";
import RolesSection from "./RolesSection";
import SiteInfoSection from "./SiteInfoSection";
import TasksSection from "./TasksSection";
import LogsSection from "./LogsSection";
import ConfigReportSection from "./ConfigReportSection";
import EvidenceSection from "./EvidenceSection";
import TimeProfilesSection from "./TimeProfilesSection";
import WebhooksSection from "./WebhooksSection";
import NotificationsSection from "./NotificationsSection";
import CloudSection from "./CloudSection"; // WAVE12(C)
import InsightsSection from "./InsightsSection"; // WAVE14(C)
import PeopleSection from "./PeopleSection"; // WAVE16(F)
import PlatesSection from "./PlatesSection"; // WAVE16(F)
import RecordingSection from "./RecordingDefaults"; // WAVE22(C)
import NavTree from "./NavTree";
import { PageHeader } from "../shared/Cards"; // WAVE21
import Welcome from "./Welcome";
import { breadcrumb, buildTree, hashParams, parseHash, siteTitle } from "./navModel";
import "./admin.css";

export type Section = "overview" | "servers" | "cameras" | "groups" | "floorplans" | "users" | "rules" | "directory" | "audit" | "health" | "roles" | "site" | "tasks" | "logs" | "report" | "evidence" | "timeprofiles" | "webhooks" | "notifications" | "zones" | "cloud" | "insights" | "people" | "plates" | "recording"; // WAVE12(C): cloud; WAVE14(C): insights; WAVE16(F): people, plates; WAVE22(C): recording

// `id` is the section id the tree places via navModel's catalog; unplaced
// sections show under "Other" until rehomed.
const sections: { id: Section; label: string; adminOnly: boolean }[] = [
  { id: "overview", label: "Overview", adminOnly: true },
  { id: "servers", label: "Recording servers", adminOnly: true },
  { id: "recording", label: "Recording", adminOnly: true }, // WAVE22(C): server recording defaults
  { id: "cameras", label: "Cameras", adminOnly: true },
  { id: "groups", label: "Groups", adminOnly: true },
  { id: "floorplans", label: "Floorplans", adminOnly: true },
  { id: "users", label: "Users", adminOnly: true },
  { id: "rules", label: "Rules", adminOnly: true },
  { id: "directory", label: "Directory", adminOnly: true },
  { id: "audit", label: "Audit", adminOnly: false },
  { id: "health", label: "Health", adminOnly: false },
  { id: "roles", label: "Roles", adminOnly: true }, // reveals with manageUsers
  // Wave 6 stream D (System Dashboard, Logs, Basics); stream A rehomes these into the tree.
  { id: "site", label: "Site information", adminOnly: true },
  { id: "tasks", label: "Current tasks", adminOnly: false },
  { id: "evidence", label: "Evidence locks", adminOnly: false },
  { id: "report", label: "Configuration report", adminOnly: true },
  { id: "logs", label: "Server logs", adminOnly: false },
  { id: "timeprofiles", label: "Time profiles", adminOnly: true },
  { id: "webhooks", label: "Webhooks", adminOnly: true },
  { id: "notifications", label: "Notifications", adminOnly: true },
  { id: "zones", label: "Zones & privacy", adminOnly: true },
  { id: "cloud", label: "Sentinel Cloud", adminOnly: true }, // WAVE12(C)
  // WAVE14(C): read-only for anyone with ViewLive; each action button checks its own permission.
  { id: "insights", label: "Insights", adminOnly: false },
  // WAVE16(F): readable with ViewLive; changes check ChangeSettings in the section.
  { id: "people", label: "People", adminOnly: false },
  { id: "plates", label: "Plates", adminOnly: false },
];

const streamLabel: Record<StreamState, string> = {
  connecting: "Connecting…",
  open: "Live",
  reconnecting: "Reconnecting…",
  stopped: "Not live",
  forbidden: "Not live",
};

export default function AdminPage({ user }: { user: V1User }) {
  const isAdmin = user.role === "Admin";
  const tree = useMemo(() => buildTree(user.role, sections), [user.role]);
  const [hash, setHash] = useState(() => window.location.hash);
  useEffect(() => {
    const on = () => setHash(window.location.hash);
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  const route = useMemo(() => parseHash(hash, tree), [hash, tree]);
  const current = route.kind === "leaf" ? route.leaf.id : "";
  const params = useMemo(() => hashParams(hash), [hash]);
  const [drawer, setDrawer] = useState(false);
  useEffect(() => setDrawer(false), [hash]);

  // Tree root title: the site name (GET /api/v2/site), "Sentinel" until it answers.
  const [siteName, setSiteName] = useState("Sentinel");
  useEffect(() => {
    let live = true;
    fetch("/api/v2/site", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => live && j && setSiteName(siteTitle(j.name)))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, []);

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
      {/* WAVE21: the operator's page header (WAVE20-OPERATOR-LOOK.md rule 4). */}
      <PageHeader className="admin-page-head" title="Admin" subtitle={`Sentinel Admin · ${siteName}`}>
        <button type="button" className="btn admin-menu" aria-label="Open navigation menu" aria-expanded={drawer} aria-controls="admin-side" onClick={() => setDrawer((d) => !d)}>
          ☰ Menu
        </button>
        <span className={`badge ${state === "open" ? "green" : "amber"}`} title="Live updates from the management server" role="status">
          <span className="dot" />
          {streamLabel[state]}
        </span>
      </PageHeader>
      <div className={`admin-shell ${drawer ? "drawer-open" : ""}`}>
        <aside id="admin-side" className="admin-side" aria-label="Sentinel Admin sections">
          <NavTree tree={tree} route={route} title={siteName} onNavigate={() => setDrawer(false)} />
        </aside>
        {drawer && <div className="admin-scrim" onClick={() => setDrawer(false)} aria-hidden="true" />}
        <main className="admin-main">
          {route.kind === "leaf" && (
            <nav className="admin-crumbs" aria-label="Breadcrumb">
              <ol>
                {breadcrumb(route.leaf).map((c, i, a) => (
                  <li key={i} aria-current={i === a.length - 1 ? "page" : undefined}>
                    {i === a.length - 1 ? <strong>{c}</strong> : <span>{c}</span>}
                  </li>
                ))}
              </ol>
            </nav>
          )}
          {route.kind === "root" && <Welcome title={siteName} tree={tree} />}
      {current === "overview" && <OverviewSection />}
      {current === "insights" && <InsightsSection />} {/* WAVE14(C) */}
      {current === "servers" && <ServersSection />}
      {current === "recording" && <RecordingSection canEdit={isAdmin} />} {/* WAVE22(C) */}
      {current === "cameras" && <CamerasSection openId={params.get("camera") ?? undefined} />}
      {current === "groups" && <GroupsSection />}
      {current === "floorplans" && <FloorplansSection />}
      {current === "users" && <UsersSection meId={user.id} />}
      {current === "people" && <PeopleSection />} {/* WAVE16(F) */}
      {current === "plates" && <PlatesSection />} {/* WAVE16(F) */}
      {current === "rules" && <RulesSection templateId={params.get("template") ?? undefined} />}
      {current === "directory" && <DirectorySection />}
      {current === "audit" && <AuditSection />}
      {current === "health" && <HealthSection canManage={isAdmin} />}
      {current === "roles" && <RolesSection />}
      {current === "site" && <SiteInfoSection canEdit={isAdmin} />}
      {current === "tasks" && <TasksSection />}
      {current === "evidence" && <EvidenceSection />}
      {current === "report" && <ConfigReportSection />}
      {current === "logs" && <LogsSection />}
      {current === "timeprofiles" && <TimeProfilesSection />}
      {current === "webhooks" && <WebhooksSection />}
      {current === "notifications" && <NotificationsSection />}
      {current === "zones" && <ZonesSection />}
      {current === "cloud" && <CloudSection canEdit={isAdmin} />} {/* WAVE12(C) */}
        </main>
      </div>
    </Live.Provider>
  );
}
