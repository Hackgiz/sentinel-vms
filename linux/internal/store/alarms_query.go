package store

// Alarm Manager queries (WAVE4-OPERATOR.md, Stream C): the filtered,
// keyset-paged alarm list, per-state counts, assignment and the events
// behind an alarm.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// AlarmQuery selects alarms for SearchAlarms. Zero values mean "no
// constraint". From/To are Unix seconds on created_at, From inclusive and To
// exclusive. CameraIDs non-empty restricts to those cameras (and so leaves
// out camera-less alarms); the server fills it from the caller's scope.
type AlarmQuery struct {
	States     []string
	Severities []string
	CameraIDs  []string
	Owner      string // exact; "unassigned" matches alarms with no owner
	From, To   int64
	Text       string // case-insensitive LIKE over title and detail
}

func (q AlarmQuery) where() ([]string, []any) {
	var where []string
	var args []any
	inClause("state", q.States, &where, &args)
	inClause("severity", q.Severities, &where, &args)
	inClause("camera_id", q.CameraIDs, &where, &args)
	switch q.Owner {
	case "":
	case "unassigned":
		where = append(where, `(owner = '' OR owner = 'Unassigned')`)
	default:
		where = append(where, `owner = ?`)
		args = append(args, q.Owner)
	}
	if q.From > 0 {
		where = append(where, `created_at >= ?`)
		args = append(args, q.From)
	}
	if q.To > 0 {
		where = append(where, `created_at < ?`)
		args = append(args, q.To)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		pat := "%" + escapeLike(t) + "%"
		where = append(where, `(title LIKE ? ESCAPE '\' OR detail LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	return where, args
}

// SearchAlarms lists up to limit matching alarms after key, newest raised
// first.
func (s *Store) SearchAlarms(ctx context.Context, q AlarmQuery, after PageKey, limit int) ([]Alarm, error) {
	where, args := q.where()
	sql, args := pageQuery(`SELECT `+alarmCols+` FROM alarms`, where, args, after, limit)
	rows, err := s.DB.QueryContext(ctx, sql, args...)
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

// AlarmStateCounts counts alarms per state under q (its States are ignored).
func (s *Store) AlarmStateCounts(ctx context.Context, q AlarmQuery) (map[string]int, error) {
	q.States = nil
	where, args := q.where()
	sql := `SELECT state, COUNT(*) FROM alarms`
	if len(where) > 0 {
		sql += ` WHERE ` + strings.Join(where, ` AND `)
	}
	rows, err := s.DB.QueryContext(ctx, sql+` GROUP BY state`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for _, st := range AlarmStates {
		out[st] = 0
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// AssignAlarm sets (or, with "", clears) an alarm's owner and appends line
// to its response log.
func (s *Store) AssignAlarm(ctx context.Context, id, owner, line string) (*Alarm, error) {
	a, err := s.Alarm(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Owner, a.UpdatedAt = owner, now()
	if owner == "" {
		a.Owner = "Unassigned"
	}
	if line != "" {
		a.ResponseLog = append(a.ResponseLog, time.Now().Format("Jan 2 15:04")+" — "+line)
	}
	log, _ := json.Marshal(a.ResponseLog)
	_, err = s.DB.ExecContext(ctx, `UPDATE alarms SET owner=?, updated_at=?, response_log=? WHERE id=?`, a.Owner, a.UpdatedAt, string(log), id)
	return a, err
}

// EventsForAlarm lists the detection events attached to an alarm, oldest
// first.
func (s *Store) EventsForAlarm(ctx context.Context, alarmID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE alarm_id = ? ORDER BY created_at, id LIMIT ?`, alarmID, limit)
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

// ValidAlarmSeverity reports whether s is a severity an operator may set.
func ValidAlarmSeverity(s string) bool { return s == "Critical" || s == "Warning" || s == "Info" }

// SetAlarmSeverity changes an alarm's severity and appends line to its log.
func (s *Store) SetAlarmSeverity(ctx context.Context, id, severity, line string) (*Alarm, error) {
	a, err := s.Alarm(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Severity, a.UpdatedAt = severity, now()
	if line != "" {
		a.ResponseLog = append(a.ResponseLog, time.Now().Format("Jan 2 15:04")+" — "+line)
	}
	log, _ := json.Marshal(a.ResponseLog)
	_, err = s.DB.ExecContext(ctx, `UPDATE alarms SET severity=?, updated_at=?, response_log=? WHERE id=?`, a.Severity, a.UpdatedAt, string(log), id)
	return a, err
}
