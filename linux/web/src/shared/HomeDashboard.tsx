// The Home dashboard (WAVE20-OPERATOR-LOOK.md rule 2, after the iPhone's
// HomeDashboard): the site and its connection, four stat tiles, then what
// needs attention (acknowledged in place) and recent activity on the left, the
// cameras and the Insights and People cards on the right. The numbers come
// from operator/homeModel.ts. WAVE21: moved out of operator/HomeView.tsx so
// the server dashboard shows it too; the client and the site's live state are
// injected (`source`), as the wave 17 sections take theirs.
import { useEffect, useMemo, useState } from "react";
import { loadInsights, type Insights } from "../admin/insights";
import { detectionApi, type Person } from "../detectionApi";
import { cameraCounts, cameraTiles, connectionLine, eventsToday, insightsLine, mergeEvent, needsAttention, peopleLine, recentActivity, statTiles, type TileTarget } from "../operator/homeModel";
import { errorMessage, type Alarm, type ApiClient, type Camera, type Event } from "./api";
import { AlarmCard, Chip, IconTile, PageHeader, SectionHeader, StatTile } from "./Cards";
import { Icon } from "./Icons";
import { kindGlyph, kindTone, tones } from "./alarmKind";
import type { StreamMessage, StreamState } from "./events";
import { Thumb } from "./Thumb";
import { dayBounds, fmtAgo } from "./time";

/** What the dashboard reads: the app's client and the site's live state, kept current by the app. */
export interface HomeSource {
  client: ApiClient;
  siteName: string;
  /** The site's address, for "Local network" or "Remote". */
  address: string;
  stream: StreamState;
  /** null while loading. */
  cameras: Camera[] | null;
  /** Open alarms. */
  alarms: Alarm[];
  /** Every stream message (event.new/updated keep the activity current; lagged re-reads it). */
  subscribe: (fn: (m: StreamMessage) => void) => () => void;
  /** Apply an alarm the user just changed. */
  putAlarm: (a: Alarm) => void;
  /** May acknowledge alarms. */
  mayAck: boolean;
}

export interface PlaybackAt {
  cameraId: string;
  at: number;
}

export interface HomeDashboardProps {
  source: HomeSource;
  /** Workspaces the user may open, for the links. */
  can: { alarms: boolean; insights: boolean; people: boolean };
  onOpenCamera: (cameraId: string) => void;
  onOpenLive: () => void;
  onOpenEvents: () => void;
  onOpenAlarms: (alarmId?: string) => void;
  onOpenInsights: () => void;
  onOpenPeople: () => void;
  onPlayback: (r: PlaybackAt) => void;
}

