// The signed-in operator workspace: a tab bar (workspaces and saved-view
// tabs, setup mode, clock), a notification strip, a sidebar (view tree,
// cameras), a toolbar with the Live / Playback switch, the camera grid, and
// in Playback the timeline bar. Events open from the toolbar in place of the
// grid. Layout, sidebar and tabs are remembered per site.
// Layout follows OPERATOR-UX.md.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Confirm, Modal } from "../admin/ui";
import { errorMessage, type View, type ViewGroup } from "../shared/api";
import { emitNotice } from "./notices";
import type { StreamState } from "../shared/events";
import AlarmManagerView from "./AlarmManagerView";
import Banners from "./Banners";
import ExportsHub from "./ExportsHub"; // WAVE7(C): wraps ExportsView with long exports + the Scheduled tab
import IncidentsView from "./IncidentsView";
import SystemMonitorView from "./SystemMonitorView";
import { desktop, type DisplayInfo } from "./bridge";
import CameraGrid from "./CameraGrid";
import { useData } from "./data";
import EventsList from "./EventsList";
import SearchView from "./SearchView";
import {
  assignCamera,
  camerasOf,
  clearTile,
  emptyLayout,
  firstEmpty,
  layoutFromView,
  openAsGrid,
  presetOf,
  presets,
  pruneCameras,
  resize,
  sameAsView,
  swapTiles,
  type GridLayout,
} from "./layout";
import MapView from "./MapView";
import ReportsView from "./ReportsView";
import Playback from "./Playback";
import { useSigned } from "./session";
import Sidebar from "./Sidebar";
import TabBar from "./TabBar";
import ViewTab from "./ViewTab";
import { FolderDialog, MoveDialog, SaveViewDialog, type SaveTarget } from "./ViewDialogs";
import { canEditView, newViewInput, viewInputFromLayout, viewInputOf } from "./viewOps";
import { findView } from "./viewTreeModel";
import { activate, closeViewTab, openViewTab, parseTabs, reconcile, serializeTabs, shortcutFor, viewIdOf, visibleFixed, type FixedId, type TabId, type TabsState } from "./workspaces";
import TimelineBar from "./TimelineBar";
import { parseSidebarState, type SidebarState } from "./sidebarModel";
import { SyncClock } from "./sync";

export type Mode = "live" | "playback";

/**
 * The timeline bar in Live mode: the playhead is pinned to now and reads
 * LIVE; any scrub, click or backward step switches the workspace to Playback
 * at that time.
 */
function LiveBar({ cameraName, onPlayback }: { cameraName?: string; onPlayback: (at: number) => void }) {
  const [span, setSpan] = useState(3600e3);
  const clock = useMemo(
    () => ({
      get position() {
        return Date.now();
      },
    }),
    [],
  );
  const lane = cameraName ? { name: cameraName, spans: [], pending: [], markers: [] } : null;
  return (
    <TimelineBar
      clock={clock}
      wall={null}
      playing={false}
      live
      rate={1}
      span={span}
      onSpan={(ms) => setSpan(ms)}
      lane={lane}
      anyLane={[]}
      selection={{ inAt: null, outAt: null }}
      onMarkIn={() => {}}
      onMarkOut={() => {}}
      onClearSelection={() => {}}
      onSeek={onPlayback}
      onPlayPause={() => {}}
      onStep={(ms) => ms < 0 && onPlayback(Date.now() + ms)}
      onGap={(dir) => dir < 0 && onPlayback(Date.now() - 5 * 60e3)}
      onRate={() => {}}
      onLive={() => {}}
      sound={false}
      onSound={() => {}}
    />
  );
}
export type Panel = "alarms" | "events" | "search" | "map" | null;

/** Open playback at a time (from an alarm or an event). */
export interface PlaybackRequest {
  cameraId: string;
  at: number;
}

const streamLabel: Record<StreamState, string> = {
  connecting: "Connecting…",
  open: "Live updates",
  reconnecting: "Reconnecting…",
  stopped: "Not connected",
  forbidden: "Not allowed",
};

