// Sentinel Operator: site picker → sign-in → workspace, or (in a pop-out
// window) one camera or view of a site the main window is signed in to.
import { useCallback, useEffect, useMemo, useState } from "react";
import { errorMessage, permissionsOf, type Me } from "../shared/api";
import { desktop, loadSites, saveSites, type PendingCertificate } from "./bridge";
import CertPrompt from "./CertPrompt";
import { DataProvider } from "./data";
import Popout from "./Popout";
import { closeSession, openSession, refusedCertificate, SignedContext, type Session, type Signed } from "./session";
import SignIn, { takeBrowserOidcReturn } from "./SignIn";
import SitePicker from "./SitePicker";
import { removeSite, touchSite, upsertSite, type Site } from "./sites";
import Workspace from "./Workspace";

export type Route = { kind: "main" } | { kind: "popout-camera"; siteId: string; cameraId: string } | { kind: "popout-view"; siteId: string; viewId: string };

/** `#/popout/camera/<siteId>/<cameraId>` and `#/popout/view/<siteId>/<viewId>` (desktop pop-out windows). */
export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").map(decodeURIComponent);
  if (parts[0] === "popout" && parts[2] && parts[3]) {
    if (parts[1] === "camera") return { kind: "popout-camera", siteId: parts[2], cameraId: parts[3] };
    if (parts[1] === "view") return { kind: "popout-view", siteId: parts[2], viewId: parts[3] };
  }
  return { kind: "main" };
}

type Phase =
  | { kind: "loading" }
  | { kind: "picker"; error?: string }
  | { kind: "connecting"; site: Site }
  | { kind: "signin"; session: Session; error?: string }
  | { kind: "cert"; site: Site; cert: PendingCertificate; session?: Session }
  | { kind: "signed"; session: Session; me: Me }
  | { kind: "popout-wait"; session: Session; error?: string };

