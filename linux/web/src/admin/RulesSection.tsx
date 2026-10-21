import { useEffect, useRef, useState } from "react";
import { errorMessage, ruleInputOf, type ApiClient, type Rule, type RuleInput } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { RuleEditor } from "../pages/RulesPage";
import { dayNames, draftRule, groupTemplates, listRuleTemplates, minuteToTime, ruleKinds, type AlarmRule, type Delivery } from "../rulesApi";
import { v2 } from "./client";
import { Confirm, Empty, ErrorBanner, Loading } from "./ui";
import "../pages/rules.css";

const sevClass: Record<string, string> = { Critical: "red", Warning: "amber", Info: "blue" };

/** Where a new rule in the editor came from (WAVE14(C)): a draft brings its explanation and warnings. */
type Prefill = { kind: "draft"; explanation: string; warnings: string[] } | { kind: "template" };

const allCamerasNote = "Applies to every camera. Choose cameras to narrow it.";

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

/** The Admin drops ?template=<id> from its hash once the template is open, so a reload doesn't reopen it. */
const dropTemplateParam = () => history.replaceState(null, "", window.location.hash.split("?")[0]);

export interface RulesHost {
  /** A template to open in the editor: the Admin's deep link (#/admin/rules-and-events/rules?template=<id>, from Insights), or the Operator's. */
  templateId?: string;
  /** The API client; the Admin's cookie client by default, the Operator passes its bearer-token client (WAVE15). */
  client?: ApiClient;
  /** Called once the linked template is open; the default clears the Admin hash's parameter. */
  onTemplateOpened?: () => void;
}

