import { useMemo, useState } from "react";
import type { AlertOn, Camera, CameraGroup, CameraInput, CameraPatch, RecordingServer } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { ChipPicker, Confirm, Empty, ErrorBanner, Loading, Modal, OnlineBadge, useLiveMessages, useLiveReload } from "./ui";

const alertChoices: { value: AlertOn; label: string }[] = [
  { value: "auto", label: "Automatic (people & vehicles when detection is installed)" },
  { value: "peopleAndVehicles", label: "People & vehicles" },
  { value: "people", label: "People only" },
  { value: "motion", label: "Any motion" },
];

export default function CamerasSection() {
  const cameras = useLoad(() => v2.cameras.list(), []);
  const servers = useLoad(() => v2.servers.list(), []);
  const groups = useLoad(() => v2.groups.list(), []);
  const [editing, setEditing] = useState<Camera | "new" | null>(null);
  const [moving, setMoving] = useState<Camera | null>(null);
  const [deleting, setDeleting] = useState<Camera | null>(null);
  const [serverFilter, setServerFilter] = useState("");
  const [query, setQuery] = useState("");

  useLiveReload(cameras.reload, ["config.changed"]);
  useLiveReload(servers.reload, ["recorder.status"]);
  useLiveMessages((m) => {
    if (m.type !== "camera.status") return;
    cameras.setData((list) => list?.map((c) => (c.id === m.data.cameraId ? { ...c, status: { ...c.status, online: m.data.online } } : c)) ?? list);
  });

  const serverName = useMemo(() => new Map((servers.data ?? []).map((s) => [s.id, s.name])), [servers.data]);
  const groupName = useMemo(() => new Map((groups.data ?? []).map((g) => [g.id, g.name])), [groups.data]);
  const all = cameras.data ?? [];
  const shown = all.filter(
    (c) =>
      (!serverFilter || c.recorderId === serverFilter) &&
      (!query || `${c.name} ${c.location}`.toLowerCase().includes(query.toLowerCase())),
  );
  const reloadAll = () => {
    void cameras.reload();
    void groups.reload();
  };

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Cameras</h1>
          <div className="sub">
            {cameras.data ? `${all.length} on ${new Set(all.map((c) => c.recorderId)).size} server${new Set(all.map((c) => c.recorderId)).size === 1 ? "" : "s"}` : "Every camera on the site"}
          </div>
        </div>
        <input type="text" placeholder="Filter…" aria-label="Filter cameras" value={query} onChange={(e) => setQuery(e.target.value)} className="head-input" />
        <select aria-label="Recording server" value={serverFilter} onChange={(e) => setServerFilter(e.target.value)} className="head-input">
          <option value="">All servers</option>
          {(servers.data ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
        <button className="btn primary" onClick={() => setEditing("new")} disabled={!servers.data}>
          + Add camera
        </button>
      </div>
      <ErrorBanner error={cameras.error || servers.error} onRetry={() => (cameras.reload(), servers.reload())} />
      <div className="card table-wrap">
        {cameras.data === null ? (
          cameras.error ? null : <Loading />
        ) : all.length === 0 ? (
          <Empty icon="◉" title="No cameras yet">
            Add one with its RTSP address, e.g. rtsp://192.168.1.20:554/stream1.
          </Empty>
        ) : shown.length === 0 ? (
          <Empty icon="◉" title="No matching cameras" />
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Name</th>
                <th>Server</th>
                <th>Groups</th>
                <th>Status</th>
                <th>Recording</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {shown.map((c) => (
                <tr key={c.id}>
                  <td>
                    <strong>{c.name}</strong>
                    {c.location && <div className="mono">{c.location}</div>}
                  </td>
                  <td>{serverName.get(c.recorderId) ?? <span className="mono">{c.recorderId || "—"}</span>}</td>
                  <td>
                    {c.groupIds.length === 0 ? (
                      <span className="muted small">None</span>
                    ) : (
                      <div className="chip-grid">
                        {c.groupIds.map((g) => (
                          <span key={g} className="badge blue">
                            {groupName.get(g) ?? "Removed group"}
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                  <td>
                    <OnlineBadge online={c.status.online} on="Online" off={c.status.message || "Offline"} />
                    {c.status.analytics && <div className="mono">Analytics: {c.status.analytics}</div>}
                  </td>
                  <td>
                    {c.recording ? <span className="badge red">24/7</span> : <span className="badge blue">Live only</span>}
                    <div className="mono">{c.retentionDays > 0 ? `Keep ${c.retentionDays} d` : "Default retention"}</div>
                  </td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(c)}>
                      Edit
                    </button>
                    <button className="btn small" onClick={() => setMoving(c)} disabled={(servers.data ?? []).filter((s) => !s.revoked).length < 2}>
                      Move
                    </button>
                    <button className="btn small danger" onClick={() => setDeleting(c)}>
                      Delete…
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {editing && servers.data && (
        <CameraEditor
          camera={editing === "new" ? null : editing}
          servers={servers.data}
          groups={groups.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reloadAll();
          }}
        />
      )}
      {moving && servers.data && (
        <MoveDialog
          camera={moving}
          servers={servers.data}
          onClose={() => setMoving(null)}
          onMoved={() => {
            setMoving(null);
            reloadAll();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete camera"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.cameras.remove(deleting.id);
            setDeleting(null);
            reloadAll();
          }}
        >
          <p>The camera stops recording and disappears from every view, group and rule.</p>
          <p className="muted small">Recordings already made are kept on the recording server until retention removes them.</p>
        </Confirm>
      )}
    </>
  );
}

const moveNote =
  "Footage recorded so far stays on the old server and stays playable: playback finds it there. New recordings go to the new server.";

function MoveDialog({ camera, servers, onClose, onMoved }: { camera: Camera; servers: RecordingServer[]; onClose: () => void; onMoved: () => void }) {
  const choices = servers.filter((s) => !s.revoked && s.id !== camera.recorderId);
  const [to, setTo] = useState(choices[0]?.id ?? "");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const from = servers.find((s) => s.id === camera.recorderId);
  return (
    <Modal
      title={`Move ${camera.name}`}
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          await v2.cameras.update(camera.id, { recorderId: to });
          onMoved();
        } catch (e) {
          setError(e);
          setBusy(false);
        }
      }}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !to}>
            Move camera
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <p className="muted">Now on {from?.name ?? camera.recorderId}.</p>
      <label className="field">
        <span>Move to</span>
        <select value={to} onChange={(e) => setTo(e.target.value)} autoFocus>
          {choices.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {s.online ? "" : " (offline)"}
            </option>
          ))}
        </select>
      </label>
      <div className="notice">{moveNote}</div>
    </Modal>
  );
}

interface Form {
  name: string;
  location: string;
  rtspUrl: string;
  subRtspUrl: string;
  username: string;
  password: string;
  clearPassword: boolean;
  recording: boolean;
  retentionDays: number;
  motionLevel: number;
  alertOn: AlertOn;
  onvifUrl: string;
  cameraAnalytics: boolean;
  recorderId: string;
  groupIds: string[];
  sortOrder: number;
}

function formOf(c: Camera | null, servers: RecordingServer[]): Form {
  if (c)
    return {
      name: c.name,
      location: c.location,
      rtspUrl: c.rtspUrl,
      subRtspUrl: c.subRtspUrl,
      username: c.username,
      password: "",
      clearPassword: false,
      recording: c.recording,
      retentionDays: c.retentionDays,
      motionLevel: c.motionLevel,
      alertOn: c.alertOn,
      onvifUrl: c.onvifUrl,
      cameraAnalytics: c.cameraAnalytics,
      recorderId: c.recorderId,
      groupIds: c.groupIds,
      sortOrder: c.sortOrder,
    };
  const def = servers.find((s) => s.local && !s.revoked) ?? servers.find((s) => !s.revoked);
  return {
    name: "",
    location: "",
    rtspUrl: "",
    subRtspUrl: "",
    username: "",
    password: "",
    clearPassword: false,
    recording: true,
    retentionDays: 0,
    motionLevel: 2,
    alertOn: "auto",
    onvifUrl: "",
    cameraAnalytics: true,
    recorderId: def?.id ?? "",
    groupIds: [],
    sortOrder: 0,
  };
}

/** Only what changed (absent fields are kept by the server). */
export function cameraPatch(c: Camera, f: Form): CameraPatch {
  const p: CameraPatch = {};
  const keys = ["name", "location", "rtspUrl", "subRtspUrl", "username", "recording", "retentionDays", "motionLevel", "alertOn", "onvifUrl", "cameraAnalytics", "recorderId", "sortOrder"] as const;
  for (const k of keys) if (f[k] !== c[k]) (p as Record<string, unknown>)[k] = f[k];
  if ([...f.groupIds].sort().join() !== [...c.groupIds].sort().join()) p.groupIds = f.groupIds;
  if (f.clearPassword) p.password = "";
  else if (f.password) p.password = f.password;
  return p;
}

function CameraEditor({
  camera,
  servers,
  groups,
  onClose,
  onSaved,
}: {
  camera: Camera | null;
  servers: RecordingServer[];
  groups: CameraGroup[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [f, setF] = useState<Form>(() => formOf(camera, servers));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((x) => ({ ...x, [k]: v }));
  const analytics = !!f.onvifUrl.trim() && f.cameraAnalytics;
  const moving = camera && f.recorderId !== camera.recorderId;
  const usable = servers.filter((s) => !s.revoked || s.id === f.recorderId);

  async function save() {
    setBusy(true);
    setError(null);
    try {
      if (camera) {
        const p = cameraPatch(camera, f);
        if (Object.keys(p).length) await v2.cameras.update(camera.id, p);
      } else {
        const body: CameraInput = {
          name: f.name.trim(),
          location: f.location,
          rtspUrl: f.rtspUrl.trim(),
          subRtspUrl: f.subRtspUrl.trim(),
          username: f.username,
          password: f.password || undefined,
          recording: f.recording,
          retentionDays: f.retentionDays,
          motionLevel: f.motionLevel,
          alertOn: f.alertOn,
          onvifUrl: f.onvifUrl.trim(),
          cameraAnalytics: f.cameraAnalytics,
          recorderId: f.recorderId || undefined,
          groupIds: f.groupIds,
        };
        await v2.cameras.create(body);
      }
      onSaved();
    } catch (e) {
      setError(e);
      setBusy(false);
    }
  }

  return (
    <Modal
      title={camera ? `Edit ${camera.name}` : "Add camera"}
      wide
      onClose={onClose}
      onSubmit={save}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy}>
            {busy ? "Saving…" : camera ? "Save" : "Add camera"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <div className="grid-2">
        <label className="field">
          <span>Name</span>
          <input type="text" value={f.name} onChange={(e) => set("name", e.target.value)} required autoFocus />
        </label>
        <label className="field">
          <span>Location</span>
          <input type="text" value={f.location} onChange={(e) => set("location", e.target.value)} placeholder="Front door, Warehouse…" />
        </label>
      </div>
      <label className="field">
        <span>Recording server</span>
        <select value={f.recorderId} onChange={(e) => set("recorderId", e.target.value)} required>
          {!f.recorderId && <option value="">Choose…</option>}
          {usable.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {s.revoked ? " (revoked)" : s.online ? "" : " (offline)"}
            </option>
          ))}
        </select>
        {moving ? <small className="move-note">{moveNote}</small> : <small>The server that connects to this camera and records it.</small>}
      </label>
      <label className="field">
        <span>Main stream (RTSP)</span>
        <input type="text" value={f.rtspUrl} onChange={(e) => set("rtspUrl", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream1" required />
        <small>Recorded at full quality. A user name and password typed into the address move into the fields below.</small>
      </label>
      <label className="field">
        <span>Sub-stream (optional)</span>
        <input type="text" value={f.subRtspUrl} onChange={(e) => set("subRtspUrl", e.target.value)} placeholder="rtsp://192.168.1.20:554/stream2" />
        <small>Low-resolution stream for live viewing; saves CPU and bandwidth.</small>
      </label>
      <div className="grid-2">
        <label className="field">
          <span>Username</span>
          <input type="text" value={f.username} onChange={(e) => set("username", e.target.value)} autoComplete="off" />
        </label>
        <label className="field">
          <span>Password</span>
          <input
            type="password"
            value={f.password}
            disabled={f.clearPassword}
            onChange={(e) => set("password", e.target.value)}
            placeholder={camera?.hasPassword ? "•••••• (unchanged)" : ""}
            autoComplete="new-password"
          />
        </label>
      </div>
      {camera?.hasPassword && (
        <label className="check">
          <input type="checkbox" checked={f.clearPassword} onChange={(e) => set("clearPassword", e.target.checked)} />
          Remove the saved password
        </label>
      )}
      <div className="field">
        <span className="field-label" id="cam-groups">
          Groups
        </span>
        <ChipPicker label="Groups" items={groups} selected={f.groupIds} onChange={(ids) => set("groupIds", ids)} empty="No groups yet. Create them under Groups." />
      </div>
      <label className="check">
        <input type="checkbox" checked={f.recording} onChange={(e) => set("recording", e.target.checked)} />
        Record 24/7
      </label>
      <div className="grid-2">
        <label className="field">
          <span>Keep recordings</span>
          <select value={f.retentionDays} onChange={(e) => set("retentionDays", Number(e.target.value))}>
            <option value={0}>Site default</option>
            {[1, 3, 7, 14, 30, 60, 90, 180, 365].map((d) => (
              <option key={d} value={d}>
                {d} day{d === 1 ? "" : "s"}
              </option>
            ))}
            {![0, 1, 3, 7, 14, 30, 60, 90, 180, 365].includes(f.retentionDays) && <option value={f.retentionDays}>{f.retentionDays} days</option>}
          </select>
        </label>
        <label className="field">
          <span>Motion detection</span>
          <select value={f.motionLevel} onChange={(e) => set("motionLevel", Number(e.target.value))}>
            <option value={0}>Off</option>
            <option value={1}>Low sensitivity</option>
            <option value={2}>Medium sensitivity</option>
            <option value={3}>High sensitivity</option>
          </select>
        </label>
      </div>
      <label className="field">
        <span>Raise alarms for</span>
        <select value={f.alertOn} onChange={(e) => set("alertOn", e.target.value as AlertOn)} disabled={f.motionLevel === 0 && !analytics}>
          {alertChoices.map((a) => (
            <option key={a.value} value={a.value}>
              {a.label}
            </option>
          ))}
        </select>
        <small>Everything still appears under Events; this only decides what becomes an alarm.</small>
      </label>
      <label className="field">
        <span>ONVIF address (optional)</span>
        <input type="text" value={f.onvifUrl} onChange={(e) => set("onvifUrl", e.target.value)} placeholder="http://192.168.1.20/onvif/device_service" />
      </label>
      <label className="check">
        <input type="checkbox" checked={analytics} disabled={!f.onvifUrl.trim()} onChange={(e) => set("cameraAnalytics", e.target.checked)} />
        Use the camera's built-in analytics (ONVIF Profile M)
      </label>
      {camera && (
        <label className="field">
          <span>Display order</span>
          <input type="number" value={f.sortOrder} onChange={(e) => set("sortOrder", Number(e.target.value) || 0)} />
          <small>Lower numbers come first in camera lists and the live grid.</small>
        </label>
      )}
    </Modal>
  );
}