export default function HomeDashboard(p: HomeDashboardProps) {
  const data = p.source;
  const client = data.client;
  const [events, setEvents] = useState<Event[]>([]);
  const [facetTotal, setFacetTotal] = useState<number | null>(null);
  const [insights, setInsights] = useState<Insights | null>(null);
  const [people, setPeople] = useState<Person[] | null>(null);
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [error, setError] = useState("");
  const [now, setNow] = useState(() => Date.now());

  // The newest detections (activity and camera stills), kept current by the stream.
  const subscribe = data.subscribe;
  useEffect(() => {
    let alive = true;
    const load = () =>
      client.events
        .page({ limit: 100 })
        .then((page) => alive && setEvents(page.items))
        .catch(() => {});
    void load();
    const off = subscribe((m) => {
      if (m.type === "event.new" || m.type === "event.updated") setEvents((ev) => mergeEvent(ev, m.data));
      else if (m.type === "lagged") void load();
    });
    return () => {
      alive = false;
      off();
    };
  }, [client, subscribe]);

  // Today's total from the facets (the page above holds the newest 100 only); re-read as events arrive.
  const todayKey = dayBounds(now)[0];
  useEffect(() => {
    let alive = true;
    const t = setTimeout(() => {
      client.events
        .facets({ from: todayKey })
        .then((f) => alive && setFacetTotal(f.kinds.reduce((n, k) => n + k.count, 0)))
        .catch(() => {});
    }, 300);
    return () => {
      alive = false;
      clearTimeout(t);
    };
  }, [client, todayKey, events.length]);

  // Relative times and "today" move on.
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    if (!p.can.insights) return;
    let alive = true;
    loadInsights("7d", Date.now(), client)
      .then((i) => alive && setInsights(i))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [client, p.can.insights]);

  useEffect(() => {
    if (!p.can.people) return;
    let alive = true;
    detectionApi(client)
      .people.list()
      .then((list) => alive && setPeople(list))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [client, p.can.people]);

  const cameras = data.cameras ?? [];
  const counts = cameraCounts(cameras);
  const tiles = statTiles(counts, eventsToday(events, now, facetTotal), data.alarms);
  const attention = needsAttention(data.alarms);
  const recent = recentActivity(events);
  const grid = useMemo(() => cameraTiles(cameras, events), [cameras, events]);
  const line = connectionLine(data.stream, data.address);
  const byId = useMemo(() => new Map(cameras.map((c) => [c.id, c])), [cameras]);
  const name = (id?: string) => (id ? (byId.get(id)?.name ?? "Camera") : "Site");
  const mayAck = data.mayAck;

  const go = (t: TileTarget) => (t === "live" ? p.onOpenLive() : t === "events" ? p.onOpenEvents() : p.can.alarms ? p.onOpenAlarms() : undefined);

  async function acknowledge(a: Alarm) {
    setBusy((s) => new Set(s).add(a.id));
    setError("");
    try {
      data.putAlarm(await client.alarms.setState(a.id, "Acknowledged"));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy((s) => {
        const n = new Set(s);
        n.delete(a.id);
        return n;
      });
    }
  }

  return (
    <div className="op-home">
      <div className="op-home-inner">
        <PageHeader title={data.siteName} subtitle={line.text} dot={line.tone} />

        <div className="op-stats">
          {tiles.map((t) => (
            <StatTile key={t.id} title={t.title} icon={t.icon} value={t.value} note={t.note} tone={t.tone} onClick={t.target !== "alarms" || p.can.alarms ? () => go(t.target) : undefined} />
          ))}
        </div>

        <div className="op-home-cols">
          <div className="op-home-left">
            <section aria-label="Needs attention">
              <SectionHeader title="Needs attention" action={p.can.alarms ? "All alarms" : undefined} onAction={() => p.onOpenAlarms()} />
              {error && <div className="error">{error}</div>}
              {attention.shown.length === 0 ? (
                <div className="op-card op-all-clear">
                  <IconTile icon="shieldCheck" tone={tones.green} size={44} />
                  <div>
                    <strong>All clear</strong>
                    <div className="muted">No alarms need attention.</div>
                  </div>
                </div>
              ) : (
                <div className="op-card-list">
                  {attention.shown.map((a) => (
                    <AlarmCard
                      key={a.id}
                      alarm={a}
                      attention
                      actionsAlways
                      meta={`${a.cameraName || name(a.cameraId)} · ${fmtAgo(a.lastEventAt, now)}`}
                      onOpen={p.can.alarms ? () => p.onOpenAlarms(a.id) : undefined}
                      actions={
                        <>
                          {mayAck && (
                            <button className="btn primary grow" disabled={busy.has(a.id)} onClick={() => void acknowledge(a)}>
                              <Icon name="check" size={16} />
                              {busy.has(a.id) ? "Acknowledging…" : "Acknowledge"}
                            </button>
                          )}
                          {a.cameraId && (
                            <button className="btn op-icon-btn" title="Play the recording" aria-label="Play the recording" onClick={() => p.onPlayback({ cameraId: a.cameraId!, at: a.createdAt - 10_000 })}>
                              <Icon name="play" size={14} />
                            </button>
                          )}
                          {a.cameraId && (
                            <button className="btn op-icon-btn" title="Show the camera live" aria-label="Show the camera live" onClick={() => p.onOpenCamera(a.cameraId!)}>
                              <Icon name="video" size={16} />
                            </button>
                          )}
                        </>
                      }
                    />
                  ))}
                  {attention.more > 0 && p.can.alarms && (
                    <button className="op-card op-more-link" onClick={() => p.onOpenAlarms()}>
                      {attention.more} more need{attention.more === 1 ? "s" : ""} attention
                    </button>
                  )}
                </div>
              )}
            </section>

            <section aria-label="Recent activity">
              <SectionHeader title="Recent Activity" action="See all" onAction={p.onOpenEvents} />
              {recent.length === 0 ? (
                <div className="op-card op-pad-lg muted">AI detections will appear here.</div>
              ) : (
                <div className="op-card op-activity-list">
                  {recent.map((e) => (
                    <button key={e.id} className="op-activity-row" onClick={() => p.onPlayback({ cameraId: e.cameraId, at: e.createdAt - 5000 })} title="Play the recording">
                      <span className="op-activity-thumb">
                        {e.hasThumbnail ? (
                          <Thumb client={client} event={e} />
                        ) : (
                          <span className="op-still-ph" style={{ color: kindTone(e.kind) }}>
                            <Icon name={kindGlyph(e.kind)} size={18} />
                          </span>
                        )}
                      </span>
                      <span className="op-activity-body">
                        <span className="op-activity-top">
                          <span className="op-activity-kind" style={{ color: kindTone(e.kind) }}>
                            <Icon name={kindGlyph(e.kind)} size={13} />
                          </span>
                          <strong>{e.label && e.label !== e.kind ? `${e.kind} · ${e.label}` : e.kind}</strong>
                          <span className="grow" />
                          <span className="op-activity-time">{fmtAgo(e.createdAt, now)}</span>
                        </span>
                        <span className="op-activity-cam">{name(e.cameraId)}</span>
                        {e.description && <span className="op-activity-desc">{e.description}</span>}
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </section>
          </div>

          <div className="op-home-right">
            <section aria-label="Cameras">
              <SectionHeader title="Cameras" action="Live view" onAction={p.onOpenLive} />
              {grid.tiles.length === 0 ? (
                <div className="op-card op-pad-lg muted">{data.cameras ? "No cameras yet." : "Loading cameras…"}</div>
              ) : (
                <div className="op-cam-tiles">
                  {grid.tiles.map((t) => (
                    <button key={t.camera.id} className="op-cam-tile" onClick={() => p.onOpenCamera(t.camera.id)} title={`Show ${t.camera.name} live`}>
                      <span className="op-cam-still">
                        {t.still ? (
                          <Thumb client={client} event={t.still} />
                        ) : (
                          <span className="op-still-ph">
                            <Icon name={t.online ? "video" : "videoOff"} size={22} />
                          </span>
                        )}
                        <span className="op-cam-badges">
                          {t.rec ? (
                            <span className="op-rec">
                              <span className="op-rec-dot" />
                              REC
                            </span>
                          ) : (
                            <span />
                          )}
                          <Chip tone={t.online ? tones.green : tones.red} className="outlined">
                            {t.online ? "Online" : "Offline"}
                          </Chip>
                        </span>
                      </span>
                      <span className="op-cam-foot">
                        <strong>{t.camera.name}</strong>
                        {t.place && (
                          <span className="op-cam-place">
                            <Icon name="pin" size={12} />
                            {t.place}
                          </span>
                        )}
                      </span>
                    </button>
                  ))}
                </div>
              )}
              {grid.more > 0 && (
                <button className="op-card op-more-link" onClick={p.onOpenLive}>
                  {grid.more} more camera{grid.more === 1 ? "" : "s"} in Live
                </button>
              )}
            </section>

            {(p.can.insights || p.can.people) && (
              <div className="op-link-cards">
                {p.can.insights && (
                  <button className="op-card op-link-card" onClick={p.onOpenInsights}>
                    <span className="op-link-card-top">
                      <Icon name="insights" size={20} />
                      <Icon name="chevron" size={14} className="op-chev" />
                    </span>
                    <strong>Insights</strong>
                    <span className="muted small">{insightsLine(insights?.totals ?? null)}</span>
                  </button>
                )}
                {p.can.people && (
                  <button className="op-card op-link-card" onClick={p.onOpenPeople}>
                    <span className="op-link-card-top">
                      <Icon name="people" size={20} />
                      <Icon name="chevron" size={14} className="op-chev" />
                    </span>
                    <strong>People</strong>
                    <span className="muted small">{peopleLine(people)}</span>
                  </button>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
