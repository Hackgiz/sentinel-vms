package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Alarm mirrors the Mac's AlertEvent (same kind/state/severity strings) so the
// iPhone app shows Linux alarms exactly like Mac ones.
type Alarm struct {
	ID          string   `json:"id"`
	CameraID    string   `json:"cameraID,omitempty"`
	CameraName  string   `json:"cameraName,omitempty"`
	Kind        string   `json:"kind"`     // Motion, Person, Camera Offline, …
	Title       string   `json:"title"`    // "Motion detected"
	Detail      string   `json:"detail"`   //
	Severity    string   `json:"severity"` // Critical | Warning | Info
	State       string   `json:"state"`    // New | Acknowledged | Investigating | Snoozed | Resolved | False Alarm
	Owner       string   `json:"owner"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
	LastEventAt int64    `json:"lastEventAt"`
	EventCount  int      `json:"eventCount"`
	EventID     string   `json:"eventID,omitempty"`
	ResponseLog []string `json:"responseLog"`
}

var AlarmStates = []string{"New", "Acknowledged", "Investigating", "Snoozed", "Resolved", "False Alarm"}

func IsOpenState(s string) bool { return s != "Resolved" && s != "False Alarm" }

func ValidAlarmState(s string) bool {
	for _, v := range AlarmStates {
		if v == s {
			return true
		}
	}
	return false
}

const alarmCols = `id, camera_id, camera_name, kind, title, detail, severity, state, owner, created_at, updated_at, last_event_at, event_count, event_id, response_log`

func scanAlarm(row interface{ Scan(...any) error }) (*Alarm, error) {
	a := &Alarm{}
	var log string
	if err := row.Scan(&a.ID, &a.CameraID, &a.CameraName, &a.Kind, &a.Title, &a.Detail, &a.Severity, &a.State, &a.Owner,
		&a.CreatedAt, &a.UpdatedAt, &a.LastEventAt, &a.EventCount, &a.EventID, &log); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(log), &a.ResponseLog)
	if a.ResponseLog == nil {
		a.ResponseLog = []string{}
	}
	return a, nil
}

func (s *Store) Alarm(ctx context.Context, id string) (*Alarm, error) {
	return scanAlarm(s.DB.QueryRowContext(ctx, `SELECT `+alarmCols+` FROM alarms WHERE id = ?`, id))
}

// Alarms lists alarms, newest activity first. openOnly drops Resolved/False Alarm.
func (s *Store) Alarms(ctx context.Context, openOnly bool, limit int) ([]Alarm, error) {
	q := `SELECT ` + alarmCols + ` FROM alarms`
	if openOnly {
		q += ` WHERE state NOT IN ('Resolved', 'False Alarm')`
	}
	q += ` ORDER BY last_event_at DESC LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alarm{}
	for rows.Next() {
		a, err := scanAlarm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// AlarmInput describes one detection or health event to raise.
type AlarmInput struct {
	CameraID, CameraName  string
	Kind, Title, Severity string
	Detail                string
	EventID               string
	At                    time.Time
	MergeWindow           time.Duration // fold into an open alarm whose last event is this recent; 0 = any open alarm
	MergeSince            time.Time     // if set, overrides MergeWindow: fold into an alarm whose last event is at/after this
	Origin                string        // first response-log line
}

// RaiseAlarm folds the event into an open alarm of the same camera and kind
// (Mac rule: detections within 60 s group together) or opens a new one.
// Returns the alarm and whether it is new.
func (s *Store) RaiseAlarm(ctx context.Context, in AlarmInput) (*Alarm, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	at := in.At.Unix()
	q := `SELECT ` + alarmCols + ` FROM alarms WHERE camera_id = ? AND kind = ? AND state NOT IN ('Resolved', 'False Alarm')`
	args := []any{in.CameraID, in.Kind}
	switch {
	case !in.MergeSince.IsZero():
		q += ` AND last_event_at >= ?`
		args = append(args, in.MergeSince.Unix())
	case in.MergeWindow > 0:
		q += ` AND last_event_at >= ?`
		args = append(args, at-int64(in.MergeWindow.Seconds()))
	}
	q += ` ORDER BY last_event_at DESC LIMIT 1`
	existing, err := scanAlarm(tx.QueryRowContext(ctx, q, args...))
	switch {
	case err == nil:
		if existing.State == "Snoozed" {
			return existing, false, tx.Commit() // snoozed: swallow, like the Mac
		}
		existing.EventCount++
		existing.LastEventAt, existing.UpdatedAt = at, now()
		existing.Detail = strings.TrimSpace(in.Detail) + fmt.Sprintf(" Grouped with %d related events.", existing.EventCount)
		if in.EventID != "" {
			existing.EventID = in.EventID
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alarms SET event_count=?, last_event_at=?, updated_at=?, detail=?, event_id=? WHERE id=?`,
			existing.EventCount, existing.LastEventAt, existing.UpdatedAt, existing.Detail, existing.EventID, existing.ID); err != nil {
			return nil, false, err
		}
		return existing, false, tx.Commit()
	case !errors.Is(err, ErrNotFound):
		return nil, false, err
	}
	a := &Alarm{ID: NewID(), CameraID: in.CameraID, CameraName: in.CameraName, Kind: in.Kind, Title: in.Title, Detail: in.Detail,
		Severity: in.Severity, State: "New", Owner: "Unassigned", CreatedAt: at, UpdatedAt: now(), LastEventAt: at, EventCount: 1,
		EventID: in.EventID, ResponseLog: []string{in.Origin}}
	log, _ := json.Marshal(a.ResponseLog)
	if _, err := tx.ExecContext(ctx, `INSERT INTO alarms(`+alarmCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.CameraID, a.CameraName, a.Kind, a.Title, a.Detail, a.Severity, a.State, a.Owner, a.CreatedAt, a.UpdatedAt, a.LastEventAt, a.EventCount, a.EventID, string(log)); err != nil {
		return nil, false, err
	}
	return a, true, tx.Commit()
}

// SetAlarmState moves an alarm and appends a response-log line.
func (s *Store) SetAlarmState(ctx context.Context, id, state, owner, note string) (*Alarm, error) {
	a, err := s.Alarm(ctx, id)
	if err != nil {
		return nil, err
	}
	a.State, a.UpdatedAt = state, now()
	if owner != "" {
		a.Owner = owner
	}
	if note != "" {
		a.ResponseLog = append(a.ResponseLog, time.Now().Format("Jan 2 15:04")+" — "+note)
	}
	log, _ := json.Marshal(a.ResponseLog)
	_, err = s.DB.ExecContext(ctx, `UPDATE alarms SET state=?, owner=?, updated_at=?, response_log=? WHERE id=?`, a.State, a.Owner, a.UpdatedAt, string(log), id)
	return a, err
}

// AddAlarmNote appends to the response log without changing state.
func (s *Store) AddAlarmNote(ctx context.Context, id, note string) error {
	a, err := s.Alarm(ctx, id)
	if err != nil {
		return err
	}
	a.ResponseLog = append(a.ResponseLog, time.Now().Format("Jan 2 15:04")+" — "+note)
	log, _ := json.Marshal(a.ResponseLog)
	_, err = s.DB.ExecContext(ctx, `UPDATE alarms SET response_log=?, updated_at=? WHERE id=?`, string(log), now(), id)
	return err
}

// MARK: - Detection events

type Event struct {
	ID          string  `json:"id"`
	CameraID    string  `json:"cameraID"`
	Kind        string  `json:"kind"` // Motion | Person | Vehicle | Animal
	Label       string  `json:"label,omitempty"`
	Score       float64 `json:"score"`
	Box         string  `json:"box,omitempty"` // JSON [x1,y1,x2,y2], normalized
	CreatedAt   int64   `json:"createdAt"`
	Thumbnail   string  `json:"-"`
	HasThumb    bool    `json:"hasThumbnail"`
	AlarmID     string  `json:"alarmID,omitempty"`
	Description string  `json:"description,omitempty"` // AI scene description
	Threat      string  `json:"threat,omitempty"`      // none | low | elevated | high
	Anomaly     bool    `json:"anomaly,omitempty"`
	Reason      string  `json:"reason,omitempty"`
	Tags        string  `json:"tags,omitempty"` // comma-separated
}

func scanEvent(row interface{ Scan(...any) error }) (*Event, error) {
	e := &Event{}
	var anomaly int
	if err := row.Scan(&e.ID, &e.CameraID, &e.Kind, &e.Score, &e.CreatedAt, &e.Thumbnail, &e.AlarmID,
		&e.Label, &e.Box, &e.Description, &e.Threat, &anomaly, &e.Reason, &e.Tags); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	e.HasThumb = e.Thumbnail != ""
	e.Anomaly = anomaly == 1
	return e, nil
}

const eventCols = `id, camera_id, kind, score, created_at, thumbnail, alarm_id, label, box, description, threat, anomaly, reason, tags`

func (s *Store) AddEvent(ctx context.Context, e *Event) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events(`+eventCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.CameraID, e.Kind, e.Score, e.CreatedAt, e.Thumbnail, e.AlarmID, e.Label, e.Box, e.Description, e.Threat, boolInt(e.Anomaly), e.Reason, e.Tags)
	return err
}

// SetEventAnalysis stores the AI scene analysis for an event.
func (s *Store) SetEventAnalysis(ctx context.Context, id, description, threat string, anomaly bool, reason, tags string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE events SET description=?, threat=?, anomaly=?, reason=?, tags=? WHERE id=?`,
		description, threat, boolInt(anomaly), reason, tags, id)
	return err
}

// EventsBetween lists events in [from, to), oldest first (for AI digests/search).
func (s *Store) EventsBetween(ctx context.Context, from, to time.Time, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE created_at >= ? AND created_at < ? ORDER BY created_at LIMIT ?`,
		from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// UpdateAlarmDetail replaces an alarm's detail (e.g. with the AI description).
func (s *Store) UpdateAlarmDetail(ctx context.Context, id, detail, severity string) error {
	q, args := `UPDATE alarms SET detail=?, updated_at=?`, []any{detail, now()}
	if severity != "" {
		q += `, severity=?`
		args = append(args, severity)
	}
	_, err := s.DB.ExecContext(ctx, q+` WHERE id=?`, append(args, id)...)
	return err
}

func (s *Store) SetEventThumbnail(ctx context.Context, id, file string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE events SET thumbnail=? WHERE id=?`, file, id)
	return err
}

func (s *Store) SetEventAlarm(ctx context.Context, id, alarmID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE events SET alarm_id=? WHERE id=?`, alarmID, id)
	return err
}

func (s *Store) Event(ctx context.Context, id string) (*Event, error) {
	return scanEvent(s.DB.QueryRowContext(ctx, `SELECT `+eventCols+` FROM events WHERE id = ?`, id))
}

// Events lists recent events, newest first; cameraID "" = all cameras.
func (s *Store) Events(ctx context.Context, cameraID string, limit int) ([]Event, error) {
	q, args := `SELECT `+eventCols+` FROM events`, []any{}
	if cameraID != "" {
		q += ` WHERE camera_id = ?`
		args = append(args, cameraID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *Store) EventCount(ctx context.Context, cameraID string) int {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE camera_id = ?`, cameraID).Scan(&n)
	return n
}

// PruneEvents deletes events older than cutoff and returns their thumbnail files.
func (s *Store) PruneEvents(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT thumbnail FROM events WHERE created_at < ? AND thumbnail != ''`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	var files []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			files = append(files, f)
		}
	}
	rows.Close()
	_, err = s.DB.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, cutoff.Unix())
	return files, err
}

// MARK: - Evidence

type Evidence struct {
	ID         string `json:"id"`
	CaseID     string `json:"caseID"`
	AlarmID    string `json:"alertID,omitempty"`
	CameraID   string `json:"cameraID"`
	CameraName string `json:"camera"`
	Title      string `json:"title"`
	Range      string `json:"range"`
	File       string `json:"-"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	LockedAt   int64  `json:"lockedAt"`
	LockedBy   string `json:"lockedBy"`
}

const evidenceCols = `id, case_id, alarm_id, camera_id, camera_name, title, range_label, file, size, sha256, locked_at, locked_by`

func scanEvidence(row interface{ Scan(...any) error }) (*Evidence, error) {
	e := &Evidence{}
	if err := row.Scan(&e.ID, &e.CaseID, &e.AlarmID, &e.CameraID, &e.CameraName, &e.Title, &e.Range, &e.File, &e.Size, &e.SHA256, &e.LockedAt, &e.LockedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

func (s *Store) AddEvidence(ctx context.Context, e *Evidence) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO evidence(`+evidenceCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.CaseID, e.AlarmID, e.CameraID, e.CameraName, e.Title, e.Range, e.File, e.Size, e.SHA256, e.LockedAt, e.LockedBy)
	return err
}

func (s *Store) EvidenceForAlarm(ctx context.Context, alarmID string) (*Evidence, error) {
	return scanEvidence(s.DB.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE alarm_id = ? ORDER BY locked_at LIMIT 1`, alarmID))
}

func (s *Store) EvidenceByID(ctx context.Context, id string) (*Evidence, error) {
	return scanEvidence(s.DB.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = ?`, id))
}

func (s *Store) EvidenceList(ctx context.Context) ([]Evidence, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+evidenceCols+` FROM evidence ORDER BY locked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Evidence{}
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// NextCaseID returns HG-YYYYMMDD-NNNNN (same format as the Mac).
func (s *Store) NextCaseID(ctx context.Context, t time.Time) string {
	prefix := "HG-" + t.Format("20060102") + "-"
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence WHERE case_id LIKE ?`, prefix+"%").Scan(&n)
	return fmt.Sprintf("%s%05d", prefix, n+1)
}
