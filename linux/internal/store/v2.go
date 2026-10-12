package store

// Queries for API v2 (internal/server/v2_resources.go): keyset-paged alarm
// and event lists, and the user and camera edits v1 has no endpoint for.

import (
	"context"
	"strings"
)

// PageKey marks a position in a list ordered newest first by (created_at,
// id). The zero key starts at the top. Items created after a page was read
// sort before its key, so paging on is stable while new ones arrive.
type PageKey struct {
	CreatedAt int64 // Unix s
	ID        string
}

// IsZero reports the first page.
func (k PageKey) IsZero() bool { return k.ID == "" }

// AlarmFilter selects alarms for AlarmsPage.
type AlarmFilter struct {
	OpenOnly bool   // drop Resolved and False Alarm
	CameraID string // "" = every camera
}

// AlarmsPage lists up to limit alarms after key, newest raised first.
func (s *Store) AlarmsPage(ctx context.Context, f AlarmFilter, after PageKey, limit int) ([]Alarm, error) {
	var where []string
	var args []any
	if f.OpenOnly {
		where = append(where, `state NOT IN ('Resolved', 'False Alarm')`)
	}
	if f.CameraID != "" {
		where = append(where, `camera_id = ?`)
		args = append(args, f.CameraID)
	}
	q, args := pageQuery(`SELECT `+alarmCols+` FROM alarms`, where, args, after, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
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

// EventsPage lists up to limit detection events after key, newest first;
// cameraID "" = every camera.
func (s *Store) EventsPage(ctx context.Context, cameraID string, after PageKey, limit int) ([]Event, error) {
	var where []string
	var args []any
	if cameraID != "" {
		where = append(where, `camera_id = ?`)
		args = append(args, cameraID)
	}
	q, args := pageQuery(`SELECT `+eventCols+` FROM events`, where, args, after, limit)
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

// pageQuery adds the key condition, newest-first order and limit to a
// SELECT over a table with created_at and id columns.
func pageQuery(sel string, where []string, args []any, after PageKey, limit int) (string, []any) {
	if !after.IsZero() {
		where = append(where, `(created_at < ? OR (created_at = ? AND id < ?))`)
		args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
	}
	if len(where) > 0 {
		sel += ` WHERE ` + strings.Join(where, ` AND `)
	}
	return sel + ` ORDER BY created_at DESC, id DESC LIMIT ?`, append(args, limit)
}

// SetUserRole changes a user's role.
func (s *Store) SetUserRole(ctx context.Context, id, role string) error {
	return s.updateOne(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
}

// SetUserPassword replaces a user's password hash.
func (s *Store) SetUserPassword(ctx context.Context, id, passwordHash string) error {
	return s.updateOne(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id)
}

// SetCameraSortOrder moves a camera in the display order.
func (s *Store) SetCameraSortOrder(ctx context.Context, id string, order int) error {
	return s.updateOne(ctx, `UPDATE cameras SET sort_order = ? WHERE id = ?`, order, id)
}

// updateOne runs an UPDATE that must hit one row (ErrNotFound otherwise).
func (s *Store) updateOne(ctx context.Context, q string, args ...any) error {
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
