import { useMemo, useState } from "react";
import { ApiError as V1ApiError } from "../api";
import ScanDialog, { profileLabel, type ScanBackend, type ScanPick } from "../pages/ScanDialog";
import type { AlertOn, Camera, CameraGroup, CameraInput, CameraPatch, RecordingServer } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { AUDIO_DEFAULT_DB, AUDIO_MAX_DB, AUDIO_MIN_DB, audioFormOf, audioPatch, clampAudioDb } from "./audioSettings";
import { v2 } from "./client";
import StreamSettingsDialog from "./StreamSettingsDialog";
import { ChipPicker, Confirm, Empty, ErrorBanner, Loading, Modal, OnlineBadge, useLiveMessages, useLiveReload } from "./ui";

/** A camera picked from a scan, bound for the server that found it. */
interface ScannedCamera {
  pick: ScanPick;
  recorderId: string;
}

/** The scan dialog's calls, routed to one recording server over api/v2. */
function scanBackend(serverId: string): ScanBackend {
  return {
    discover: async (sweep) => {
      const r = await v2.servers.discover(serverId, sweep);
      return {
        devices: (r.devices ?? []).map((d) => ({ ...d, manufacturer: d.manufacturer ?? "", model: d.model ?? "", serviceURL: d.serviceURL ?? "" })),
        subnets: r.subnets ?? [],
        warnings: r.warnings ?? [],
      };
    },
    probe: async (host, serviceURL, username, password) => {
      const r = await v2.servers.probe(serverId, { host, serviceURL, username, password });
      if (r.error || !r.details) {
        throw new V1ApiError(r.error || "The camera couldn't be probed.", 422, { authFailed: !!r.authFailed, suggestedURL: r.suggestedURL });
      }
      return { details: r.details, mainURL: r.mainURL, subURL: r.subURL };
    },
  };
}

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
  const [editing, setEditing] = useState<Camera | "new" | ScannedCamera | null>(null);
  const [moving, setMoving] = useState<Camera | null>(null);
  const [deleting, setDeleting] = useState<Camera | null>(null);
  const [streamCam, setStreamCam] = useState<Camera | null>(null);
  const [scanServer, setScanServer] = useState<string | null>(null);
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
        <button
          className="btn"
          onClick={() => {
            const list = servers.data ?? [];
            const def = list.find((x) => x.local && !x.revoked && x.online) ?? list.find((x) => !x.revoked && x.online);
            if (def) setScanServer(def.id);
          }}
          disabled={!(servers.data ?? []).some((x) => !x.revoked && x.online)}
        >
          Scan network
        </button>
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
                    <button className="btn small" onClick={() => setStreamCam(c)} title={c.onvifUrl ? undefined : "Added without ONVIF"} disabled={!c.onvifUrl}>
                      Stream settings…
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
      {scanServer !== null && servers.data && (
        <ScanDialog
          backend={scanBackend(scanServer)}
          rescanKey={scanServer}
          header={
            <label className="field">
              <span>Scan from</span>
              <select value={scanServer} onChange={(e) => setScanServer(e.target.value)}>
                {servers.data
                  .filter((x) => !x.revoked)
                  .map((x) => (
                    <option key={x.id} value={x.id} disabled={!x.online}>
                      {x.name}
                      {x.online ? "" : " (offline)"}
                    </option>
                  ))}
              </select>
              <small>The scan runs on that server's own network — cameras live on the recording server's LAN.</small>
            </label>
          }
          onClose={() => setScanServer(null)}
          onPick={(p) => {
            const recorderId = scanServer;
            setScanServer(null);
            setEditing({ pick: p, recorderId });
          }}
        />
      )}
      {editing && servers.data && (
        <CameraEditor
          camera={editing === "new" || "pick" in editing ? null : editing}
          scanned={editing !== "new" && "pick" in editing ? editing : undefined}
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
      {streamCam && <StreamSettingsDialog camera={streamCam} onClose={() => setStreamCam(null)} />}
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
  audioDetect: boolean;
  audioThresholdDb: number;
  recorderId: string;
  groupIds: string[];
  sortOrder: number;
}

function formOf(c: Camera | null, servers: RecordingServer[], scanned?: ScannedCamera): Form {
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
      ...audioFormOf(c),
      recorderId: c.recorderId,
      groupIds: c.groupIds,
      sortOrder: c.sortOrder,
    };
  const def = servers.find((s) => s.local && !s.revoked) ?? servers.find((s) => !s.revoked);
  const blank: Form = {
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
    ...audioFormOf(null),
    recorderId: def?.id ?? "",
    groupIds: [],
    sortOrder: 0,
  };
  if (!scanned) return blank;
  const i = scanned.pick.initial; // api/v1 field spellings (rtspURL …)
  return {
    ...blank,
    name: i.name,
    location: i.location ?? "",
    rtspUrl: i.rtspURL,
    subRtspUrl: i.subRTSPURL ?? "",
    username: i.username ?? "",
    password: i.password ?? "",
    onvifUrl: i.onvifURL ?? "",
    cameraAnalytics: i.cameraAnalytics !== false,
    recorderId: scanned.recorderId,
  };
}

