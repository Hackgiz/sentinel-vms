// WAVE21: the dashboard's landing page, the operator's Home dashboard
// (shared/HomeDashboard.tsx) on the dashboard's cookie client: cameras and
// open alarms read here and kept current by one event stream; the links go
// to the dashboard's own pages.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { can, type User } from "../api";
import { v2 } from "../admin/client";
import { siteTitle } from "../admin/navModel";
import { homeCan, homeLinks } from "../dashboardNav";
import { fetchAll, type Alarm, type Camera } from "../shared/api";
import type { StreamMessage } from "../shared/events";
import HomeDashboard from "../shared/HomeDashboard";
import { useEventStream } from "../shared/hooks";

const openStates = new Set(["New", "Acknowledged", "Investigating", "Snoozed"]);

function upsertAlarm(list: Alarm[], a: Alarm): Alarm[] {
  const rest = list.filter((x) => x.id !== a.id);
  if (!openStates.has(a.state)) return rest;
  return [a, ...rest].sort((x, y) => y.updatedAt - x.updatedAt);
}

const go = (hash: string) => {
  window.location.hash = hash;
};

export default function HomePage({ user, siteName, onAlarmsChange }: { user: User; siteName: string; onAlarmsChange?: () => void }) {
  const [cameras, setCameras] = useState<Camera[] | null>(null);
  const [alarms, setAlarms] = useState<Alarm[]>([]);
  const subscribers = useRef(new Set<(m: StreamMessage) => void>());

  const loadCameras = useCallback(() => v2.cameras.list().then(setCameras).catch(() => {}), []);
  const loadAlarms = useCallback(
    () =>
      fetchAll((cursor) => v2.alarms.page({ state: "open", cursor, limit: 200 }), 1000)
        .then(setAlarms)
        .catch(() => {}),
    [],
  );
  useEffect(() => {
    void loadCameras();
    void loadAlarms();
  }, [loadCameras, loadAlarms]);

  const { state } = useEventStream(v2, {
    topics: ["alarm", "event", "camera", "config"],
    onMessage: (m) => {
      switch (m.type) {
        case "camera.status":
          setCameras((cs) => cs?.map((c) => (c.id === m.data.cameraId ? { ...c, status: { ...c.status, online: m.data.online } } : c)) ?? cs);
          break;
        case "config.changed":
          void loadCameras();
          break;
        case "alarm.new":
        case "alarm.updated":
          setAlarms((as) => upsertAlarm(as, m.data));
          onAlarmsChange?.();
          break;
      }
      for (const fn of subscribers.current) fn(m);
    },
    onResync: () => {
      void loadCameras();
      void loadAlarms();
      for (const fn of subscribers.current) fn({ type: "lagged", at: Date.now(), data: { dropped: 0 } });
    },
  });

  const subscribe = useCallback((fn: (m: StreamMessage) => void) => {
    subscribers.current.add(fn);
    return () => {
      subscribers.current.delete(fn);
    };
  }, []);
  const putAlarm = useCallback(
    (a: Alarm) => {
      setAlarms((as) => upsertAlarm(as, a));
      onAlarmsChange?.();
    },
    [onAlarmsChange],
  );

  const source = useMemo(
    () => ({
      client: v2,
      siteName: siteTitle(siteName),
      address: window.location.origin,
      stream: state,
      cameras,
      alarms,
      subscribe,
      putAlarm,
      mayAck: can.acknowledge(user),
    }),
    [siteName, state, cameras, alarms, subscribe, putAlarm, user],
  );

  return (
    <HomeDashboard
      source={source}
      can={homeCan(user)}
      onOpenCamera={(id) => go(homeLinks.camera(id))}
      onOpenLive={() => go(homeLinks.live)}
      onOpenEvents={() => go(homeLinks.events)}
      onOpenAlarms={() => go(homeLinks.alarms)}
      onOpenInsights={() => go(homeLinks.insights)}
      onOpenPeople={() => go(homeLinks.people)}
      onPlayback={(r) => go(homeLinks.playback(r.cameraId, r.at))}
    />
  );
}
