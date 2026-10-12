// A connection to one site: the bearer-token v2 client and its token store.

import { createContext, useContext } from "react";
import { ApiError, createClient, type ApiClient, type Me, type Permission } from "../shared/api";
import type { VideoRoute } from "../shared/video";
import { desktop, type PendingCertificate } from "./bridge";
import type { Site } from "./sites";
import { SharedTokenStore } from "./tokens";

export const clientName = desktop ? `Sentinel Operator (${desktop.platform === "linux" ? "Linux" : desktop.platform})` : "Sentinel Operator (browser)";

export interface Session {
  site: Site;
  client: ApiClient;
  store: SharedTokenStore;
  /**
   * Where video comes from. The desktop app goes straight to the recording
   * server (it answers cross-origin checks for its own pages, see
   * linux/desktop/src/site.ts); a browser uses the gateway unless the
   * recording server is on its origin.
   */
  route: VideoRoute;
}

export async function openSession(site: Site): Promise<Session> {
  const store = await SharedTokenStore.open(site.id, desktop?.tokens ?? null);
  await desktop?.setSite(site.address);
  const client = createClient({ baseUrl: site.address, tokens: store, clientName });
  return { site, client, store, route: desktop ? "direct" : "auto" };
}

export function closeSession(s: Session) {
  s.store.close();
}

/** The signed-in session and user, for every page under the workspace. */
export interface Signed {
  session: Session;
  me: Me;
  can: (p: Permission) => boolean;
}

export const SignedContext = createContext<Signed | null>(null);

export function useSigned(): Signed {
  const s = useContext(SignedContext);
  if (!s) throw new Error("useSigned outside a signed-in workspace");
  return s;
}

/**
 * The desktop app refused this site's certificate (self-signed, unknown
 * CA)? Then the failure is a network error and the app has the
 * certificate for the trust prompt.
 */
export async function refusedCertificate(site: Site, e: unknown): Promise<PendingCertificate | null> {
  if (!desktop || !(e instanceof ApiError) || e.code !== "network" || !site.address.startsWith("https:")) return null;
  return desktop.certificates.pending(site.address).catch(() => null);
}
