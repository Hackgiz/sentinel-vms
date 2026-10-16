package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Webhook is a stored outbound webhook. Secret is write-only at the API: it
// never leaves the server except as an HMAC key.
type Webhook struct {
	ID          string
	Name        string
	URL         string
	Secret      string
	Events      []string // event-kind subscriptions; empty = rule actions only
	Enabled     bool
	LastStatus  string
	LastFiredAt int64 // unix seconds
	CreatedAt   int64
}

const webhookCols = `id, name, url, secret, events, enabled, last_status, last_fired_at, created_at`

// scanWebhook reads a row; the secret is stored sealed and comes back plain.
func (s *Store) scanWebhook(row interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	var events string
	var enabled int
	if err := row.Scan(&w.ID, &w.Name, &w.URL, &w.Secret, &events, &enabled, &w.LastStatus, &w.LastFiredAt, &w.CreatedAt); err != nil {
		return nil, err
	}
	w.Secret, _ = s.Unseal(w.Secret)
	w.Enabled = enabled != 0
	if json.Unmarshal([]byte(events), &w.Events) != nil || w.Events == nil {
		w.Events = []string{}
	}
	return &w, nil
}

func (s *Store) Webhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+webhookCols+` FROM webhooks ORDER BY name COLLATE NOCASE, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		w, err := s.scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (s *Store) Webhook(ctx context.Context, id string) (*Webhook, error) {
	w, err := s.scanWebhook(s.DB.QueryRowContext(ctx, `SELECT `+webhookCols+` FROM webhooks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// SaveWebhook inserts (empty ID) or replaces a webhook's configuration. It
// leaves last_status / last_fired_at alone.
func (s *Store) SaveWebhook(ctx context.Context, w *Webhook) error {
	events, _ := json.Marshal(append([]string{}, w.Events...))
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	sealed, err := s.Seal(w.Secret)
	if err != nil {
		return err
	}
	if w.ID == "" {
		w.ID, w.CreatedAt = NewID(), now()
		_, err := s.DB.ExecContext(ctx, `INSERT INTO webhooks(id, name, url, secret, events, enabled, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			w.ID, w.Name, w.URL, sealed, string(events), enabled, w.CreatedAt)
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE webhooks SET name = ?, url = ?, secret = ?, events = ?, enabled = ? WHERE id = ?`,
		w.Name, w.URL, sealed, string(events), enabled, w.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWebhookResult records the outcome of the latest delivery.
func (s *Store) SetWebhookResult(ctx context.Context, id, status string, at int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE webhooks SET last_status = ?, last_fired_at = ? WHERE id = ?`, status, at, id)
	return err
}

func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
