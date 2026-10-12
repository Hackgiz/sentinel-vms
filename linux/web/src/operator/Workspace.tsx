// The signed-in operator workspace: a sidebar (Views, Cameras), a toolbar
// with the Live / Playback switch, the camera grid, and in Playback the
// timeline bar. Alarms and Events open from the toolbar or the sidebar
// footer in place of the grid. Layout and sidebar are remembered per site.
// Layout follows OPERATOR-UX.md.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Confirm, Modal } from "../admin/ui";
import { errorMessage, type View } from "../shared/api";
import type { StreamState } from "../shared/events";
import AlarmQueue from "./AlarmQueue";
import { desktop, type DisplayInfo } from "./bridge";
import CameraGrid from "./CameraGrid";
import { useData } from "./data";
import EventsList from "./EventsList";
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
  viewFromLayout,
  type GridLayout,
} from "./layout";
import Playback from "./Playback";
import { useSigned } from "./session";
import Sidebar from "./Sidebar";
import { parseSidebarState, type SidebarState } from "./sidebarModel";
import { SyncClock } from "./sync";

export type Mode = "live" | "playback";
export type Panel = "alarms" | "events" | null;

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

type Dialog = { kind: "save-as" } | { kind: "rename"; view: View } | { kind: "delete"; view: View } | null;

export default function Workspace({ onSignOut, onSwitchSite }: { onSignOut: () => void; onSwitchSite: () => void }) {
  const { session, me } = useSigned();
  const data = useData();
  const siteId = session.site.id;
  const [mode, setMode] = useState<Mode>("live");
  const [panel, setPanel] = useState<Panel>(null);
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
  const newAlarms = data.alarms.filter((a) => a.state === "New").length;
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
    setPanel(null);
  }, []);

  // Ctrl+1…4: Live, Playback, Alarms, Events. Ctrl+B: sidebar.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      if (e.key === "1") goMode("live");
      else if (e.key === "2") goMode("playback");
      else if (e.key === "3") setPanel("alarms");
      else if (e.key === "4") setPanel("events");
      else if (e.key === "b") setSide((s) => ({ ...s, collapsed: !s.collapsed }));
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [goMode]);

  const place = useCallback(
    (index: number, cameraId: string) => {
      setLayout((l) => assignCamera(l, index, cameraId));
      setSelected(index);
    },
    [setLayout],
  );

  const pickCamera = useCallback(
    (id: string) => {
      const i = firstEmpty(layout);
      place(i >= 0 ? i : (selected ?? layout.tiles.length - 1), id);
    },
    [layout, selected, place],
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

  async function save(target: View | null, viewName: string) {
    setBusy(true);
    setError("");
    try {
      const body = viewFromLayout(viewName, layout);
      const saved = target ? await session.client.views.update(target.id, body) : await session.client.views.create(body);
      await data.reloadViews();
      setGrid((s) => ({ ...s, viewId: saved.id }));
      setDialog(null);
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  const preset = presetOf(layout);
  const stageProps = {
    layout,
    selected,
    onSelect: setSelected,
    onAssign: place,
    onSwap: (a: number, b: number) => setLayout((l) => swapTiles(l, a, b)),
    onClear: (i: number) => setLayout((l) => clearTile(l, i)),
  };

  return (
    <div className="op-shell op-ws">
      <Sidebar
        state={side}
        onState={(fn) => setSide(fn)}
        shown={shown}
        viewId={viewId}
        dirty={dirty}
        onOpenView={(v) => {
          setGrid({ layout: layoutFromView(v), viewId: v.id });
          setSelected(null);
          setPanel(null);
        }}
        onRenameView={(v) => {
          setName(v.name);
          setError("");
          setDialog({ kind: "rename", view: v });
        }}
        onDeleteView={(v) => setDialog({ kind: "delete", view: v })}
        onUpdateView={(v) => void save(v, v.name)}
        onSaveAs={() => {
          setName(view ? `${view.name} copy` : "");
          setError("");
          setDialog({ kind: "save-as" });
        }}
        onPickCamera={(c) => pickCamera(c.id)}
        onOpenAsGrid={(ids) => {
          setGrid({ layout: openAsGrid(ids), viewId: null });
          setSelected(0);
          setPanel(null);
        }}
        newAlarms={newAlarms}
        onAlarms={() => setPanel(panel === "alarms" ? null : "alarms")}
        onEvents={() => setPanel(panel === "events" ? null : "events")}
        panel={panel}
      />

      <div className="op-col">
        <div className="op-toolbar op-tb">
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
              <button className="btn small" disabled={busy} onClick={() => void save(view, view!.name)} title="Update this view with the current grid">
                Save
              </button>
            </>
          )}
          <div className="grow" />
          <button className={`btn small ${panel === "alarms" ? "on" : ""}`} onClick={() => setPanel(panel === "alarms" ? null : "alarms")} title="Ctrl+3">
            Alarms {newAlarms > 0 && <span className="nav-count">{newAlarms}</span>}
          </button>
          <button className={`btn small ${panel === "events" ? "on" : ""}`} onClick={() => setPanel(panel === "events" ? null : "events")} title="Ctrl+4">
            Events
          </button>
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
          {panel === "alarms" && <AlarmQueue onPlayback={openPlayback} />}
          {panel === "events" && <EventsList onPlayback={openPlayback} />}
          {!panel && mode === "live" && <CameraGrid {...stageProps} />}
          {!panel && mode === "playback" && (
            <Playback clock={clock} {...stageProps} request={request} onRequestDone={() => setRequest(null)} onLive={() => goMode("live")} />
          )}
        </main>
      </div>

      {(dialog?.kind === "save-as" || dialog?.kind === "rename") && (
        <Modal
          title={dialog.kind === "rename" ? "Rename view" : "Save view"}
          onClose={() => setDialog(null)}
          onSubmit={() => {
            if (!name.trim()) return;
            if (dialog.kind === "rename") {
              // Renaming keeps the stored layout, not the unsaved one on screen.
              setBusy(true);
              session.client.views
                .update(dialog.view.id, { name: name.trim(), columns: dialog.view.columns, rows: dialog.view.rows, tiles: dialog.view.tiles })
                .then(() => data.reloadViews())
                .then(() => setDialog(null))
                .catch((e) => setError(errorMessage(e)))
                .finally(() => setBusy(false));
            } else {
              void save(null, name);
            }
          }}
          actions={
            <>
              <button type="button" className="btn" onClick={() => setDialog(null)}>
                Cancel
              </button>
              <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
                {dialog.kind === "rename" ? "Rename" : "Save"}
              </button>
            </>
          }
        >
          <label className="field">
            <span>Name</span>
            <input type="text" autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
            {dialog.kind === "save-as" && (
              <small>
                {layout.columns}×{layout.rows}, {shown.size} camera{shown.size === 1 ? "" : "s"}. Views are yours; other users don't see them.
              </small>
            )}
          </label>
          {error && <div className="error">{error}</div>}
        </Modal>
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
            Delete <strong>{dialog.view.name}</strong>? The cameras stay on screen until you change the layout.
          </p>
        </Confirm>
      )}
    </div>
  );
}
