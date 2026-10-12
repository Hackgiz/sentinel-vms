import { useMemo, useState } from "react";
import type { Camera, CameraGroup } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { ChipPicker, Confirm, Empty, ErrorBanner, Loading, Modal, useLiveReload } from "./ui";

export default function GroupsSection() {
  const groups = useLoad(() => v2.groups.list(), []);
  const cameras = useLoad(() => v2.cameras.list(), []);
  const [editing, setEditing] = useState<CameraGroup | "new" | null>(null);
  const [deleting, setDeleting] = useState<CameraGroup | null>(null);
  useLiveReload(cameras.reload, ["config.changed"]);
  useLiveReload(groups.reload, ["config.changed"]);
  const camName = useMemo(() => new Map((cameras.data ?? []).map((c) => [c.id, c.name])), [cameras.data]);
  const list = groups.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Groups</h1>
          <div className="sub">Camera groups decide what users scoped to them can see, e.g. "Warehouse" or "Front office".</div>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + New group
        </button>
      </div>
      <ErrorBanner error={groups.error || cameras.error} onRetry={() => (groups.reload(), cameras.reload())} />
      <div className="card table-wrap">
        {groups.data === null ? (
          groups.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="▣" title="No groups yet">
            Without groups every user sees every camera. Create a group to give someone access to only part of the site.
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Group</th>
                <th>Cameras</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((g) => (
                <tr key={g.id}>
                  <td>
                    <strong>{g.name}</strong>
                    <div className="mono">
                      {g.cameraIds.length} camera{g.cameraIds.length === 1 ? "" : "s"}
                    </div>
                  </td>
                  <td>
                    {g.cameraIds.length === 0 ? (
                      <span className="muted small">No cameras</span>
                    ) : (
                      <div className="chip-grid">
                        {g.cameraIds.slice(0, 8).map((id) => (
                          <span key={id} className="badge blue">
                            {camName.get(id) ?? "Removed camera"}
                          </span>
                        ))}
                        {g.cameraIds.length > 8 && <span className="badge blue">+{g.cameraIds.length - 8} more</span>}
                      </div>
                    )}
                  </td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(g)}>
                      Edit
                    </button>
                    <button className="btn small danger" onClick={() => setDeleting(g)}>
                      Delete…
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {editing && (
        <GroupEditor
          group={editing === "new" ? null : editing}
          cameras={cameras.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void groups.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete group"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.groups.remove(deleting.id);
            setDeleting(null);
            void groups.reload();
          }}
        >
          <p>The cameras stay; only the group goes.</p>
          <p className="muted small">Users scoped only to this group will then see no cameras until you change their scope.</p>
        </Confirm>
      )}
    </>
  );
}

function GroupEditor({ group, cameras, onClose, onSaved }: { group: CameraGroup | null; cameras: Camera[]; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(group?.name ?? "");
  const [ids, setIds] = useState<string[]>(group?.cameraIds ?? []);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      title={group ? `Edit ${group.name}` : "New group"}
      wide
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          const body = { name: name.trim(), cameraIds: ids };
          if (group) await v2.groups.update(group.id, body);
          else await v2.groups.create(body);
          onSaved();
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
          <button className="btn primary" disabled={busy || !name.trim()}>
            {group ? "Save" : "Create group"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} maxLength={100} required onChange={(e) => setName(e.target.value)} autoFocus />
      </label>
      <div className="field">
        <span className="field-label">Cameras</span>
        <ChipPicker label="Cameras" items={cameras} selected={ids} onChange={setIds} empty="No cameras yet." />
      </div>
    </Modal>
  );
}
