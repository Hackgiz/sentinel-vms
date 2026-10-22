import { useCallback, useEffect, useState } from "react";
import { api, can, setUnauthorizedHandler, type UpdateStatus, type User } from "./api";
import AuthScreen from "./pages/AuthScreen";
import LivePage from "./pages/LivePage";
import RecordingsPage from "./pages/RecordingsPage";
import CamerasPage from "./pages/CamerasPage";
import UsersPage from "./pages/UsersPage";
import AuditPage from "./pages/AuditPage";
import SystemPage from "./pages/SystemPage";
import PhonesPage from "./pages/PhonesPage";
import AlarmsPage from "./pages/AlarmsPage";
import EventsPage from "./pages/EventsPage";
import RulesPage from "./pages/RulesPage";
import FeedbackDialog from "./pages/FeedbackDialog";
import AdminPage from "./admin/AdminPage";
import LinkPage from "./pages/LinkPage";
import { isLinkRoute } from "./pages/linkCode";
import CloudPage from "./pages/CloudPage"; // WAVE13(C)
import HomePage from "./pages/HomePage"; // WAVE21
import Rail, { RailMenu } from "./shared/Rail"; // WAVE21
import { Icon } from "./shared/Icons";
import { siteTitle } from "./admin/navModel";
import { alarmBadge, currentPage, pageFromHash, visiblePages, type Page } from "./dashboardNav";

export default function App() {
  const [loading, setLoading] = useState(true);
  const [setupRequired, setSetupRequired] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [version, setVersion] = useState("");
  const [page, setPage] = useState<Page>(() => pageFromHash(window.location.hash));
  const [siteName, setSiteName] = useState("Sentinel");
  const [reachable, setReachable] = useState(true);
  const [newAlarms, setNewAlarms] = useState(0);
  const [feedbackOpen, setFeedbackOpen] = useState(false);
  const [update, setUpdate] = useState<UpdateStatus | null>(null);
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem("sentinel_update_dismissed") ?? "";
    } catch {
      return "";
    }
  });
  // Admins can act on an update, so only they see the banner.
  useEffect(() => {
    if (!user || !can.changeSettings(user)) return;
    const load = () => api.update().then(setUpdate).catch(() => {});
    load();
    const t = setInterval(load, 60 * 60 * 1000);
    return () => clearInterval(t);
  }, [user]);
  const refreshAlarmCount = useCallback(() => {
    api
      .alarms(false)
      .then((a) => {
        setNewAlarms(a.filter((x) => x.state === "New").length);
        setReachable(true);
      })
      .catch(() => setReachable(false));
  }, []);
  // The rail's site name (GET /api/v2/site), "Sentinel" until it answers.
  useEffect(() => {
    if (!user) return;
    let live = true;
    fetch("/api/v2/site", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((j) => live && j && setSiteName(siteTitle(j.name)))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [user]);
  useEffect(() => {
    if (!user) return;
    refreshAlarmCount();
    const t = setInterval(refreshAlarmCount, 10000);
    return () => clearInterval(t);
  }, [user, refreshAlarmCount]);

  const refreshStatus = useCallback(async () => {
    try {
      const s = await api.status();
      setSetupRequired(s.setupRequired);
      setUser(s.user ?? null);
      setVersion(s.version);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null));
    refreshStatus();
    const onHash = () => setPage(pageFromHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [refreshStatus]);

  if (loading) return null;
  if (!user) {
    return (
      <AuthScreen
        setup={setupRequired}
        onSignedIn={(u) => {
          setUser(u);
          setSetupRequired(false);
        }}
      />
    );
  }

  // /link: approve a Sentinel TV sign-in (WAVE10(C), device codes).
  if (isLinkRoute()) return <LinkPage user={user} />;

  const visible = visiblePages(user);
  const current = currentPage(page, visible);
  const signOut = async () => {
    await api.logout().catch(() => {});
    setUser(null);
  };

  return (
    <div className="shell">
      <Rail
        className="dash-rail"
        label="Pages"
        items={visible.map((p) => {
          const badge = p.id === "alarms" ? alarmBadge(newAlarms) : "";
          return { id: p.id, label: p.short, icon: p.icon, title: p.label, badge, ariaLabel: badge ? `${p.label}, ${newAlarms} new alarms` : p.label };
        })}
        active={current}
        siteName={siteName}
        online={reachable}
        status={reachable ? "Connected" : "Can't reach the server"}
        onSelect={(id) => {
          window.location.hash = `#/${id}`;
        }}
        foot={
          <RailMenu label={`${user.name} · ${user.role}`}>
            <div className="op-menu-label">
              <strong>{user.name}</strong> · {user.role}
              <br />
              Sentinel VMS{version && version !== "dev" ? ` ${version}` : ""}
            </div>
            <button role="menuitem" onClick={() => setFeedbackOpen(true)}>
              <Icon name="mail" size={16} />
              Send feedback
            </button>
            <a role="menuitem" href="https://sentvms.com/support" target="_blank" rel="noopener noreferrer">
              <Icon name="help" size={16} />
              Help
            </a>
            <button role="menuitem" className="danger" onClick={() => void signOut()}>
              <Icon name="logout" size={16} />
              Sign out
            </button>
          </RailMenu>
        }
      />
      <main className="main">
        {update?.available && dismissed !== update.latest && current !== "system" && (
          <div className="notice update-banner">
            <div className="grow">
              <strong>Sentinel {update.latest} is available</strong> (you have {update.current}).
              {update.notes && <> {update.notes}</>}
            </div>
            <a className="btn small primary" href={update.url} target="_blank" rel="noopener noreferrer">
              Download
            </a>
            <a className="btn small" href="#/system">
              How to upgrade
            </a>
            <button
              className="btn small"
              aria-label="Dismiss"
              onClick={() => {
                setDismissed(update.latest);
                try {
                  localStorage.setItem("sentinel_update_dismissed", update.latest);
                } catch {}
              }}
            >
              ×
            </button>
          </div>
        )}
        {current === "home" && <HomePage user={user} siteName={siteName} onAlarmsChange={refreshAlarmCount} />}
        {current === "live" && <LivePage user={user} />}
        {current === "alarms" && <AlarmsPage user={user} onChange={refreshAlarmCount} />}
        {current === "rules" && <RulesPage user={user} />}
        {current === "events" && <EventsPage />}
        {current === "recordings" && <RecordingsPage />}
        {current === "cameras" && <CamerasPage />}
        {current === "phones" && <PhonesPage />}
        {current === "cloud" && <CloudPage user={user} />} {/* WAVE13(C) */}
        {current === "users" && <UsersPage me={user} />}
        {current === "audit" && <AuditPage />}
        {current === "system" && <SystemPage user={user} />}
        {current === "admin" && <AdminPage user={user} />}
      </main>
      {feedbackOpen && <FeedbackDialog onClose={() => setFeedbackOpen(false)} />}
    </div>
  );
}
