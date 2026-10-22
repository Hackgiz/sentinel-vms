// WAVE20: the Home workspace, the dashboard a sign-in opens on (rule 2 of
// WAVE20-OPERATOR-LOOK.md). WAVE21: the dashboard itself is
// shared/HomeDashboard.tsx (the server dashboard shows it too); this hands it
// the workspace's session and live data.
import HomeDashboard, { type HomeDashboardProps } from "../shared/HomeDashboard";
import { useData } from "./data";
import { useSigned } from "./session";

export type HomeViewProps = Omit<HomeDashboardProps, "source">;

export default function HomeView(p: HomeViewProps) {
  const { session, can } = useSigned();
  const data = useData();
  return (
    <HomeDashboard
      {...p}
      source={{
        client: session.client,
        siteName: session.site.name,
        address: session.site.address,
        stream: data.stream,
        cameras: data.cameras,
        alarms: data.alarms,
        subscribe: data.subscribe,
        putAlarm: data.putAlarm,
        mayAck: can("acknowledgeAlarms"),
      }}
    />
  );
}
