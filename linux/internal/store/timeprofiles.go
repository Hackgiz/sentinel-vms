package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"sentinel-linux/internal/rules"
)

// TimeProfiles lists the time profiles by name.
func (s *Store) TimeProfiles(ctx context.Context) ([]rules.TimeProfile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, body, created_at FROM time_profiles ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rules.TimeProfile{}
	for rows.Next() {
		p, err := scanTimeProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) TimeProfile(ctx context.Context, id string) (*rules.TimeProfile, error) {
	p, err := scanTimeProfile(s.DB.QueryRowContext(ctx, `SELECT id, name, body, created_at FROM time_profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func scanTimeProfile(row interface{ Scan(...any) error }) (*rules.TimeProfile, error) {
	var p rules.TimeProfile
	var body string
	if err := row.Scan(&p.ID, &p.Name, &body, &p.CreatedAt); err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(body), &p) != nil {
		p.Weekly = nil // unreadable body: a profile that is never active
	}
	if p.Weekly == nil {
		p.Weekly = []rules.Span{}
	}
	return &p, nil
}

func timeProfileBody(p *rules.TimeProfile) string {
	b, _ := json.Marshal(struct {
		Weekly []rules.Span `json:"weekly"`
	}{p.Weekly})
	return string(b)
}

// SaveTimeProfile inserts (empty ID) or replaces a profile. ErrDuplicateName
// when another profile has the name.
func (s *Store) SaveTimeProfile(ctx context.Context, p *rules.TimeProfile) error {
	p.Name = strings.TrimSpace(p.Name)
	var err error
	if p.ID == "" {
		p.ID, p.CreatedAt = NewID(), now()
		_, err = s.DB.ExecContext(ctx, `INSERT INTO time_profiles(id, name, body, created_at) VALUES(?, ?, ?, ?)`,
			p.ID, p.Name, timeProfileBody(p), p.CreatedAt)
	} else {
		var res sql.Result
		res, err = s.DB.ExecContext(ctx, `UPDATE time_profiles SET name = ?, body = ? WHERE id = ?`, p.Name, timeProfileBody(p), p.ID)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				return ErrNotFound
			}
		}
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDuplicateName
	}
	return err
}

func (s *Store) DeleteTimeProfile(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM time_profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RulesUsingTimeProfile names the rules that reference the profile.
func (s *Store) RulesUsingTimeProfile(ctx context.Context, id string) ([]string, error) {
	list, err := s.AlarmRules(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range list {
		if r.TimeProfileID == id {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// RulesUsingWebhook names the rules with a webhook action for the webhook.
func (s *Store) RulesUsingWebhook(ctx context.Context, id string) ([]string, error) {
	list, err := s.AlarmRules(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range list {
		for _, a := range r.Actions {
			if a.Type == rules.ActionWebhook && a.WebhookID == id {
				names = append(names, r.Name)
				break
			}
		}
	}
	return names, nil
}
