// The site's live state for the workspace: cameras (with status), groups,
// recording servers, saved views and open alarms, kept current by the event
// stream (camera.status, config.changed, recorder.status, alarm.*) and
// re-read after `lagged` or a reconnect.

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { fetchAll, type Alarm, type Camera, type CameraGroup, type RecordingServer, type View, type ViewGroup } from "../shared/api";
import type { StreamMessage, StreamState } from "../shared/events";
import { useEventStream } from "../shared/hooks";
import type { ExportDraft } from "./exportsModel";
import { streamNotice } from "./healthModel";
import { emitNotice, recorderNotice } from "./notices";
import { useSigned } from "./session";

export interface OperatorData {
  cameras: Camera[] | null;
  groups: CameraGroup[];
  servers: RecordingServer[];
  views: View[];
  /** The Shared tree: other people's and the managers' shared views, and their folders. */
  sharedViews: View[];
  viewGroups: ViewGroup[];
  /** The views have been read at least once (an empty list is real). */
  viewsLoaded: boolean;
  /** Open alarms (New, Acknowledged, Investigating, Snoozed), newest first. */
  alarms: Alarm[];
  error: unknown;
  stream: StreamState;
  reload: () => void;
  reloadViews: () => Promise<void>;
  /** Apply an alarm the user just changed (the stream echoes it too). */
  putAlarm: (a: Alarm) => void;
  /** Every stream message, for pages with their own lists (events). */
  subscribe: (fn: (m: StreamMessage) => void) => () => void;
  camera: (id: string) => Camera | undefined;
  /** A range Playback hands to the Exports workspace; the workspace takes it and clears it. */
  exportDraft: ExportDraft | null;
  setExportDraft: (d: ExportDraft | null) => void;
  /** Whether the camera's recording server offers WebRTC. */
  webrtcFor: (c: Camera) => boolean;
}

const Ctx = createContext<OperatorData | null>(null);

export function useData(): OperatorData {
  const d = useContext(Ctx);
  if (!d) throw new Error("useData outside <DataProvider>");
  return d;
}

const openStates = new Set(["New", "Acknowledged", "Investigating", "Snoozed"]);

function upsertAlarm(list: Alarm[], a: Alarm): Alarm[] {
  const rest = list.filter((x) => x.id !== a.id);
  if (!openStates.has(a.state)) return rest;
  return [a, ...rest].sort((x, y) => y.updatedAt - x.updatedAt);
}

export function DataProvider({ children }: { children: ReactNode }) {
  const { session } = useSigned();
  const client = session.client;
  const [cameras, setCameras] = useState<Camera[] | null>(null);
  const [groups, setGroups] = useState<CameraGroup[]>([]);
  const [servers, setServers] = useState<RecordingServer[]>([]);
  const [views, setViews] = useState<View[]>([]);
  const [sharedViews, setSharedViews] = useState<View[]>([]);
  const [viewGroups, setViewGroups] = useState<ViewGroup[]>([]);
  const [viewsLoaded, setViewsLoaded] = useState(false);
  const [alarms, setAlarms] = useState<Alarm[]>([]);
  const [exportDraft, setExportDraft] = useState<ExportDraft | null>(null);
  const [error, setError] = useState<unknown>(null);
  const subscribers = useRef(new Set<(m: StreamMessage) => void>());
  const latest = useRef({ cameras, servers }); // what the stream handler compares transitions against
  latest.current = { cameras, servers };
  const serversRef = useRef<RecordingServer[]>([]);
  serversRef.current = servers;

  const loadCameras = useCallback(async () => {
    const [c, g] = await Promise.all([client.cameras.list(), client.groups.list().catch(() => [] as CameraGroup[])]);
    setCameras(c);
    setGroups(g);
  }, [client]);
  const loadServers = useCallback(async () => setServers(await client.servers.list().catch(() => [] as RecordingServer[])), [client]);
  const reloadViews = useCallback(async () => {
    const [mine, tree] = await Promise.all([client.views.list(), client.viewGroups.tree().catch(() => ({ groups: [] as ViewGroup[], views: [] as View[] }))]);
    setViews(mine);
    setSharedViews(tree.views);
    setViewGroups(tree.groups);
    setViewsLoaded(true);
  }, [client]);
  const loadAlarms = useCallback(async () => setAlarms(await fetchAll((cursor) => client.alarms.page({ state: "open", cursor, limit: 200 }), 1000)), [client]);

  const reload = useCallback(() => {
    Promise.all([loadCameras(), loadServers(), reloadViews(), loadAlarms()])
      .then(() => setError(null))
      .catch(setError);
  }, [loadCameras, loadServers, reloadViews, loadAlarms]);
  useEffect(reload, [reload]);

  const onMessage = useCallback(
    (m: StreamMessage) => {
      if (m.type === "camera.status") emitNotice(streamNotice(m, latest.current.cameras, latest.current.servers)); // camera transitions; recorder ones go through recorderNotice below
      switch (m.type) {
        case "camera.status":
          setCameras((cs) => cs?.map((c) => (c.id === m.data.cameraId ? { ...c, status: { ...c.status, online: m.data.online, message: m.data.online ? "Online" : c.status.message === "Online" ? "No signal" : c.status.message } } : c)) ?? cs);
          break;
        case "config.changed":
          void loadCameras().catch(() => {});
          break;
        case "recorder.status": {
          const known = serversRef.current.find((x) => x.id === m.data.recorderId);
          const note = known ? recorderNotice(known.name, known.online, m.data.online, known.id) : null;
          if (note) emitNotice(note);
          setServers((ss) => ss.map((s) => (s.id === m.data.recorderId ? { ...s, online: m.data.online } : s)));
          // Cameras on it change state too; their own camera.status follows, but re-read to be sure.
          void loadCameras().catch(() => {});
          break;
        }
        case "alarm.new":
        case "alarm.updated":
          setAlarms((as) => upsertAlarm(as, m.data));
          break;
      }
      for (const fn of subscribers.current) fn(m);
    },
    [loadCameras],
  );
  const { state } = useEventStream(client, {
    topics: ["alarm", "event", "camera", "config"],
    // recorder.status; a server that doesn't know the topic refuses the upgrade.
    optionalTopics: ["servers"],
    onMessage,
    onResync: () => {
      reload();
      for (const fn of subscribers.current) fn({ type: "lagged", at: Date.now(), data: { dropped: 0 } });
    },
  });

  const value = useMemo<OperatorData>(() => {
    const byId = new Map((cameras ?? []).map((c) => [c.id, c]));
    const serverById = new Map(servers.map((s) => [s.id, s]));
    return {
      cameras,
      groups,
      servers,
      views,
      sharedViews,
      viewGroups,
      viewsLoaded,
      alarms,
      error,
      stream: state,
      reload,
      reloadViews,
      putAlarm: (a) => setAlarms((as) => upsertAlarm(as, a)),
      subscribe: (fn) => {
        subscribers.current.add(fn);
        return () => {
          subscribers.current.delete(fn);
        };
      },
      camera: (id) => byId.get(id),
      exportDraft,
      setExportDraft,
      // Unknown server (list not readable yet): try WebRTC, VideoTile falls back to HLS.
      webrtcFor: (c) => serverById.get(c.recorderId)?.capabilities.webrtc ?? true,
    };
  }, [cameras, groups, servers, views, sharedViews, viewGroups, viewsLoaded, alarms, error, state, reload, reloadViews, exportDraft]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