export default function RulesSection({ templateId, client = v2, onTemplateOpened = dropTemplateParam }: RulesHost) {
  const rules = useLoad(() => client.rules.list(), [client]);
  const cameras = useLoad(() => client.cameras.list(), [client]);
  // Wave 6: time profiles and webhooks the rule editor can pick (both lists need ChangeSettings, which this admin page already requires).
  const profiles = useLoad(async () => (await client.call("get", "/api/v2/time-profiles")).items, [client]);
  const hooks = useLoad(async () => (await client.call("get", "/api/v2/webhooks")).items, [client]);
  // Wave 14: ready-made rules to start from.
  const templates = useLoad(() => listRuleTemplates(client), [client]);
  const [editing, setEditingRule] = useState<Rule | RuleInput | null>(null);
  const [extra, setExtra] = useState<{ timeProfileId: string; webhookIds: string[] }>({ timeProfileId: "", webhookIds: [] });
  const [prefill, setPrefill] = useState<Prefill | null>(null);
  const [editorKey, setEditorKey] = useState(0);
  const setEditing = (r: Rule | RuleInput | null, from: Prefill | null = null) => {
    setEditingRule(r);
    setExtra({ timeProfileId: r?.timeProfileId ?? "", webhookIds: (r?.actions ?? []).filter((a) => a.type === "webhook").map((a) => a.webhookId) });
    setPrefill(from);
    setEditorKey((k) => k + 1);
  };
  const [templatesOpen, setTemplatesOpen] = useState<boolean | null>(null);
  const [describe, setDescribe] = useState("");
  const [drafting, setDrafting] = useState(false);
  const [draftError, setDraftError] = useState<unknown>(null);

  async function draft() {
    setDrafting(true);
    setDraftError(null);
    try {
      // The draft comes back off and unsaved; the editor's Create rule is what saves it.
      const d = await draftRule(describe, client);
      setEditing(d.rule, { kind: "draft", explanation: d.explanation, warnings: d.warnings ?? [] });
    } catch (e) {
      setDraftError(e);
    } finally {
      setDrafting(false);
    }
  }

  // Insights' "Use template" lands here with a template id: open it once, then tell the host so it can drop the link.
  const linked = useRef("");
  useEffect(() => {
    // Cleared by the host: the same template may be linked again.
    if (!templateId) linked.current = "";
    const t = templateId ? templates.data?.find((x) => x.id === templateId) : undefined;
    if (!t || linked.current === t.id) return;
    linked.current = t.id;
    setEditing(t.rule, { kind: "template" });
    onTemplateOpened();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templateId, templates.data]);
  const profileName = (id: string) => profiles.data?.find((p) => p.id === id)?.name ?? "Removed profile";
  const hookName = (id: string) => hooks.data?.find((h) => h.id === id)?.name ?? "Removed webhook";
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
      <form
        className="card rule-draft"
        onSubmit={(e) => {
          e.preventDefault();
          void draft();
        }}
      >
        <h2>Describe a rule</h2>
        <label className="field">
          <span>What should raise an alarm?</span>
          <textarea
            rows={2}
            maxLength={500}
            value={describe}
            disabled={drafting}
            placeholder="e.g. Tell me about people at the loading dock after 6pm"
            onChange={(e) => setDescribe(e.target.value)}
          />
          <small>Sentinel drafts a rule for you to check. Nothing is saved until you create it. Uses one AI request.</small>
        </label>
        <ErrorBanner error={draftError} />
        <button className="btn primary" disabled={drafting || describe.trim().length < 3}>
          {drafting && <span className="spinner" aria-hidden="true" />}
          {drafting ? "Drafting…" : "Draft rule"}
        </button>
      </form>
      <details
        className="card rule-templates"
        open={templatesOpen ?? (rules.data !== null && rules.data.length === 0)}
        onToggle={(e) => setTemplatesOpen(e.currentTarget.open)}
      >
        <summary>
          <h2>Start from a template</h2>
        </summary>
        <ErrorBanner error={templates.error} onRetry={templates.reload} />
        {templates.data === null
          ? !templates.error && <Loading />
          : groupTemplates(templates.data).map((g) => (
              <section key={g.id} className="template-group">
                <h3>{g.label}</h3>
                <div className="template-grid">
                  {g.items.map((t) => (
                    <div key={t.id} className="template-card">
                      <strong>{t.title}</strong>
                      <p className="muted small">{t.summary}</p>
                      <div className="rule-pills">
                        <span className="badge blue">{t.rule.kinds.length ? t.rule.kinds.join(", ") : "Any alarm"}</span>
                        <span className={`badge ${sevClass[t.rule.minSeverity] ?? "blue"}`}>{t.rule.minSeverity === "Info" ? "Any severity" : `${t.rule.minSeverity}+`}</span>
                        {t.rule.schedule && (
                          <span className="badge blue">
                            {minuteToTime(t.rule.fromMinute)}–{minuteToTime(t.rule.toMinute)}
                          </span>
                        )}
                        <span className={`badge ${t.rule.delivery === "push" ? "amber" : "blue"}`}>{t.rule.delivery === "push" ? "Operators + phone push" : "Operators only"}</span>
                      </div>
                      <button type="button" className="btn small" onClick={() => setEditing(t.rule, { kind: "template" })}>
                        Use template
                      </button>
                    </div>
                  ))}
                </div>
              </section>
            ))}
      </details>
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
                    onChange={() => run(() => client.rules.update(r.id, { ...ruleInputOf(r), enabled: !r.enabled }))}
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
                {r.timeProfileId && <span className="badge blue">Profile: {profileName(r.timeProfileId)}</span>}
                {(r.actions ?? []).map((a, i) => (
                  <span key={i} className="badge amber">
                    Webhook: {hookName(a.webhookId)}
                  </span>
                ))}
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
          key={editorKey}
          rule={toForm(editing)}
          kinds={ruleKinds}
          cameras={cameras.data ?? []}
          busy={busy}
          error={error}
          intro={
            prefill?.kind === "draft" && (
              <div className="rule-draft-notes">
                {prefill.explanation && <p>{prefill.explanation}</p>}
                {prefill.warnings.length > 0 && (
                  <ul className="rule-draft-warnings">
                    {prefill.warnings.map((w, i) => (
                      <li key={i}>{w}</li>
                    ))}
                  </ul>
                )}
                <small className="hint">Check the draft, then create the rule. Drafts start off: tick "Rule is on" to use it.</small>
              </div>
            )
          }
          allCamerasNote={prefill ? allCamerasNote : undefined}
          extra={
            <>
              <label className="field">
                <span>Only during time profile</span>
                <select value={extra.timeProfileId} onChange={(e) => setExtra({ ...extra, timeProfileId: e.target.value })}>
                  <option value="">Always (no profile)</option>
                  {(profiles.data ?? []).map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                    </option>
                  ))}
                </select>
                <small>Outside the profile the rule doesn't fire. Manage profiles under Time profiles.</small>
              </label>
              <div className="field">
                <span className="field-label">Call webhooks when it fires</span>
                <div className="chip-grid">
                  {(hooks.data ?? []).length === 0 && <span className="muted small">No webhooks yet. Add one under Webhooks.</span>}
                  {(hooks.data ?? []).map((h) => (
                    <label key={h.id} className={`chip ${extra.webhookIds.includes(h.id) ? "on" : ""}`}>
                      <input
                        type="checkbox"
                        checked={extra.webhookIds.includes(h.id)}
                        onChange={() =>
                          setExtra({ ...extra, webhookIds: extra.webhookIds.includes(h.id) ? extra.webhookIds.filter((x) => x !== h.id) : [...extra.webhookIds, h.id] })
                        }
                      />
                      {h.name}
                    </label>
                  ))}
                </div>
              </div>
            </>
          }
          onCancel={() => {
            setEditing(null);
            setError("");
          }}
          onSave={async (f) => {
            const body: RuleInput = {
              ...fromForm(f),
              ...(extra.timeProfileId ? { timeProfileId: extra.timeProfileId } : {}),
              ...(extra.webhookIds.length ? { actions: extra.webhookIds.map((webhookId) => ({ type: "webhook" as const, webhookId })) } : {}),
            };
            const ok = await run(() => ("id" in editing ? client.rules.update(editing.id, body) : client.rules.create(body)));
            if (!ok) return;
            if (prefill?.kind === "draft") setDescribe("");
            setEditing(null);
          }}
        />
      )}
      {deleting && (
        <Confirm
          title={`Delete "${deleting.name}"?`}
          confirmLabel="Delete rule"
          onCancel={() => setDeleting(null)}
          run={async () => {
            await client.rules.remove(deleting.id);
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