interface GridState {
  layout: GridLayout;
  viewId: string | null;
}

const gridKey = (siteId: string) => `operator.live.${siteId}`;
const sideKey = (siteId: string) => `operator.sidebar.${siteId}`;
const tabsKey = (siteId: string) => `operator.tabs.${siteId}`;

function loadGrid(siteId: string): GridState {
  try {
    const s = JSON.parse(localStorage.getItem(gridKey(siteId)) ?? "null") as GridState | null;
    if (s && s.layout && Array.isArray(s.layout.tiles)) return { layout: layoutFromView({ columns: s.layout.columns, rows: s.layout.rows, tiles: s.layout.tiles.filter((t) => t.cameraId).map((t) => ({ ...t, cameraId: t.cameraId! })) }), viewId: s.viewId ?? null };
  } catch {
    /* storage blocked or malformed */
  }
  return { layout: emptyLayout(2, 2), viewId: null };
}

function loadSidebar(siteId: string): SidebarState {
  try {
    return parseSidebarState(localStorage.getItem(sideKey(siteId)));
  } catch {
    return parseSidebarState(null);
  }
}

function loadTabs(siteId: string): TabsState {
  try {
    return parseTabs(localStorage.getItem(tabsKey(siteId)));
  } catch {
    return parseTabs(null);
  }
}

type Dialog =
  | { kind: "save-as"; /** The view tab the layout comes from; null = the Live grid. */ source: string | null }
  | { kind: "rename"; view: View }
  | { kind: "delete"; view: View }
  | { kind: "move"; view: View }
  | { kind: "folder-new"; parentId: string }
  | { kind: "folder-rename"; group: ViewGroup }
  | { kind: "folder-delete"; group: ViewGroup }
  | null;