export default function App({ route }: { route: Route }) {
  const [sites, setSites] = useState<Site[]>([]);
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const popout = route.kind !== "main";

  const storeSites = useCallback(async (next: Site[]) => {
    setSites(next);
    await saveSites(next);
  }, []);

  /** Open a site: straight in with saved tokens, else the sign-in form. */
  const connect = useCallback(
    async (site: Site, code?: string) => {
      setPhase({ kind: "connecting", site });
      let session: Session;
      try {
        session = await openSession(site);
      } catch (e) {
        setPhase({ kind: "picker", error: errorMessage(e) });
        return;
      }
      try {
        if (code) await session.client.signInWithOidcCode(code);
        if (!session.store.get()) {
          setPhase(popout ? { kind: "popout-wait", session } : { kind: "signin", session });
          return;
        }
        const me = await session.client.me();
        setPhase({ kind: "signed", session, me });
      } catch (e) {
        const cert = await refusedCertificate(site, e);
        if (cert) setPhase({ kind: "cert", site, cert, session });
        else if (popout) setPhase({ kind: "popout-wait", session, error: errorMessage(e) });
        else setPhase({ kind: "signin", session, error: session.store.get() ? errorMessage(e) : undefined });
      }
    },
    [popout],
  );

  useEffect(() => {
    let alive = true;
    void loadSites().then((list) => {
      if (!alive) return;
      setSites(list);
      if (route.kind !== "main") {
        const site = list.find((s) => s.id === route.siteId);
        if (site) void connect(site);
        else setPhase({ kind: "picker", error: "This window's site is no longer saved." });
        return;
      }
      const sso = desktop ? null : takeBrowserOidcReturn();
      const ssoSite = sso && list.find((s) => s.id === sso.siteId);
      if (sso && ssoSite) {
        if ("code" in sso) void connect(ssoSite, sso.code);
        else setPhase({ kind: "picker", error: sso.error });
      } else {
        setPhase({ kind: "picker" });
      }
    });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Signed out here, in another window, or by the server (401): back to the sign-in form.
  const session = phase.kind === "signed" || phase.kind === "signin" || phase.kind === "popout-wait" ? phase.session : null;
  useEffect(() => {
    if (!session) return;
    session.client.setUnauthorizedHandler(() => setPhase((p) => (p.kind === "signed" && p.session === session ? (popout ? { kind: "popout-wait", session } : { kind: "signin", session, error: "You're signed out. Sign in again." }) : p)));
    return session.store.subscribe((t) => {
      if (!t) setPhase((p) => (p.kind === "signed" && p.session === session ? (popout ? { kind: "popout-wait", session } : { kind: "signin", session }) : p));
      else if (popout) {
        // The main window signed in again: pick it up.
        setPhase((p) => {
          if (p.kind === "popout-wait" && p.session === session) void session.client.me().then((me) => setPhase({ kind: "signed", session, me }));
          return p;
        });
      }
    });
  }, [session, popout]);

  // Leaving a session (other site, sign-out) closes it.
  useEffect(() => () => void (session && closeSession(session)), [session]);

  const signed = useMemo<Signed | null>(() => {
    if (phase.kind !== "signed") return null;
    const perms = new Set(phase.me.permissions?.length ? phase.me.permissions : permissionsOf(phase.me.role));
    return { session: phase.session, me: phase.me, can: (p) => perms.has(p) };
  }, [phase]);

  const backToPicker = useCallback(async () => {
    await desktop?.setSite(null);
    setPhase({ kind: "picker" });
  }, []);

  switch (phase.kind) {
    case "loading":
    case "connecting":
      return (
        <div className="auth">
          <div className="scan-status" role="status">
            <span className="spinner" aria-hidden="true" />
            {phase.kind === "connecting" ? `Connecting to ${phase.site.name}…` : "Loading…"}
          </div>
        </div>
      );
    case "picker":
      return (
        <>
          {phase.error && <div className="error op-toast">{phase.error}</div>}
          <SitePicker
            sites={sites}
            onConnect={(s) => void connect(s)}
            onSave={async (input) => {
              const { sites: next } = upsertSite(sites, input);
              await storeSites(next);
            }}
            onRemove={(s) => {
              void storeSites(removeSite(sites, s.id));
              void desktop?.tokens.save(s.id, null);
            }}
          />
        </>
      );
    case "cert":
      return (
        <CertPrompt
          site={phase.site}
          cert={phase.cert}
          onTrusted={() => void connect(phase.site)}
          onCancel={() => void backToPicker()}
        />
      );
    case "signin":
      return (
        <SignIn
          key={phase.session.site.id}
          session={phase.session}
          initialError={phase.error}
          onCertificate={(cert) => setPhase({ kind: "cert", site: phase.session.site, cert, session: phase.session })}
          onBack={() => void backToPicker()}
          onSignedIn={async (name) => {
            void storeSites(touchSite(sites, phase.session.site.id, name));
            try {
              const me = await phase.session.client.me();
              setPhase({ kind: "signed", session: phase.session, me });
            } catch (e) {
              setPhase({ kind: "signin", session: phase.session, error: errorMessage(e) });
            }
          }}
        />
      );
    case "popout-wait":
      return (
        <div className="auth">
          <div className="card">
            <h1>{phase.session.site.name}</h1>
            <p className="lead">{phase.error ?? "Sign in to this site in the main Sentinel Operator window."}</p>
          </div>
        </div>
      );
    case "signed":
      return (
        <SignedContext.Provider value={signed}>
          <DataProvider>
            {route.kind === "main" ? (
              <Workspace
                onSignOut={async () => {
                  await phase.session.client.signOut();
                  setPhase({ kind: "signin", session: phase.session });
                }}
                onSwitchSite={() => void backToPicker()}
              />
            ) : (
              <Popout route={route} />
            )}
          </DataProvider>
        </SignedContext.Provider>
      );
  }
}
