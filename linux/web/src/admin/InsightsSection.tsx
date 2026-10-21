// Sentinel Admin › Basics › Insights (WAVE14(C)): what happened over a range,
// from the activity the server already counts, with recommendations. No AI
// call. The arithmetic is in insights.ts; action buttons follow the
// permissions in /api/v2/me. The Operator hosts it too (WAVE15) with its own
// client, permissions and link handling.
import { useMemo, useState } from "react";
import { permissionsOf, type ApiClient, type Permission } from "../shared/api";
import { useLoad } from "../shared/hooks";
import { v2 } from "./client";
import {
  DEFAULT_RANGE,
  OTHER,
  RANGES,
  actionFor,
  alertChangeError,
  alertOnLabel,
  barLabel,
  dayLabel,
  formatShare,
  isEmpty,
  kindLabel,
  linkTarget,
  loadInsights,
  stackDaily,
  type ActionPlan,
  type DayBar,
  type Insights,
  type InsightsLink,
  type RangeId,
  type Recommendation,
} from "./insights";
import { Empty, ErrorBanner, Loading } from "./ui";

const levels: Record<string, { label: string; tone: string }> = {
  attention: { label: "Attention", tone: "amber" },
  suggestion: { label: "Suggestion", tone: "blue" },
  info: { label: "Info", tone: "ins-info" },
};

/**
 * How a host page runs the section. Every field is optional: left out, the
 * Admin's behaviour applies (cookie client, permissions from /api/v2/me, links
 * that follow the Admin's hash).
 */
export interface InsightsHost {
  /** The API client; the Operator passes its bearer-token client. */
  client?: ApiClient;
  /** The host's permission check, so the section needn't load /api/v2/me itself. */
  can?: (p: Permission) => boolean;
  /** Open a link in the host (a tab switch) instead of following the Admin hash. */
  onLink?: (target: InsightsLink) => void;
  /** Links the host can open; the others' buttons are hidden. Default: all. */
  opens?: (target: InsightsLink) => boolean;
}

