package store

import "context"

// ActivityCount is one bucket of detections: a camera, a kind and the start of
// an hour (Unix seconds, like every timestamp in the store). Camera scope is applied by the
// caller, so this stays a plain aggregate.
type ActivityCount struct {
	CameraID string
	Kind     string
	Hour     int64
	Count    int
}

// AlarmCount is one row of alarms opened in a range.
type AlarmCount struct {
	CameraID string
	Kind     string
	Severity string
	Count    int
}

const hourSec = int64(3600)

// EventActivity counts events created in [from, to) (Unix seconds) per camera,
// kind and hour.
func (s *Store) EventActivity(ctx context.Context, from, to int64) ([]ActivityCount, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT camera_id, kind, (created_at / ?) * ?, COUNT(*)
		FROM events
		WHERE created_at >= ? AND created_at < ?
		GROUP BY camera_id, kind, (created_at / ?)
		ORDER BY 3, 1, 2`, hourSec, hourSec, from, to, hourSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivityCount{}
	for rows.Next() {
		var c ActivityCount
		if err := rows.Scan(&c.CameraID, &c.Kind, &c.Hour, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AlarmActivity counts alarms opened (created_at, Unix seconds) in [from, to)
// per camera, kind and severity.
func (s *Store) AlarmActivity(ctx context.Context, from, to int64) ([]AlarmCount, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT camera_id, kind, severity, COUNT(*)
		FROM alarms
		WHERE created_at >= ? AND created_at < ?
		GROUP BY camera_id, kind, severity
		ORDER BY 4 DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlarmCount{}
	for rows.Next() {
		var c AlarmCount
		if err := rows.Scan(&c.CameraID, &c.Kind, &c.Severity, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
