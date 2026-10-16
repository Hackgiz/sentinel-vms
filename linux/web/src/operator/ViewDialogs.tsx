// Dialogs of the view tree: save a view (name and where it goes), name a
// folder, move a shared view into a folder.
import { useState } from "react";
import { Modal } from "../admin/ui";
import type { ViewGroup } from "../shared/api";
import { folderOptions } from "./viewTreeModel";

export interface SaveTarget {
  name: string;
  shared: boolean;
  groupId: string;
}

/** Save as…: a name, and Private or (with manage rights) Shared in a folder. */
export function SaveViewDialog({
  initialName,
  summary,
  canManage,
  groups,
  busy,
  error,
  onSave,
  onClose,
}: {
  initialName: string;
  summary: string;
  canManage: boolean;
  groups: ViewGroup[];
  busy: boolean;
  error: string;
  onSave: (t: SaveTarget) => void;
  onClose: () => void;
}) {
  const [name, setName] = useState(initialName);
  const [shared, setShared] = useState(false);
  const [groupId, setGroupId] = useState("");
  const folders = folderOptions(groups);
  return (
    <Modal
      title="Save view"
      onClose={onClose}
      onSubmit={() => name.trim() && onSave({ name: name.trim(), shared: canManage && shared, groupId: canManage && shared ? groupId : "" })}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
            Save
          </button>
        </>
      }
    >
      <label className="field">
        <span>Name</span>
        <input type="text" autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
        <small>{summary}</small>
      </label>
      {canManage && (
        <>
          <label className="field">
            <span>Save to</span>
            <select value={shared ? "shared" : "private"} onChange={(e) => setShared(e.target.value === "shared")}>
              <option value="private">Private (only you)</option>
              <option value="shared">Shared (everyone)</option>
            </select>
          </label>
          {shared && (
            <label className="field">
              <span>Folder</span>
              <select value={groupId} onChange={(e) => setGroupId(e.target.value)}>
                <option value="">Top level</option>
                {folders.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.label}
                  </option>
                ))}
              </select>
            </label>
          )}
        </>
      )}
      {!canManage && <small className="muted">Views you save are yours; other users don't see them.</small>}
      {error && <div className="error">{error}</div>}
    </Modal>
  );
}

export function FolderDialog({ title, initial, busy, error, onSave, onClose }: { title: string; initial: string; busy: boolean; error: string; onSave: (name: string) => void; onClose: () => void }) {
  const [name, setName] = useState(initial);
  return (
    <Modal
      title={title}
      onClose={onClose}
      onSubmit={() => name.trim() && onSave(name.trim())}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !name.trim()}>
            Save
          </button>
        </>
      }
    >
      <label className="field">
        <span>Folder name</span>
        <input type="text" autoFocus maxLength={100} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      {error && <div className="error">{error}</div>}
    </Modal>
  );
}

export function MoveDialog({ current, groups, busy, error, onMove, onClose }: { current: string; groups: ViewGroup[]; busy: boolean; error: string; onMove: (groupId: string) => void; onClose: () => void }) {
  const [groupId, setGroupId] = useState(current);
  const folders = folderOptions(groups);
  return (
    <Modal
      title="Move to folder"
      onClose={onClose}
      onSubmit={() => onMove(groupId)}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            Move
          </button>
        </>
      }
    >
      <label className="field">
        <span>Folder</span>
        <select value={groupId} autoFocus onChange={(e) => setGroupId(e.target.value)}>
          <option value="">Top level</option>
          {folders.map((f) => (
            <option key={f.id} value={f.id}>
              {f.label}
            </option>
          ))}
        </select>
      </label>
      {error && <div className="error">{error}</div>}
    </Modal>
  );
}
