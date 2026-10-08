import { useCallback, useEffect, useState } from "react";
import { api, can, setUnauthorizedHandler, type User } from "./api";
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
import FeedbackDialog from "./pages/FeedbackDialog";

type Page = "live" | "alarms" | "events" | "recordings" | "cameras" | "phones" | "users" | "audit" | "system";

const pages: { id: Page; label: string; icon: string; allowed: (u: User) => boolean }[] = [
  { id: "live", label: "Live", icon: "▦", allowed: () => true },
  { id: "alarms", label: "Alarms", icon: "⚑", allowed: () => true },
  { id: "events", label: "Events", icon: "✦", allowed: () => true },
  { id: "recordings", label: "Recordings", icon: "▶", allowed: () => true },
  { id: "cameras", label: "Cameras", icon: "◉", allowed: can.manageCameras },
  { id: "phones", label: "iPhone", icon: "▯", allowed: can.manageUsers },
  { id: "users", label: "Users", icon: "◎", allowed: can.manageUsers },
  { id: "audit", label: "Audit Log", icon: "☰", allowed: can.viewAudit },
  { id: "system", label: "System", icon: "⚙", allowed: () => true },
];

function pageFromHash(): Page {
  const id = window.location.hash.replace(/^#\/?/, "") as Page;
  return pages.some((p) => p.id === id) ? id : "live";
}

export default function App() {
  const [loading, setLoading] = useState(true);
  const [setupRequired, setSetupRequired] = useState(false);
  const [user, setUser] = useState<User | null>(null);
  const [version, setVersion] = useState("");
  const [page, setPage] = useState<Page>(pageFromHash);
  const [newAlarms, setNewAlarms] = useState(0);
  const [feedbackOpen, setFeedbackOpen] = useState(false);
  const refreshAlarmCount = useCallback(() => {
    api
      .alarms(false)
      .then((a) => setNewAlarms(a.filter((x) => x.state === "New").length))
      .catch(() => {});
  }, []);
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
    const onHash = () => setPage(pageFromHash());
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

  const visible = pages.filter((p) => p.allowed(user));
  const current = visible.some((p) => p.id === page) ? page : "live";

  return (
    <div className="shell">
      <nav className="sidebar">
        <div className="brand">
          <img src="/favicon.svg" alt="" />
          <div>
            Sentinel VMS<small>Linux {version !== "dev" ? `· ${version}` : ""}</small>
          </div>
        </div>
        {visible.map((p) => (
          <a key={p.id} href={`#/${p.id}`} className={`nav-item ${current === p.id ? "active" : ""}`}>
            <span className="icon">{p.icon}</span>
            {p.label}
            {p.id === "alarms" && newAlarms > 0 && <span className="nav-count">{newAlarms}</span>}
          </a>
        ))}
        <div className="spacer" />
        <div className="side-help">
          <button className="nav-item" onClick={() => setFeedbackOpen(true)}>
            <span className="icon">✉</span>Send feedback
          </button>
          <a className="nav-item" href="https://sentvms.com/support" target="_blank" rel="noopener noreferrer">
            <span className="icon">?</span>Help
          </a>
        </div>
        <div className="whoami">
          <strong>{user.name}</strong>
          {user.role}
          <button
            className="btn small"
            style={{ marginTop: 8, width: "100%" }}
            onClick={async () => {
              await api.logout().catch(() => {});
              setUser(null);
            }}
          >
            Sign out
          </button>
        </div>
      </nav>
      <main className="main">
        {current === "live" && <LivePage user={user} />}
        {current === "alarms" && <AlarmsPage user={user} onChange={refreshAlarmCount} />}
        {current === "events" && <EventsPage />}
        {current === "recordings" && <RecordingsPage />}
        {current === "cameras" && <CamerasPage />}
        {current === "phones" && <PhonesPage />}
        {current === "users" && <UsersPage me={user} />}
        {current === "audit" && <AuditPage />}
        {current === "system" && <SystemPage user={user} />}
      </main>
      {feedbackOpen && <FeedbackDialog onClose={() => setFeedbackOpen(false)} />}
    </div>
  );
}
