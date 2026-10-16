// System Monitor load meters: CPU, memory and disk of every recording
// server, coloured by the site's thresholds, with an editor for admins.
import { useEffect, useState, type FormEvent } from "react";
import { useLoad } from "../shared/hooks";
import { Empty, ErrorBanner, Loading, Modal } from "./ui";
import { v2 } from "./client";
import { systemApi, type MonitorThresholds } from "./systemApi";
import { DEFAULT_THRESHOLDS, levelColor, levelFor, thresholdError, type Threshold } from "./thresholds";

/** The thresholds in force; a signed-in non-admin can't read them, so defaults apply. */
export function useThresholds() {
  const t = useLoad(() => systemApi.thresholds.get().catch(() => DEFAULT_THRESHOLDS), []);
  return { thresholds: t.data ?? DEFAULT_THRESHOLDS, reload: t.reload };
}

function Meter({ label, pct, t, detail }: { label: string; pct: number | null; t: Threshold; detail?: string }) {
  const level = levelFor(pct, t);
  return (
    <div className="stat">
      <span>{label}</span>
      <span style={{ display: "flex", alignItems: "center", gap: 8, minWidth: "55%" }}>
        <span
          className="meter grow"
          role="meter"
          aria-label={label}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct === null ? undefined : Math.round(pct)}
          data-level={level}
        >
          <div style={{ width: `${Math.min(100, Math.round(pct ?? 0))}%`, background: levelColor(level) }} />
        </span>
        <span className="mono small">{pct === null ? "—" : `${Math.round(pct)}%`}</span>
        {detail && <span className="muted small">{detail}</span>}
      </span>
    </div>
  );
}

export default function MonitorLoad({ canManage }: { canManage: boolean }) {
  const health = useLoad(() => v2.system.health(), []);
  const { thresholds, reload: reloadThresholds } = useThresholds();
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    const t = setInterval(() => !document.hidden && void health.reload(), 15_000);
    return () => clearInterval(t);
  }, [health.reload]);

  return (
    <>
      <ErrorBanner error={health.error} onRetry={health.reload} />
      <section className="card" aria-label="Server load">
        <div className="health-head">
          <h2>Load</h2>
          {canManage && (
            <button className="btn small" onClick={() => setEditing(true)}>
              Thresholds…
            </button>
          )}
        </div>
        {health.data === null ? (
          health.error ? null : <Loading />
        ) : health.data.length === 0 ? (
          <Empty icon="▤" title="No readings yet" />
        ) : (
          <div className="grid-2">
            {health.data.map((s) => (
              <div key={s.id} aria-label={`${s.name} load`}>
                <strong>{s.name}</strong>
                {!s.online ? (
                  <div className="muted small">Offline</div>
                ) : (
                  <>
                    <Meter label="CPU" pct={s.cpu} t={thresholds.cpu} />
                    <Meter label="Memory" pct={s.mem && s.mem.total > 0 ? (s.mem.used / s.mem.total) * 100 : null} t={thresholds.mem} />
                    <Meter label="Disk" pct={s.disk && s.disk.total > 0 ? (s.disk.used / s.disk.total) * 100 : null} t={thresholds.disk} />
                  </>
                )}
              </div>
            ))}
          </div>
        )}
      </section>
      {editing && (
        <ThresholdsDialog
          current={thresholds}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void reloadThresholds();
          }}
        />
      )}
    </>
  );
}

function ThresholdsDialog({ current, onClose, onSaved }: { current: MonitorThresholds; onClose: () => void; onSaved: () => void }) {
  const [t, setT] = useState<MonitorThresholds>(current);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const problem = thresholdError(t);
  const set = (k: keyof MonitorThresholds, f: keyof Threshold, v: string) => setT((x) => ({ ...x, [k]: { ...x[k], [f]: Number(v) } }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (problem) return;
    setBusy(true);
    setError(null);
    try {
      await systemApi.thresholds.put(t);
      onSaved();
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  }
  const rows: [keyof MonitorThresholds, string][] = [
    ["cpu", "CPU"],
    ["mem", "Memory"],
    ["disk", "Disk"],
  ];
  return (
    <Modal
      title="Monitor thresholds"
      onClose={onClose}
      onSubmit={submit}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !!problem}>
            {busy ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <p className="muted small">A meter turns amber and then red at these percentages.</p>
      <ErrorBanner error={error} />
      {problem && <div className="error">{problem}</div>}
      {rows.map(([k, label]) => (
        <div key={k} className="field" role="group" aria-label={label}>
          <span>{label}</span>
          <label>
            Amber %{" "}
            <input type="number" min={1} max={99} value={t[k].amber} onChange={(e) => set(k, "amber", e.target.value)} />
          </label>{" "}
          <label>
            Red %{" "}
            <input type="number" min={2} max={100} value={t[k].red} onChange={(e) => set(k, "red", e.target.value)} />
          </label>
        </div>
      ))}
    </Modal>
  );
}
