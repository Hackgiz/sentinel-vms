import { useState } from "react";
import { errorMessage, ruleInputOf, type Rule, type RuleInput } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { RuleEditor } from "../pages/RulesPage";
import { dayNames, minuteToTime, type AlarmRule, type Delivery } from "../rulesApi";
import { v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading } from "./ui";
import "../pages/rules.css";

/** Alarm kinds a rule can match (the server's rules.Kinds; API v2 has no endpoint for them yet). */
export const ruleKinds = ["Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering", "Tamper", "Camera Offline", "Storage"];

const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };

const blank: RuleInput = {
  name: "",
  enabled: true,
  cameraIds: [],
  kinds: ["Person"],
  minSeverity: "Info",
  schedule: false,
  fromMinute: 22 * 60,
  toMinute: 6 * 60,
  days: [],
  delivery: "push",
  instructions: "",
};

// The shared editor speaks the v1 rule shape; these convert at the edges.
const toForm = (r: RuleInput | Rule): AlarmRule | Omit<AlarmRule, "id"> => {
  const { cameraIds, delivery, ...rest } = ruleInputOf(r);
  const base = { ...rest, cameraIDs: cameraIds, delivery: (delivery === "dashboard" ? "dashboard" : "push") as Delivery };
  return "id" in r ? { ...base, id: r.id } : base;
};
const fromForm = (f: AlarmRule | Omit<AlarmRule, "id">): RuleInput => ({
  name: f.name.trim(),
  enabled: f.enabled,
  cameraIds: f.cameraIDs,
  kinds: f.kinds,
  minSeverity: f.minSeverity,
  schedule: f.schedule,
  fromMinute: f.fromMinute,
  toMinute: f.toMinute,
  days: f.days,
  delivery: f.delivery,
  instructions: f.instructions,
});

export default function RulesSection() {
  const rules = useLoad(() => v2.rules.list(), []);
  const cameras = useLoad(() => v2.cameras.list(), []);
  const [editing, setEditing] = useState<Rule | RuleInput | null>(null);
  const [deleting, setDeleting] = useState<Rule | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const list = rules.data ?? [];
  const camName = (id: string) => cameras.data?.find((c) => c.id === id)?.name ?? "Removed camera";

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      await rules.reload();
      return true;
    } catch (e) {
      setError(errorMessage(e));
      return false;
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Rules</h1>
          <div className="sub">Rules add operator instructions to matching alarms and decide which alarms are pushed to phones. Times are the management server's time.</div>
        </div>
        <button className="btn primary" onClick={() => setEditing({ ...blank })}>
          + New rule
        </button>
      </div>
      <ErrorBanner error={rules.error} onRetry={rules.reload} />
      {error && !editing && <div className="error">{error}</div>}
      {rules.data === null ? (
        rules.error ? null : (
          <div className="card">
            <Loading />
          </div>
        )
      ) : list.length === 0 ? (
        <div className="card">
          <Empty icon="⚐" title="No alarm rules">
            Alarms still reach operators; without a rule they get no instructions and aren't pushed to phones.
          </Empty>
        </div>
      ) : (
        <div className="rule-list">
          {list.map((r) => (
            <div key={r.id} className={`card rule-row ${r.enabled ? "" : "off"}`}>
              <div className="rule-head">
                <strong>{r.name}</strong>
                <label className="switch-row small">
                  <input
                    type="checkbox"
                    checked={r.enabled}
                    disabled={busy}
                    onChange={() => run(() => v2.rules.update(r.id, { ...ruleInputOf(r), enabled: !r.enabled }))}
                    aria-label={`${r.name} on`}
                  />
                  {r.enabled ? "On" : "Off"}
                </label>
              </div>
              <div className="rule-pills">
                <span className="badge blue">{r.kinds.length ? r.kinds.join(", ") : "Any alarm"}</span>
                <span className={`badge ${sevClass[r.minSeverity] ?? "blue"}`}>{r.minSeverity === "Info" ? "Any severity" : `${r.minSeverity}+`}</span>
                <span className="badge blue">
                  {r.cameraIds.length === 0 ? "All cameras" : r.cameraIds.length <= 2 ? r.cameraIds.map(camName).join(", ") : `${r.cameraIds.length} cameras`}
                </span>
                {r.schedule && (
                  <span className="badge blue">
                    {minuteToTime(r.fromMinute)}–{minuteToTime(r.toMinute)}
                    {r.toMinute < r.fromMinute ? " (overnight)" : ""}
                  </span>
                )}
                {r.days.length > 0 && <span className="badge blue">{r.days.map((d) => dayNames[d]).join(" ")}</span>}
                <span className={`badge ${r.delivery === "push" ? "amber" : "blue"}`}>{r.delivery === "push" ? "Operators + phone push" : "Operators only"}</span>
                <span className="grow" />
                <button className="btn small" onClick={() => setEditing(r)}>
                  Edit
                </button>
                <button className="btn small danger" onClick={() => setDeleting(r)}>
                  Delete…
                </button>
              </div>
              {r.instructions && <div className="rule-ins">{r.instructions}</div>}
            </div>
          ))}
        </div>
      )}
      {editing && (
        <RuleEditor
          rule={toForm(editing)}
          kinds={ruleKinds}
          cameras={cameras.data ?? []}
          busy={busy}
          error={error}
          onCancel={() => {
            setEditing(null);
            setError("");
          }}
          onSave={async (f) => {
            const body = fromForm(f);
            const ok = await run(() => ("id" in editing ? v2.rules.update(editing.id, body) : v2.rules.create(body)));
            if (ok) setEditing(null);
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete "${deleting.name}"?`}
          confirmLabel="Delete rule"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await v2.rules.remove(deleting.id);
            setDeleting(null);
            await rules.reload();
          }}
        >
          <p>Matching alarms no longer get its instructions or push notifications.</p>
        </Confirm>
      )}
    </>
  );
}