/** Only what changed (absent fields are kept by the server). */
export function cameraPatch(c: Camera, f: Form): CameraPatch {
  const p: CameraPatch = {};
  const keys = ["name", "location", "rtspUrl", "subRtspUrl", "username", "recording", "retentionDays", "motionLevel", "alertOn", "onvifUrl", "cameraAnalytics", "recorderId", "sortOrder"] as const;
  for (const k of keys) if (f[k] !== c[k]) (p as Record<string, unknown>)[k] = f[k];
  Object.assign(p, audioPatch(c, f));
  if ([...f.groupIds].sort().join() !== [...c.groupIds].sort().join()) p.groupIds = f.groupIds;
  if (f.clearPassword) p.password = "";
  else if (f.password) p.password = f.password;
  return p;
}

function CameraEditor({
  camera,
  scanned,
  servers,
  groups,
  onClose,
  onSaved,
}: {
  camera: Camera | null;
  scanned?: ScannedCamera;
  servers: RecordingServer[];
  groups: CameraGroup[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [f, setF] = useState<Form>(() => formOf(camera, servers, scanned));
  const profiles = scanned?.pick.profiles ?? [];
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
          audioDetect: f.audioDetect,
          audioThresholdDb: f.audioThresholdDb,
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
      title={camera ? `Edit ${camera.name}` : scanned ? "Add discovered camera" : "Add camera"}
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
      {profiles.length > 0 && (
        <div className="notice">
          {profiles.length === 1
            ? "Found the camera's stream and filled it in below."
            : `Found ${profiles.length} streams on this camera. Pick which to record and which to watch live.`}
        </div>
      )}
      {profiles.length > 0 && (
        <label className="field">
          <span>Record this stream</span>
          <select value={f.rtspUrl} onChange={(e) => set("rtspUrl", e.target.value)}>
            {!profiles.some((p) => p.rtspURL === f.rtspUrl) && <option value={f.rtspUrl}>Custom address</option>}
            {profiles.map((p) => (
              <option key={p.token} value={p.rtspURL}>
                {profileLabel(p)}
              </option>
            ))}
          </select>
        </label>
      )}
      {profiles.length > 1 && (
        <label className="field">
          <span>Live view stream</span>
          <select value={f.subRtspUrl} onChange={(e) => set("subRtspUrl", e.target.value)}>
            <option value="">Same as recording</option>
            {profiles
              .filter((p) => p.rtspURL !== f.rtspUrl)
              .map((p) => (
                <option key={p.token} value={p.rtspURL}>
                  {profileLabel(p)}
                  {p.rtspURL === scanned?.pick.recommendedSub ? " (recommended)" : ""}
                </option>
              ))}
          </select>
          <small>A low-res stream for live view saves CPU, but opens a second connection to the camera.</small>
        </label>
      )}
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
      <label className="check">
        <input type="checkbox" checked={f.audioDetect} onChange={(e) => set("audioDetect", e.target.checked)} />
        Detect loud and distinctive sounds (needs a camera with a microphone)
      </label>
      {f.audioDetect && (
        <label className="field">
          <span>Loud noise threshold (dB over background)</span>
          <input
            type="number"
            min={AUDIO_MIN_DB}
            max={AUDIO_MAX_DB}
            value={f.audioThresholdDb}
            onChange={(e) => set("audioThresholdDb", clampAudioDb(Number(e.target.value)))}
          />
          <small>
            Raises an "audio" event when the sound stays this far above the room's background level. Lower is more sensitive; {AUDIO_DEFAULT_DB} suits most rooms.
            {camera?.status.audio ? ` Status: ${camera.status.audio}.` : ""}
          </small>
        </label>
      )}
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
