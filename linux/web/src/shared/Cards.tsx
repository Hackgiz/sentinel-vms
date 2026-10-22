// WAVE20: the iPhone's building blocks for the operator (WAVE20-OPERATOR-LOOK.md):
// page header, section header, chips, stat tiles, the icon tile, and the
// alarm and event cards. Colours are CSS colours from alarmKind.ts; the
// styles live in look.css (op-page-head, op-chip, op-stat, op-card…), shared by
// the operator and (WAVE21) the server dashboard.
import type { CSSProperties, KeyboardEvent, ReactNode } from "react";
import { alarmGlyph, alarmTone, kindGlyph, kindTone, severityTone, stateTone } from "./alarmKind";
import { Icon } from "./Icons";

const toneStyle = (tone: string): CSSProperties => ({ "--tone": tone }) as CSSProperties;

/** A workspace's title, a one-line subtitle (count or state) and its controls on the right. */
export function PageHeader({ title, subtitle, dot, children, className }: { title: ReactNode; subtitle?: ReactNode; /** A status dot before the subtitle. */ dot?: string; children?: ReactNode; className?: string }) {
  return (
    <header className={`op-page-head ${className ?? ""}`}>
      <div className="op-page-titles">
        <h1>{title}</h1>
        {subtitle !== undefined && subtitle !== "" && (
          <div className="op-page-sub">
            {dot && <span className="op-dot" style={toneStyle(dot)} aria-hidden="true" />}
            {subtitle}
          </div>
        )}
      </div>
      {children && <div className="op-page-tools">{children}</div>}
    </header>
  );
}

/** A section title with an optional link-style action on the right ("All alarms"). */
export function SectionHeader({ title, action, onAction }: { title: string; action?: string; onAction?: () => void }) {
  return (
    <div className="op-section-head">
      <h2>{title}</h2>
      {action && onAction && (
        <button type="button" className="op-link" onClick={onAction}>
          {action}
        </button>
      )}
    </div>
  );
}

/** A soft pill in a tone (the iPhone's Chip). */
export function Chip({ tone, icon, filled, children, title, className }: { tone: string; icon?: string; filled?: boolean; children: ReactNode; title?: string; className?: string }) {
  return (
    <span className={`op-chip ${filled ? "filled" : ""} ${className ?? ""}`} style={toneStyle(tone)} title={title}>
      {icon && <Icon name={icon} size={12} />}
      {children}
    </span>
  );
}

/** A detection kind as a chip in the iPhone's kind colour. */
export const KindChip = ({ kind, upper }: { kind: string; upper?: boolean }) => (
  <Chip tone={kindTone(kind)} icon={kindGlyph(kind)}>
    {upper ? kind.toUpperCase() : kind}
  </Chip>
);

/** Severity, uppercase, as on the iPhone's alarm cards. */
export const SeverityBadge = ({ severity }: { severity: string }) => (
  <Chip tone={severityTone(severity)} className="op-sev">
    {severity.toUpperCase()}
  </Chip>
);

/** A rounded square holding a glyph, tinted. */
export const IconTile = ({ icon, tone, size = 36 }: { icon: string; tone: string; size?: number }) => (
  <span className="op-icon-tile" style={{ ...toneStyle(tone), width: size, height: size }} aria-hidden="true">
    <Icon name={icon} size={Math.round(size * 0.5)} />
  </span>
);

/** One number on a dashboard: what it counts, the value, a toned note. */
export function StatTile({ title, icon, value, note, tone, onClick }: { title: string; icon: string; value: string; note: string; tone: string; onClick?: () => void }) {
  return (
    <button type="button" className="op-stat" style={toneStyle(tone)} onClick={onClick} disabled={!onClick} aria-label={`${title}: ${value}, ${note}`}>
      <span className="op-stat-title">
        <Icon name={icon} size={14} />
        {title}
      </span>
      <span className="op-stat-value">{value}</span>
      <span className="op-stat-note">{note}</span>
    </button>
  );
}

