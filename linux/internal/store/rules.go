package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"sentinel-linux/internal/rules"
)

// AlarmRules lists the alarm rules in display order.
func (s *Store) AlarmRules(ctx context.Context) ([]rules.Rule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, body FROM alarm_rules ORDER BY position, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rules.Rule{}
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		var r rules.Rule
		if json.Unmarshal([]byte(body), &r) != nil {
			continue // unreadable row: skip rather than break every alarm
		}
		r.ID = id
		r.Normalize()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AlarmRule(ctx context.Context, id string) (*rules.Rule, error) {
	var body string
	err := s.DB.QueryRowContext(ctx, `SELECT body FROM alarm_rules WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var r rules.Rule
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return nil, err
	}
	r.ID = id
	r.Normalize()
	return &r, nil
}

// SaveAlarmRule inserts (empty ID) or replaces a rule. New rules go last.
func (s *Store) SaveAlarmRule(ctx context.Context, r *rules.Rule) error {
	t := now()
	r.UpdatedAt = t
	if r.ID == "" {
		r.ID, r.CreatedAt = NewID(), t
		body, _ := json.Marshal(r)
		_, err := s.DB.ExecContext(ctx, `INSERT INTO alarm_rules(id, position, body, created_at)
			VALUES(?, (SELECT COALESCE(MAX(position), 0) + 1 FROM alarm_rules), ?, ?)`, r.ID, string(body), r.CreatedAt)
		return err
	}
	body, _ := json.Marshal(r)
	res, err := s.DB.ExecContext(ctx, `UPDATE alarm_rules SET body = ? WHERE id = ?`, string(body), r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAlarmRule(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM alarm_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
