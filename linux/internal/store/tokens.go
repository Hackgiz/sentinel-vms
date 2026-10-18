package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// API v2 bearer tokens (PLATFORM.md, "Client API v2"). Each sign-in creates
// a token family: a short-lived opaque access token and a rotating refresh
// token, both stored only as SHA-256 hashes. Refreshing replaces both; a
// refresh token presented again after it was rotated means it leaked, so
// the whole family is revoked.

// ErrTokenReuse: a rotated refresh token came back; its family is revoked.
var ErrTokenReuse = errors.New("refresh token already used; sign in again")

// TokenPair is what a client gets on sign-in or refresh.
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	FamilyID         string
}

func newOpaqueToken(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func newPair(accessTTL, refreshTTL time.Duration) (TokenPair, error) {
	a, err := newOpaqueToken("sat_")
	if err != nil {
		return TokenPair{}, err
	}
	r, err := newOpaqueToken("srt_")
	if err != nil {
		return TokenPair{}, err
	}
	t := time.Now()
	return TokenPair{AccessToken: a, AccessExpiresAt: t.Add(accessTTL), RefreshToken: r, RefreshExpiresAt: t.Add(refreshTTL)}, nil
}

// CreateTokenPair starts a new token family for a user. client is a free
// label ("Sentinel Operator on Mac-mini") shown in the audit log.
func (s *Store) CreateTokenPair(ctx context.Context, userID, client string, accessTTL, refreshTTL time.Duration) (TokenPair, error) {
	p, err := newPair(accessTTL, refreshTTL)
	if err != nil {
		return p, err
	}
	p.FamilyID = NewID()
	n := now()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO api_tokens(id, family_id, user_id, client, access_hash, access_expires_at, refresh_hash, refresh_expires_at, created_at, used_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, NewID(), p.FamilyID, userID, client, hashToken(p.AccessToken), p.AccessExpiresAt.Unix(),
		hashToken(p.RefreshToken), p.RefreshExpiresAt.Unix(), n, n)
	return p, err
}

// AccessTokenUser resolves a bearer access token to its user.
func (s *Store) AccessTokenUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var family string
	u := &User{}
	err := s.DB.QueryRowContext(ctx, `SELECT u.id, u.name, u.role, u.password_hash, u.created_at, t.family_id
		FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.access_hash = ? AND t.access_expires_at > ?`, hashToken(token), now()).
		Scan(&u.ID, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt, &family)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := capDeviceToken(ctx, s.DB, family, u); err != nil { // WAVE10(C): device-code sign-ins are Viewer-capped (device_codes.go)
		return nil, err
	}
	return u, nil
}

// RefreshTokenPair rotates a family: the refresh token is exchanged for a
// new pair. Returns the user too, so the caller can re-check the account.
func (s *Store) RefreshTokenPair(ctx context.Context, refreshToken string, accessTTL, refreshTTL time.Duration) (TokenPair, *User, error) {
	h := hashToken(refreshToken)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TokenPair{}, nil, err
	}
	defer tx.Rollback()
	var id, family, userID string
	var refreshExp int64
	err = tx.QueryRowContext(ctx, `SELECT id, family_id, user_id, refresh_expires_at FROM api_tokens WHERE refresh_hash = ?`, h).
		Scan(&id, &family, &userID, &refreshExp)
	if errors.Is(err, sql.ErrNoRows) {
		// Rotated already? Then someone else has it: revoke the family.
		if err := tx.QueryRowContext(ctx, `SELECT family_id FROM api_tokens WHERE prev_refresh_hash = ?`, h).Scan(&family); err == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE family_id = ?`, family); err != nil {
				return TokenPair{}, nil, err
			}
			if err := tx.Commit(); err != nil {
				return TokenPair{}, nil, err
			}
			return TokenPair{}, nil, ErrTokenReuse
		}
		return TokenPair{}, nil, ErrNotFound
	}
	if err != nil {
		return TokenPair{}, nil, err
	}
	if refreshExp <= now() {
		_, _ = tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
		_ = tx.Commit()
		return TokenPair{}, nil, ErrNotFound
	}
	p, err := newPair(accessTTL, refreshTTL)
	if err != nil {
		return p, nil, err
	}
	p.FamilyID = family
	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET access_hash = ?, access_expires_at = ?, refresh_hash = ?, prev_refresh_hash = ?,
		refresh_expires_at = ?, used_at = ? WHERE id = ?`, hashToken(p.AccessToken), p.AccessExpiresAt.Unix(), hashToken(p.RefreshToken), h,
		p.RefreshExpiresAt.Unix(), now(), id); err != nil {
		return p, nil, err
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT id, name, role, password_hash, created_at FROM users WHERE id = ?`, userID))
	if err != nil {
		return p, nil, err
	}
	if err := capDeviceToken(ctx, tx, family, u); err != nil { // WAVE10(C)
		return TokenPair{}, nil, err
	}
	return p, u, tx.Commit()
}

// RevokeRefreshToken ends the family a refresh token belongs to (sign out
// on one device). Unknown tokens are not an error.
func (s *Store) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE refresh_hash = ?`, hashToken(refreshToken))
	return err
}

// RevokeAccessTokenFamily ends the family an access token belongs to.
func (s *Store) RevokeAccessTokenFamily(ctx context.Context, accessToken string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE family_id IN (SELECT family_id FROM api_tokens WHERE access_hash = ?)`, hashToken(accessToken))
	return err
}

// DeleteUserTokens signs a user out of every API client.
func (s *Store) DeleteUserTokens(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID)
	return err
}

// PruneTokens drops families whose refresh token expired.
func (s *Store) PruneTokens(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE refresh_expires_at <= ?`, now())
}
