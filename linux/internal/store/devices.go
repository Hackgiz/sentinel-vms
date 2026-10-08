package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// PairedDevice is an iPhone running Sentinel Mobile. Only a SHA-256 of its
// bearer token is stored (the Mac keeps tokens in plain JSON; we don't).
type PairedDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PairedBy   string `json:"pairedBy"`
	PairedAt   int64  `json:"pairedAt"`
	LastSeenAt int64  `json:"lastSeenAt"`
	HasPush    bool   `json:"hasPush"`

	apnsToken string
}

func (d PairedDevice) APNSToken() string { return d.apnsToken }

const deviceCols = `id, name, paired_by, paired_at, last_seen_at, apns_token`

func scanDevice(row interface{ Scan(...any) error }) (*PairedDevice, error) {
	d := &PairedDevice{}
	if err := row.Scan(&d.ID, &d.Name, &d.PairedBy, &d.PairedAt, &d.LastSeenAt, &d.apnsToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.HasPush = d.apnsToken != ""
	return d, nil
}

// CreatePairedDevice registers a phone and returns it with its bearer token
// (shown to the phone exactly once). Token format matches the Mac's: 48 random
// bytes, URL-safe base64 without padding.
func (s *Store) CreatePairedDevice(ctx context.Context, name, pairedBy string) (*PairedDevice, string, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	d := &PairedDevice{ID: NewID(), Name: name, PairedBy: pairedBy, PairedAt: now()}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO paired_devices(id, name, token_hash, paired_by, paired_at) VALUES(?,?,?,?,?)`,
		d.ID, d.Name, hashToken(token), d.PairedBy, d.PairedAt)
	if err != nil {
		return nil, "", err
	}
	return d, token, nil
}

// DeviceForToken authenticates a phone. ErrNotFound means the token is unknown
// (revoked or never issued) — only then should callers answer 401, because
// Sentinel Mobile unpairs itself on a 401.
func (s *Store) DeviceForToken(ctx context.Context, token string) (*PairedDevice, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	d, err := scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM paired_devices WHERE token_hash = ?`, hashToken(token)))
	if err != nil {
		return nil, err
	}
	// Video makes several requests a second; only write last-seen once a minute.
	if t := now(); t-d.LastSeenAt >= 60 {
		_, _ = s.DB.ExecContext(ctx, `UPDATE paired_devices SET last_seen_at = ? WHERE id = ?`, t, d.ID)
		d.LastSeenAt = t
	}
	return d, nil
}

func (s *Store) PairedDevices(ctx context.Context) ([]PairedDevice, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+deviceCols+` FROM paired_devices ORDER BY paired_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PairedDevice{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) PairedDevice(ctx context.Context, id string) (*PairedDevice, error) {
	return scanDevice(s.DB.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM paired_devices WHERE id = ?`, id))
}

func (s *Store) RevokeDevice(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM paired_devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetDeviceAPNSToken(ctx context.Context, id, apnsToken string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE paired_devices SET apns_token = ?, last_seen_at = ? WHERE id = ?`, apnsToken, time.Now().Unix(), id)
	return err
}
