package store

import (
	"context"
	"time"
)

// CameraEventsBetween lists one camera's detection events in [from, to),
// oldest first (timeline markers).
func (s *Store) CameraEventsBetween(ctx context.Context, cameraID string, from, to time.Time, limit int) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE camera_id = ? AND created_at >= ? AND created_at < ? ORDER BY created_at LIMIT ?`,
		cameraID, from.Unix(), to.Unix(), limit)
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

// CameraAlarmsBetween lists one camera's alarms raised in [from, to), oldest
// first (timeline markers).
func (s *Store) CameraAlarmsBetween(ctx context.Context, cameraID string, from, to time.Time, limit int) ([]Alarm, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+alarmCols+` FROM alarms WHERE camera_id = ? AND created_at >= ? AND created_at < ? ORDER BY created_at LIMIT ?`,
		cameraID, from.Unix(), to.Unix(), limit)
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