export default function InsightsSection({ client = v2, can: hostCan, onLink, opens }: InsightsHost = {}) {
  const [rangeId, setRangeId] = useState<RangeId>(DEFAULT_RANGE);
  const insights = useLoad(() => loadInsights(rangeId, Date.now(), client), [rangeId, client]);
  // The same permissions source the Operator uses: Me.permissions, else the role's.
  const me = useLoad(() => (hostCan ? Promise.resolve(null) : client.me()), [client, !hostCan]);
  const meCan = useMemo(() => {
    const perms = new Set<Permission>(me.data ? (me.data.permissions?.length ? me.data.permissions : permissionsOf(me.data.role)) : []);
    return (p: Permission) => perms.has(p);
  }, [me.data]);
  const can = hostCan ?? meCan;
  const [done, setDone] = useState("");
  const data = insights.data;

  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Insights</h1>
          <div className="sub">What happened on your cameras, with suggestions. Days follow the management server's clock.</div>
        </div>
        <div className="seg" role="group" aria-label="Range">
          {RANGES.map((r) => (
            <button
              key={r.id}
              type="button"
              className={r.id === rangeId ? "on" : ""}
              aria-pressed={r.id === rangeId}
              onClick={() => {
                setRangeId(r.id);
                setDone("");
              }}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>
      <ErrorBanner error={insights.error} onRetry={insights.reload} />
      {done && (
        <div className="notice" role="status">
          {done}
        </div>
      )}
      {data === null ? (
        insights.error ? null : (
          <div className="card">
            <Loading />
          </div>
        )
      ) : isEmpty(data) ? (
        <div className="card">
          <Empty icon="◔" title="Not enough activity yet">
            Insights appear once there are alarms or events in the range.
          </Empty>
        </div>
      ) : (
        <div className={insights.loading ? "ins-stale" : undefined} aria-busy={insights.loading}>
          <InsightsView
            data={data}
            can={can}
            client={client}
            onLink={onLink}
            opens={opens}
            onChanged={(message) => {
              setDone(message);
              void insights.reload();
            }}
          />
        </div>
      )}
    </>
  );
}

type CardProps = { can: (p: Permission) => boolean; onChanged: (message: string) => void; client: ApiClient; onLink?: InsightsHost["onLink"]; opens?: InsightsHost["opens"] };

function InsightsView({ data, ...card }: { data: Insights } & CardProps) {
  const stack = useMemo(() => stackDaily(data.daily), [data.daily]);
  return (
    <>
      <div className="admin-stats ins-tiles">
        <Tile title="Alarms" value={data.totals.alarms} />
        <Tile title="Events" value={data.totals.events} />
        <Tile title="Cameras with alarms" value={data.totals.camerasWithAlarms} />
        <Tile title="Offline cameras" value={data.totals.offlineCameras} note="Had a Camera Offline alarm" />
      </div>

      <section className="card" aria-labelledby="ins-recs">
        <h2 id="ins-recs">Recommendations</h2>
        {data.recommendations.length === 0 ? (
          <p className="muted small ins-none">Nothing to suggest for this range.</p>
        ) : (
          <ul className="ins-recs">
            {data.recommendations.map((r) => (
              <RecommendationCard key={r.id} rec={r} {...card} />
            ))}
          </ul>
        )}
      </section>

      <section className="card" aria-labelledby="ins-daily">
        <h2 id="ins-daily">Alarms per day</h2>
        <DailyChart kinds={stack.kinds} bars={stack.bars} />
      </section>

      <div className="grid-2 ins-grid">
        <section className="card" aria-labelledby="ins-mix">
          <h2 id="ins-mix">Alarm mix</h2>
          {data.mix.length === 0 ? (
            <p className="muted small ins-none">No alarms in this range.</p>
          ) : (
            <ul className="ins-mix">
              {data.mix.map((m) => (
                <li key={m.kind}>
                  <span className="ins-mix-kind">
                    <span className={`ins-swatch ${tone(m.kind, stack.kinds)}`} aria-hidden="true" />
                    {kindLabel(m.kind)}
                  </span>
                  <span className="ins-share" aria-hidden="true">
                    <span className={tone(m.kind, stack.kinds)} style={{ width: `${Math.min(100, Math.max(0, m.share * 100))}%` }} />
                  </span>
                  <span className="mono">
                    {m.alarms.toLocaleString()} · {formatShare(m.share)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
        <section className="card table-wrap" aria-labelledby="ins-cams">
          <h2 id="ins-cams">Busiest cameras</h2>
          {data.cameras.length === 0 ? (
            <p className="muted small ins-none">No camera activity in this range.</p>
          ) : (
            <table className="list">
              <thead>
                <tr>
                  <th>Camera</th>
                  <th>Alarms</th>
                  <th>Most often</th>
                  <th>Raises alarms for</th>
                </tr>
              </thead>
              <tbody>
                {data.cameras.map((c) => (
                  <tr key={c.cameraId}>
                    <td>
                      <strong>{c.name}</strong>
                    </td>
                    <td>{c.alarms.toLocaleString()}</td>
                    <td>{c.topKind ? kindLabel(c.topKind) : "—"}</td>
                    <td>{alertOnLabel[c.alertOn] ?? c.alertOn}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </section>
      </div>
    </>
  );
}

function Tile({ title, value, note }: { title: string; value: number; note?: string }) {
  return (
    <div className="card">
      <div className="stat-title">{title}</div>
      <div className="stat-value">{value.toLocaleString()}</div>
      {note && <div className="muted small">{note}</div>}
    </div>
  );
}

/** Exported for the render tests. */
export function RecommendationCard({ rec, can, onChanged, client, onLink, opens }: { rec: Recommendation } & CardProps) {
  const plan = actionFor(rec, can);
  const link = plan?.kind === "link" ? linkTarget(rec) : null;
  const target = link && (opens?.(link) ?? true) ? link : null;
  const level = levels[rec.level] ?? levels.info;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function switchAlerts(p: Extract<ActionPlan, { kind: "setAlertOn" }>) {
    setBusy(true);
    setError("");
    try {
      await client.cameras.update(p.cameraId, { alertOn: p.alertOn });
      onChanged(`Saved. The camera now raises alarms for ${alertOnLabel[p.alertOn].toLowerCase()}.`);
    } catch (e) {
      setError(alertChangeError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <li className={`ins-rec ${rec.level}`}>
      <div className="ins-rec-head">
        <span className={`badge ${level.tone}`}>{level.label}</span>
        <strong>{rec.title}</strong>
      </div>
      <p>{rec.detail}</p>
      {plan?.kind === "link" &&
        target &&
        (onLink ? (
          <button type="button" className="btn small" onClick={() => onLink(target)}>
            {plan.label}
          </button>
        ) : (
          <a className="btn small" href={plan.href}>
            {plan.label}
          </a>
        ))}
      {plan?.kind === "setAlertOn" && (
        <button type="button" className="btn small" disabled={busy} onClick={() => void switchAlerts(plan)}>
          {busy ? "Switching…" : plan.label}
        </button>
      )}
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
    </li>
  );
}

/** Colour class for a kind: its slot among the chart's kinds, or Other's. */
const tone = (kind: string, kinds: string[]) => {
  const i = kinds.indexOf(kind);
  return i < 0 || kind === OTHER ? "ins-other" : `ins-k${i}`;
};

// The chart is drawn in a fixed box that stretches to the card's width; heights are real pixels.
const CHART_W = 600;
const CHART_H = 140;
const GAP = 2;

/** Alarms per day as stacked bars: one segment per kind, the rest as Other. */
function DailyChart({ kinds, bars }: { kinds: string[]; bars: DayBar[] }) {
  if (bars.every((b) => b.total === 0)) return <p className="muted small ins-none">No alarms in this range.</p>;
  const peak = Math.max(...bars.map((b) => b.total));
  const busiest = bars.find((b) => b.total === peak)!;
  const slot = CHART_W / bars.length;
  const barW = Math.min(slot * 0.7, 36);
  const first = dayLabel(bars[0].day);
  const last = dayLabel(bars[bars.length - 1].day);
  return (
    <>
      <p className="muted small ins-peak">
        Busiest: {dayLabel(busiest.day)}, {peak.toLocaleString()} alarm{peak === 1 ? "" : "s"}
      </p>
      <svg className="ins-chart" viewBox={`0 0 ${CHART_W} ${CHART_H}`} preserveAspectRatio="none" role="group" aria-label={`Alarms per day by kind, ${first} to ${last}`}>
        {bars.map((b, i) => {
          const label = barLabel(b);
          let y = CHART_H;
          return (
            <g key={b.day} role="img" aria-label={label}>
              <title>{label}</title>
              <rect className="ins-hit" x={i * slot} y={0} width={slot} height={CHART_H} />
              {b.segments.map((s, j) => {
                const h = (s.count / peak) * (CHART_H - GAP);
                y -= h;
                const gap = j > 0 ? Math.min(GAP, h / 2) : 0;
                return <rect key={s.kind} className={tone(s.kind, kinds)} x={i * slot + (slot - barW) / 2} y={y} width={barW} height={Math.max(h - gap, 0.5)} />;
              })}
            </g>
          );
        })}
        <line className="ins-base" x1={0} x2={CHART_W} y1={CHART_H - 0.5} y2={CHART_H - 0.5} />
      </svg>
      <div className="ins-axis" aria-hidden="true">
        <span>{first}</span>
        {bars.length > 1 && <span>{last}</span>}
      </div>
      <ul className="ins-legend" aria-label="Kinds">
        {kinds.map((k) => (
          <li key={k}>
            <span className={`ins-swatch ${tone(k, kinds)}`} aria-hidden="true" />
            {kindLabel(k)}
          </li>
        ))}
      </ul>
    </>
  );
}
