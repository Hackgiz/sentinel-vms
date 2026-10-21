// The signed-in user's permissions for an Admin section (GET /api/v2/me),
// the same source Insights uses: Me.permissions, else the role's. Until it
// answers, nothing is allowed, so write actions stay hidden. A host page
// (the Operator, WAVE17) passes its own check and client instead.
import { useMemo } from "react";
import { permissionsOf, type ApiClient, type Permission } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";

export function useCan(client: ApiClient = v2, hostCan?: (p: Permission) => boolean): (p: Permission) => boolean {
  const me = useLoad(() => (hostCan ? Promise.resolve(null) : client.me()), [client, !hostCan]);
  const meCan = useMemo(() => {
    const perms = new Set<Permission>(me.data ? (me.data.permissions?.length ? me.data.permissions : permissionsOf(me.data.role)) : []);
    return (p: Permission) => perms.has(p);
  }, [me.data]);
  return hostCan ?? meCan;
}