/** A plain rounded panel. */
export const Card = ({ children, className, ...rest }: { children: ReactNode; className?: string } & React.HTMLAttributes<HTMLDivElement>) => (
  <div className={`op-card ${className ?? ""}`} {...rest}>
    {children}
  </div>
);

export interface AlarmCardAlarm {
  id: string;
  title: string;
  kind?: string;
  severity: string;
  state: string;
  detail?: string;
}

export interface AlarmCardProps {
  alarm: AlarmCardAlarm;
  /** Under the title: camera · relative time (and anything else worth one line). */
  meta: ReactNode;
  selected?: boolean;
  /** The action row: shown on the selected or hovered card, or always. */
  actions?: ReactNode;
  actionsAlways?: boolean;
  /** Before the icon tile (a selection checkbox). */
  leading?: ReactNode;
  onOpen?: () => void;
  /** The left stripe and border tint: on alarms that need attention. */
  attention?: boolean;
  /** WAVE21: more about the alarm, between the main row and the actions (instructions, evidence…). */
  children?: ReactNode;
}

/**
 * An alarm as the iPhone's dense AlarmCard: a severity stripe on the left, the
 * kind's icon tile, title and severity on one line, camera · time and the
 * state under it, one line of detail, then the action row.
 */
export function AlarmCard({ alarm, meta, selected, actions, actionsAlways, leading, onOpen, attention, children }: AlarmCardProps) {
  const tone = alarmTone(alarm);
  const key = (e: KeyboardEvent) => {
    if (e.target !== e.currentTarget) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onOpen?.();
    }
  };
  return (
    <div className={`op-card op-alarm-card ${selected ? "selected" : ""} ${attention ? "attention" : ""}`} style={toneStyle(tone)} aria-selected={onOpen ? !!selected : undefined}>
      <div className="op-card-main" role={onOpen ? "button" : undefined} tabIndex={onOpen ? 0 : undefined} onClick={onOpen} onKeyDown={onOpen ? key : undefined}>
        {leading}
        <IconTile icon={alarmGlyph(alarm.kind, alarm.severity)} tone={tone} />
        <div className="op-card-body">
          <div className="op-card-title">
            <strong>{alarm.title}</strong>
            <SeverityBadge severity={alarm.severity} />
          </div>
          <div className="op-card-meta">
            <span className="op-card-meta-text">{meta}</span>
            <span className="op-card-state" style={{ color: stateTone(alarm.state) }}>
              {alarm.state}
            </span>
          </div>
          {alarm.detail && <div className="op-card-detail">{alarm.detail}</div>}
        </div>
      </div>
      {children}
      {actions && <div className={`op-card-actions ${actionsAlways ? "always" : ""}`}>{actions}</div>}
    </div>
  );
}

/**
 * A detection as the iPhone's event card: the snapshot with the kind chip
 * and the time over it, then the camera, the chips and one line of description.
 */
export function EventCard({ media, kind, time, title, badges, meta, extra, description, onOpen, openTitle, children, className }: { media: ReactNode; kind: string; time: string; title: string; badges?: ReactNode; meta?: ReactNode; /** Under the meta line (a score bar). */ extra?: ReactNode; description?: string; onOpen: () => void; openTitle?: string; /** Over the card, outside the open button (Find similar…). */ children?: ReactNode; className?: string }) {
  return (
    <div className={`op-card op-event-card ${className ?? ""}`} style={toneStyle(kindTone(kind))}>
      <button type="button" className="op-event-open" onClick={onOpen} title={openTitle}>
        <span className="op-event-media">
          {media}
          <span className="op-event-over">
            <KindChip kind={kind} upper />
            <span className="op-event-time">{time}</span>
          </span>
        </span>
        <span className="op-event-body">
          <span className="op-card-title">
            <strong>{title}</strong>
            {badges}
          </span>
          {meta && <span className="op-card-meta">{meta}</span>}
          {extra}
          {description && <span className="op-card-detail">{description}</span>}
        </span>
      </button>
      {children}
    </div>
  );
}