export default function Workspace({ onSignOut, onSwitchSite }: { onSignOut: () => void; onSwitchSite: () => void }) {
  const { session, me, can } = useSigned();
  const data = useData();
  const siteId = session.site.id;
  const canManage = can("manageViews");
  const [mode, setMode] = useState<Mode>("live");
  const [tabs, setTabs] = useState<TabsState>(() => loadTabs(siteId));
  const [eventsOpen, setEventsOpen] = useState(false);
  const [setup, setSetup] = useState(false);
  /** Layouts being edited in view tabs (not stored until saved). */
  const [tabLayouts, setTabLayouts] = useState<Record<string, GridLayout>>({});
  const [grid, setGrid] = useState<GridState>(() => loadGrid(siteId));
  const [selected, setSelected] = useState<number | null>(null);
  const [side, setSide] = useState<SidebarState>(() => loadSidebar(siteId));
  const [request, setRequest] = useState<PlaybackRequest | null>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [more, setMore] = useState(false);
  const [displays, setDisplays] = useState<DisplayInfo[]>([]);
  const clock = useRef(new SyncClock()).current;
  const { layout, viewId } = grid;
  const view = data.views.find((v) => v.id === viewId) ?? null;
  const dirty = view ? !sameAsView(layout, view) : false;
  const activeView = viewIdOf(tabs.active);
  const activeFixed = activeView === null ? tabs.active : null;
  // Setup only unlocks saved-view tabs; pressed anywhere else, say so
  // instead of looking dead (the Live grid is always editable).
  const toggleSetup = useCallback(() => {
    if (!setup && activeView === null) {
      emitNotice({
        kind: "info",
        key: "setup-hint",
        text: "Setup mode edits saved views. The Live grid is always editable — drag cameras from the tree, then “Save current as view…”. Open a saved view in its own tab to edit it.",
      });
    }
    setSetup((s) => !s);
  }, [setup, activeView]);
  const panel: Panel = eventsOpen && activeFixed === "live" ? "events" : activeFixed === "alarms" || activeFixed === "search" || activeFixed === "map" ? activeFixed : null;
  const shown = useMemo(() => new Set(camerasOf(layout)), [layout]);
  const setLayout = useCallback((fn: (l: GridLayout) => GridLayout) => setGrid((s) => ({ ...s, layout: fn(s.layout) })), []);

  useEffect(() => {
    try {
      localStorage.setItem(gridKey(siteId), JSON.stringify(grid));
    } catch {
      /* storage blocked */
    }
  }, [grid, siteId]);
  useEffect(() => {
    try {
      localStorage.setItem(sideKey(siteId), JSON.stringify(side));
    } catch {
      /* storage blocked */
    }
  }, [side, siteId]);
  useEffect(() => {
    try {
      localStorage.setItem(tabsKey(siteId), serializeTabs(tabs));
    } catch {
      /* storage blocked */
    }
  }, [tabs, siteId]);

  // Cameras that went away (deleted, out of scope) leave their tiles.
  useEffect(() => {
    if (!data.cameras) return;
    const known = new Set(data.cameras.map((c) => c.id));
    setGrid((s) => {
      const l = pruneCameras(s.layout, known);
      return l === s.layout ? s : { ...s, layout: l };
    });
  }, [data.cameras]);

  // A view deleted elsewhere: keep the layout, forget the link.
  useEffect(() => {
    if (viewId && data.viewsLoaded && !data.views.some((v) => v.id === viewId)) setGrid((s) => ({ ...s, viewId: null }));
  }, [data.views, data.viewsLoaded, viewId]);

  useEffect(() => {
    if (!desktop) return;
    void desktop.windows.displays().then(setDisplays).catch(() => {});
  }, []);

  useEffect(() => {
    document.title = `${session.site.name} · Sentinel Operator`;
  }, [session.site.name]);

  const goMode = useCallback((m: Mode) => {
    setMode(m);
    setEventsOpen(false);
    setTabs((t) => activate(t, "live"));
  }, []);

  // Deep link into the Alarm Manager tab (from Incidents, or a banner).
  const [focusAlarm, setFocusAlarm] = useState<string | null>(null);
  const openAlarm = useCallback((alarmId: string) => {
    setFocusAlarm(alarmId);
    setTabs((t) => activate(t, "alarms"));
  }, []);

  // The workspace panes beyond Live. Streams add theirs here (one line each);
  // the tab bar shows exactly the ones listed.
  const panes: Partial<Record<FixedId, () => React.ReactNode>> = {
    alarms: () => <AlarmManagerView onPlayback={openPlayback} focusAlarmId={focusAlarm} onFocusDone={() => setFocusAlarm(null)} />,
    incidents: () => <IncidentsView onPlayback={openPlayback} onOpenAlarm={openAlarm} />,
    exports: () => <ExportsHub />,
    monitor: () => <SystemMonitorView />,
    reports: () => <ReportsView />,
    search: () => <SearchView onPlayback={openPlayback} />,
    map: () => (
      <MapView
        onPlayback={openPlayback}
        onOpenCamera={(id) => {
          // Show it live: on the grid already, else in the next free tile.
          const i = layout.tiles.findIndex((t) => t.cameraId === id);
          if (i >= 0) setSelected(i);
          else pickCamera(id);
          goMode("live");
        }}
      />
    ),
  };
  const available = new Set<FixedId>(["live", ...(Object.keys(panes) as FixedId[])]);
  const fixed = visibleFixed(available);
  const availableKey = [...available].join(",");

  // Ctrl+1…9 (see workspaces.ts). Ctrl+B: sidebar.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      const act = shortcutFor(e.key, new Set(availableKey.split(",") as FixedId[]));
      if (act) {
        if (act.mode) setMode(act.mode);
        setEventsOpen(!!act.events);
        setTabs((t) => activate(t, act.tab));
      } else if (e.key === "b") setSide((s) => ({ ...s, collapsed: !s.collapsed }));
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [availableKey]);

  // View tabs whose view was deleted close; so does a stale active tab.
  useEffect(() => {
    if (!data.viewsLoaded) return;
    setTabs((t) => reconcile(t, new Set(availableKey.split(",") as FixedId[]), (id) => !!findView(id, data.views, data.sharedViews)));
  }, [data.views, data.sharedViews, data.viewsLoaded, availableKey]);

  const openTab = useCallback((v: View) => setTabs((t) => openViewTab(t, v.id)), []);
  const closeTab = useCallback((id: string) => {
    setTabs((t) => closeViewTab(t, id));
    setTabLayouts((m) => {
      const { [id]: _dropped, ...rest } = m;
      return rest;
    });
  }, []);
  const selectTab = useCallback((tab: TabId) => {
    setEventsOpen(false);
    setTabs((t) => activate(t, tab));
  }, []);

  const place = useCallback(
    (index: number, cameraId: string) => {
      setLayout((l) => assignCamera(l, index, cameraId));
      setSelected(index);
    },
    [setLayout],
  );

  // A camera chosen in the tree: into the open view tab while setting it up,
  // else focused in the Live grid (selected if it is shown, else placed).
  const pickCamera = useCallback(
    (id: string) => {
      if (setup && activeView) {
        setTabLayouts((m) => {
          const v = findView(activeView, data.views, data.sharedViews);
          const l = m[activeView] ?? (v ? layoutFromView(v) : null);
          if (!l) return m;
          const i = firstEmpty(l);
          return { ...m, [activeView]: assignCamera(l, i >= 0 ? i : l.tiles.length - 1, id) };
        });
        return;
      }
      setTabs((t) => activate(t, "live"));
      setEventsOpen(false);
      const at = layout.tiles.findIndex((t) => t.cameraId === id);
      if (at >= 0) return setSelected(at);
      const i = firstEmpty(layout);
      place(i >= 0 ? i : (selected ?? layout.tiles.length - 1), id);
    },
    [layout, selected, place, setup, activeView, data.views, data.sharedViews],
  );

  const openPlayback = useCallback(
    (r: PlaybackRequest) => {
      let i = layout.tiles.findIndex((t) => t.cameraId === r.cameraId);
      if (i < 0) {
        i = firstEmpty(layout);
        if (i < 0) i = selected ?? 0;
        setLayout((l) => assignCamera(l, i, r.cameraId));
      }
      setSelected(i);
      setRequest(r);
      goMode("playback");
    },
    [layout, selected, setLayout, goMode],
  );

  /**
   * Store a layout: update `target` (keeping where a shared view sits) or
   * create a view, private or shared in a folder. Null on failure (the
   * error shows in the open dialog).
   */
  async function persist(target: View | null, viewName: string, lay: GridLayout, where = { shared: false, groupId: "" }): Promise<View | null> {
    setBusy(true);
    setError("");
    try {
      const saved = target ? await session.client.views.update(target.id, viewInputFromLayout(target, viewName, lay)) : await session.client.views.create(newViewInput(viewName, lay, where));
      await data.reloadViews();
      return saved;
    } catch (e) {
      setError(errorMessage(e));
      return null;
    } finally {
      setBusy(false);
    }
  }

  /** The Live grid into a view. */
  async function save(target: View | null, viewName: string) {
    const saved = await persist(target, viewName, layout);
    if (!saved) return;
    setGrid((s) => ({ ...s, viewId: saved.id }));
    setDialog(null);
  }

  /** Run a request, then re-read the tree and close the dialog (or show the error in it). */
  async function change(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      await data.reloadViews();
      setDialog(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  // The open view tab: its stored view, and the layout being edited.
  const tabView = activeView ? findView(activeView, data.views, data.sharedViews) : undefined;
  const tabLayout = tabView ? (tabLayouts[tabView.id] ?? layoutFromView(tabView)) : null;
  const tabDirty = tabView && tabLayout ? !sameAsView(tabLayout, tabView) : false;
  const dropTabLayout = (id: string) =>
    setTabLayouts((m) => {
      const { [id]: _dropped, ...rest } = m;
      return rest;
    });
  const tabViews = useMemo(
    () => [...data.views.map((view) => ({ view, where: "Private" })), ...data.sharedViews.map((view) => ({ view, where: "Shared" }))],
    [data.views, data.sharedViews],
  );
  const saveAsSource = dialog?.kind === "save-as" ? dialog.source : null;
  const saveAsLayout = dialog?.kind === "save-as" ? (saveAsSource ? (tabLayouts[saveAsSource] ?? layout) : layout) : layout;

  const preset = presetOf(layout);
  const stageProps = {
    layout,
    selected,
    onSelect: setSelected,
    onAssign: place,
    onSwap: (a: number, b: number) => setLayout((l) => swapTiles(l, a, b)),
    onClear: (i: number) => setLayout((l) => clearTile(l, i)),
  };
  const toggleEvents = () => {
    setTabs((t) => activate(t, "live"));
    setEventsOpen((e) => !(e && activeFixed === "live"));
  };

  return (
    <div className="op-shell">
      <TabBar
        tabs={tabs}
        fixed={fixed}
        views={tabViews}
        alarmCount={data.alarms.length}
        onSelect={selectTab}
        onCloseView={closeTab}
        onOpenView={(id) => setTabs((t) => openViewTab(t, id))}
        setup={setup}
        onSetup={toggleSetup}
      />
      <Banners />
      <div className="op-ws-body">
        <Sidebar
          state={side}
          onState={(fn) => setSide(fn)}
          shown={shown}
          viewId={viewId}
          dirty={dirty}
          onOpenView={(v) => {
            setGrid({ layout: layoutFromView(v), viewId: v.id });
            setSelected(null);
            goMode("live");
          }}
          onOpenTab={openTab}
          onRenameView={(v) => {
            setName(v.name);
            setError("");
            setDialog({ kind: "rename", view: v });
          }}
          onDeleteView={(v) => {
            setError("");
            setDialog({ kind: "delete", view: v });
          }}
          onUpdateView={(v) => void save(v, v.name)}
          onMoveView={(v) => {
            setError("");
            setDialog({ kind: "move", view: v });
          }}
          onSaveAs={() => {
            setError("");
            setDialog({ kind: "save-as", source: tabView && activeView ? activeView : null });
          }}
          onNewFolder={(parentId) => {
            setError("");
            setDialog({ kind: "folder-new", parentId });
          }}
          onRenameFolder={(group) => {
            setError("");
            setDialog({ kind: "folder-rename", group });
          }}
          onDeleteFolder={(group) => setDialog({ kind: "folder-delete", group })}
          onPickCamera={(c) => pickCamera(c.id)}
          onOpenAsGrid={(ids) => {
            setGrid({ layout: openAsGrid(ids), viewId: null });
            setSelected(0);
            goMode("live");
          }}
        />

        <div className="op-col">
          <div className="op-toolbar op-tb">
            {activeFixed === "live" && (
              <>
                <div className="seg op-mode" role="group" aria-label="Mode">
                  <button className={mode === "live" && !panel ? "on" : ""} aria-pressed={mode === "live"} onClick={() => goMode("live")} title="Ctrl+1">
                    Live
                  </button>
                  <button className={mode === "playback" && !panel ? "on" : ""} aria-pressed={mode === "playback"} onClick={() => goMode("playback")} title="Ctrl+2">
                    Playback
                  </button>
                </div>
                <div className="seg" role="group" aria-label="Layout">
                  {presets.map((p) => (
                    <button key={p.id} className={preset === p.id ? "on" : ""} onClick={() => setLayout((l) => resize(l, p.columns, p.rows))} title={`${p.label} grid`}>
                      {p.label}
                    </button>
                  ))}
                </div>
                {view && <span className="op-viewname" title="The open view">{view.name}</span>}
                {dirty && (
                  <>
                    <span className="badge amber">Unsaved changes</span>
                    {canEditView(view!, canManage) && (
                      <button className="btn small" disabled={busy} onClick={() => void save(view, view!.name)} title="Update this view with the current grid">
                        Save
                      </button>
                    )}
                  </>
                )}
              </>
            )}
            <div className="grow" />
            {activeFixed === "live" && (
              <button className={`btn small ${panel === "events" ? "on" : ""}`} onClick={toggleEvents} title="Ctrl+4">
                Events
              </button>
            )}
            <span className={`badge ${data.stream === "open" ? "green" : "amber"}`} role="status" title="Live updates from the Management Server">
              <span className="dot" />
              {streamLabel[data.stream]}
            </span>
            <div className="op-more">
              <button className="btn small" aria-haspopup="menu" aria-expanded={more} aria-label="More" onClick={() => setMore((m) => !m)} onBlur={() => setTimeout(() => setMore(false), 150)}>
                ⋯
              </button>
              {more && (
                <div className="op-menu right" role="menu">
                  <div className="op-menu-label" title={`${me.role} · ${session.site.address}`}>
                    {me.name} · {session.site.name}
                  </div>
                  <button role="menuitem" disabled={!shown.size} onClick={() => setLayout((l) => emptyLayout(l.columns, l.rows))}>
                    Clear the grid
                  </button>
                  {desktop && view && (
                    <button role="menuitem" onClick={() => void desktop!.windows.open({ kind: "view", siteId, viewId: view.id, title: view.name })}>
                      Open this view in a new window
                    </button>
                  )}
                  {desktop &&
                    displays.map((d) => (
                      <button key={d.id} role="menuitem" onClick={() => void desktop!.windows.fullscreen(d.id)}>
                        Full screen on {d.label}
                        {d.primary ? " (main)" : ""}
                      </button>
                    ))}
                  {desktop && displays.length > 0 && (
                    <button role="menuitem" onClick={() => void desktop!.windows.fullscreen(null)}>
                      Leave full screen
                    </button>
                  )}
                  <button role="menuitem" onClick={onSwitchSite}>
                    Sites
                  </button>
                  <button role="menuitem" onClick={onSignOut}>
                    Sign out
                  </button>
                </div>
              )}
            </div>
          </div>

          {data.error !== null && data.cameras === null && (
            <div className="error op-page-error">
              {(data.error as Error)?.message ?? String(data.error)}{" "}
              <button className="btn small" onClick={data.reload}>
                Try again
              </button>
            </div>
          )}

          <main className="op-stage">
            {activeFixed !== null && activeFixed !== "live" && panes[activeFixed as FixedId]?.()}
            {activeFixed === "live" && eventsOpen && <EventsList onPlayback={openPlayback} />}
            {activeFixed === "live" && !eventsOpen && mode === "live" && (
              <>
                <CameraGrid {...stageProps} />
                <LiveBar
                  cameraName={selected !== null ? data.camera(layout.tiles[selected]?.cameraId ?? "")?.name : undefined}
                  onPlayback={(at) => {
                    const id = (selected !== null ? layout.tiles[selected]?.cameraId : undefined) ?? camerasOf(layout)[0] ?? "";
                    openPlayback({ cameraId: id, at });
                  }}
                />
              </>
            )}
            {activeFixed === "live" && !eventsOpen && mode === "playback" && (
              <Playback clock={clock} {...stageProps} request={request} onRequestDone={() => setRequest(null)} onLive={() => goMode("live")} />
            )}
            {tabView && tabLayout && (
              <ViewTab
                key={tabView.id}
                view={tabView}
                layout={tabLayout}
                onLayout={(fn) => setTabLayouts((m) => ({ ...m, [tabView.id]: fn(m[tabView.id] ?? layoutFromView(tabView)) }))}
                setup={setup}
                canUpdate={canEditView(tabView, canManage)}
                dirty={tabDirty}
                busy={busy}
                onSave={() =>
                  void persist(tabView, tabView.name, tabLayout).then((saved) => {
                    if (saved) dropTabLayout(tabView.id);
                  })
                }
                onSaveAs={() => {
                  setError("");
                  setDialog({ kind: "save-as", source: tabView.id });
                }}
                onRevert={() => dropTabLayout(tabView.id)}
                onSetup={toggleSetup}
              />
            )}
            {activeView && !tabView && data.viewsLoaded && <div className="muted op-pad">This view is no longer available.</div>}
          </main>
        </div>
      </div>

      {dialog?.kind === "save-as" && (
        <SaveViewDialog
          initialName={saveAsSource ? `${findView(saveAsSource, data.views, data.sharedViews)?.name ?? "View"} copy` : view ? `${view.name} copy` : ""}
          summary={`${saveAsLayout.columns}×${saveAsLayout.rows}, ${camerasOf(saveAsLayout).length} camera${camerasOf(saveAsLayout).length === 1 ? "" : "s"}.`}
          canManage={canManage}
          groups={data.viewGroups}
          busy={busy}
          error={error}
          onClose={() => setDialog(null)}
          onSave={async (t: SaveTarget) => {
            const saved = await persist(null, t.name, saveAsLayout, t);
            if (!saved) return;
            if (saveAsSource) {
              dropTabLayout(saveAsSource);
              setTabs((s) => openViewTab(s, saved.id));
            } else setGrid((s) => ({ ...s, viewId: saved.id }));
            setDialog(null);
          }}
        />
      )}
      {dialog?.kind === "rename" && (
        <Modal
          title="Rename view"
          onClose={() => setDialog(null)}
          onSubmit={() => {
            if (!name.trim()) return;
            // Renaming keeps the stored layout, not the unsaved one on screen.
            void change(() => session.client.views.update(dialog.view.id, viewInputOf(dialog.view, { name: name.trim() })));
          }}
          actions={
            <>
              <button type="button" className="btn" onClick={() => setDialog(null)}>
                Cancel
              </button>
              <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
                Rename
              </button>
            </>
          }
        >
          <label className="field">
            <span>Name</span>
            <input type="text" autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          {error && <div className="error">{error}</div>}
        </Modal>
      )}
      {dialog?.kind === "move" && (
        <MoveDialog
          current={dialog.view.groupId ?? ""}
          groups={data.viewGroups}
          busy={busy}
          error={error}
          onClose={() => setDialog(null)}
          onMove={(groupId) => void change(() => session.client.views.update(dialog.view.id, viewInputOf(dialog.view, { groupId })))}
        />
      )}
      {(dialog?.kind === "folder-new" || dialog?.kind === "folder-rename") && (
        <FolderDialog
          title={dialog.kind === "folder-new" ? "New folder" : "Rename folder"}
          initial={dialog.kind === "folder-rename" ? dialog.group.name : ""}
          busy={busy}
          error={error}
          onClose={() => setDialog(null)}
          onSave={(folderName) =>
            void change(() =>
              dialog.kind === "folder-new"
                ? session.client.viewGroups.create({ name: folderName, parentId: dialog.parentId })
                : session.client.viewGroups.update(dialog.group.id, { name: folderName, parentId: dialog.group.parentId, sortOrder: dialog.group.sortOrder }),
            )
          }
        />
      )}
      {dialog?.kind === "folder-delete" && (
        <Confirm
          title="Delete folder"
          confirmLabel="Delete"
          onCancel={() => setDialog(null)}
          run={async () => {
            await session.client.viewGroups.remove(dialog.group.id);
            await data.reloadViews();
            setDialog(null);
          }}
        >
          <p>
            Delete the folder <strong>{dialog.group.name}</strong>? Only an empty folder can be deleted.
          </p>
        </Confirm>
      )}
      {dialog?.kind === "delete" && (
        <Confirm
          title="Delete view"
          confirmLabel="Delete"
          onCancel={() => setDialog(null)}
          run={async () => {
            await session.client.views.remove(dialog.view.id);
            await data.reloadViews();
            setGrid((s) => (s.viewId === dialog.view.id ? { ...s, viewId: null } : s));
            setDialog(null);
          }}
        >
          <p>
            Delete <strong>{dialog.view.name}</strong>
            {dialog.view.shared ? ", which everyone can see" : ""}? The cameras stay on screen until you change the layout.
          </p>
        </Confirm>
      )}
    </div>
  );
}
