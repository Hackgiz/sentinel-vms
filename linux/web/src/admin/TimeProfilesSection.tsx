import { useEffect, useRef, useState } from "react";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading, Modal } from "./ui";
import { CELL_MINUTES, CELLS_PER_DAY, describeSpans, emptyGrid, gridToSpans, spansToGrid, type Grid, type Span } from "./timeProfile";
import "./timeProfiles.css";

type Profile = { id: string; name: string; weekly: Span[] };

const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export default function TimeProfilesSection() {
  const profiles = useLoad(async () => (await v2.call("get", "/api/v2/time-profiles")).items as Profile[], []);
  const [editing, setEditing] = useState<Profile | "new" | null>(null);
  const [deleting, setDeleting] = useState<Profile | null>(null);
  const list = profiles.data ?? [];

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Time profiles</h1>
          <div className="sub">
            Weekly schedules a rule can use: outside its profile the rule doesn't fire. Times are the management server's local time; a window that runs past midnight
            belongs to the day it started.
          </div>
        </div>
        <button className="btn primary" onClick={() => setEditing("new")}>
          + New profile
        </button>
      </div>
      <ErrorBanner error={profiles.error} onRetry={profiles.reload} />
      <div className="card table-wrap">
        {profiles.data === null ? (
          profiles.error ? null : <Loading />
        ) : list.length === 0 ? (
          <Empty icon="◷" title="No time profiles yet">
            Create one such as "Business hours" or "Nights and weekends", then pick it in a rule.
          </Empty>
        ) : (
          <table className="list">
            <thead>
              <tr>
                <th>Profile</th>
                <th>Active</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.map((p) => (
                <tr key={p.id}>
                  <td>
                    <strong>{p.name}</strong>
                  </td>
                  <td className="small">{describeSpans(p.weekly)}</td>
                  <td className="row-actions">
                    <button className="btn small" onClick={() => setEditing(p)}>
                      Edit
                    </button>
                    <button className="btn small danger" onClick={() => setDeleting(p)}>
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
        <ProfileEditor
          profile={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void profiles.reload();
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete profile"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.call("delete", "/api/v2/time-profiles/{profileId}", { path: { profileId: deleting.id } });
            setDeleting(null);
            void profiles.reload();
          }}
        >
          <p>A profile that rules still use can't be deleted; the error lists them.</p>
        </Confirm>
      )}
    </>
  );
}

function ProfileEditor({ profile, onClose, onSaved }: { profile: Profile | null; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(profile?.name ?? "");
  const [grid, setGrid] = useState<Grid>(() => (profile ? spansToGrid(profile.weekly) : emptyGrid()));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const spans = gridToSpans(grid);

  const paint = useRef<boolean | null>(null);
  useEffect(() => {
    const up = () => (paint.current = null);
    window.addEventListener("pointerup", up);
    window.addEventListener("pointercancel", up);
    return () => {
      window.removeEventListener("pointerup", up);
      window.removeEventListener("pointercancel", up);
    };
  }, []);
  const setCell = (d: number, c: number, on: boolean) => setGrid((g) => (g[d][c] === on ? g : g.map((row, i) => (i === d ? row.map((v, j) => (j === c ? on : v)) : row))));
  const setRow = (d: number, on: boolean) => setGrid((g) => g.map((row, i) => (i === d ? row.map(() => on) : row)));
  const setRange = (ds: number[], fromH: number, toH: number) =>
    setGrid((g) => g.map((row, i) => (ds.includes(i) ? row.map((v, c) => (c >= (fromH * 60) / CELL_MINUTES && c < (toH * 60) / CELL_MINUTES ? true : v)) : row)));
  const cellAt = (e: React.PointerEvent) => {
    const el = document.elementFromPoint(e.clientX, e.clientY) as HTMLElement | null;
    if (!el?.dataset.d) return null;
    return { d: Number(el.dataset.d), c: Number(el.dataset.c) };
  };

  return (
    <Modal
      title={profile ? `Edit ${profile.name}` : "New time profile"}
      wide
      onClose={onClose}
      onSubmit={async () => {
        setBusy(true);
        setError(null);
        try {
          const body = { name: name.trim(), weekly: spans };
          if (profile) await v2.call("put", "/api/v2/time-profiles/{profileId}", { path: { profileId: profile.id }, body });
          else await v2.call("post", "/api/v2/time-profiles", { body });
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
            {profile ? "Save" : "Create profile"}
          </button>
        </>
      }
    >
      <ErrorBanner error={error} />
      <label className="field">
        <span>Name</span>
        <input type="text" value={name} maxLength={100} required placeholder="e.g. Business hours" onChange={(e) => setName(e.target.value)} autoFocus />
      </label>
      <div className="field">
        <span className="field-label">Active times (drag to paint; click a day to fill or clear it)</span>
        <div className="tp-presets">
          <button type="button" className="btn small" onClick={() => setRange([1, 2, 3, 4, 5], 8, 18)}>
            + Weekdays 08–18
          </button>
          <button type="button" className="btn small" onClick={() => setRange([0, 1, 2, 3, 4, 5, 6], 0, 24)}>
            + Always
          </button>
          <button type="button" className="btn small" onClick={() => setGrid(emptyGrid())}>
            Clear all
          </button>
        </div>
        <div
          className="tp-grid"
          role="group"
          aria-label="Weekly schedule"
          onPointerDown={(e) => {
            const hit = cellAt(e);
            if (!hit) return;
            e.preventDefault();
            paint.current = !grid[hit.d][hit.c];
            setCell(hit.d, hit.c, paint.current);
          }}
          onPointerMove={(e) => {
            if (paint.current === null) return;
            const hit = cellAt(e);
            if (hit) setCell(hit.d, hit.c, paint.current);
          }}
        >
          <div className="tp-row tp-hours" aria-hidden="true">
            <span className="tp-day" />
            <div className="tp-cells">
              {[0, 3, 6, 9, 12, 15, 18, 21].map((h) => (
                <span key={h} style={{ left: `${(h / 24) * 100}%` }}>
                  {String(h).padStart(2, "0")}
                </span>
              ))}
            </div>
          </div>
          {days.map((name, d) => (
            <div className="tp-row" key={name}>
              <button type="button" className="tp-day" aria-label={`${name}: ${grid[d].every(Boolean) ? "clear" : "fill"} the whole day`} onClick={() => setRow(d, !grid[d].every(Boolean))}>
                {name}
              </button>
              <div className="tp-cells">
                {Array.from({ length: CELLS_PER_DAY }, (_, c) => (
                  <div
                    key={c}
                    data-d={d}
                    data-c={c}
                    className={`tp-cell ${grid[d][c] ? "on" : ""} ${c % 4 === 0 ? "hour" : ""}`}
                    title={`${name} ${String(Math.floor((c * CELL_MINUTES) / 60)).padStart(2, "0")}:${String((c * CELL_MINUTES) % 60).padStart(2, "0")}`}
                  />
                ))}
              </div>
            </div>
          ))}
        </div>
        <small className="hint">{spans.length === 0 ? "Nothing painted: a rule using this profile never fires." : describeSpans(spans)}</small>
      </div>
    </Modal>
  );
}
