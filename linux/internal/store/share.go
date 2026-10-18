package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// Expiring share links (WAVE7-FEATURES.md, Stream D): a link hands one
// evidence item to someone without an account. The URL token is 32 random
// bytes, base64url, shown once at creation; only its SHA-256 is stored.

// ShareLink is a row of share_links, joined with its evidence item.
type ShareLink struct {
	ID         string
	EvidenceID string
	CaseID     string // from the evidence row ("" if it was released)
	CameraID   string
	Title      string
	Note       string
	ExpiresAt  int64 // Unix seconds
	MaxUses    int   // 0: unlimited within expiry
	Uses       int
	CreatedBy  string
	CreatedAt  int64
	RevokedAt  int64 // 0: live
}

// Live reports whether the link can still be opened at now: not revoked,
// not expired and not used up.
func (l *ShareLink) Live(now time.Time) bool {
	return l.RevokedAt == 0 && l.ExpiresAt > now.Unix() && (l.MaxUses == 0 || l.Uses < l.MaxUses)
}

// HashShareToken is the hex SHA-256 stored for a token.
func HashShareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewShareToken makes a fresh 32-byte base64url token.
func NewShareToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const shareCols = `l.id, l.evidence_id, COALESCE(e.case_id, ''), COALESCE(e.camera_id, ''), COALESCE(e.title, ''), l.note,
	l.expires_at, l.max_uses, l.uses, l.created_by, l.created_at, l.revoked_at`

const shareFrom = ` FROM share_links l LEFT JOIN evidence e ON e.id = l.evidence_id`

func scanShareLink(row interface{ Scan(...any) error }) (*ShareLink, error) {
	l := &ShareLink{}
	err := row.Scan(&l.ID, &l.EvidenceID, &l.CaseID, &l.CameraID, &l.Title, &l.Note,
		&l.ExpiresAt, &l.MaxUses, &l.Uses, &l.CreatedBy, &l.CreatedAt, &l.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// CreateShareLink stores a link to an evidence item and returns it with the
// plaintext token, which is never stored and can't be recovered later.
func (s *Store) CreateShareLink(ctx context.Context, evidenceID, note string, expiresAt time.Time, maxUses int, createdBy string) (*ShareLink, string, error) {
	token, err := NewShareToken()
	if err != nil {
		return nil, "", err
	}
	id := NewID()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO share_links(id, token_hash, evidence_id, note, expires_at, max_uses, uses, created_by, created_at, revoked_at)
		VALUES(?,?,?,?,?,?,0,?,?,0)`, id, HashShareToken(token), evidenceID, note, expiresAt.Unix(), maxUses, createdBy, time.Now().Unix())
	if err != nil {
		return nil, "", err
	}
	l, err := s.ShareLinkByID(ctx, id)
	return l, token, err
}

// ShareLinkByID reads a link by id.
func (s *Store) ShareLinkByID(ctx context.Context, id string) (*ShareLink, error) {
	return scanShareLink(s.DB.QueryRowContext(ctx, `SELECT `+shareCols+shareFrom+` WHERE l.id = ?`, id))
}

// ShareLinkByToken looks a link up by the hash of its token (ErrNotFound if
// none). The stored hash is compared in constant time as well, so the
// answer never depends on how much of a guess matched.
func (s *Store) ShareLinkByToken(ctx context.Context, token string) (*ShareLink, error) {
	want := HashShareToken(token)
	var id, got string
	err := s.DB.QueryRowContext(ctx, `SELECT id, token_hash FROM share_links WHERE token_hash = ?`, want).Scan(&id, &got)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return nil, ErrNotFound
	}
	return s.ShareLinkByID(ctx, id)
}

// ShareLinks lists every link, newest first (revoked and expired included).
func (s *Store) ShareLinks(ctx context.Context) ([]ShareLink, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+shareCols+shareFrom+` ORDER BY l.created_at DESC, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareLink{}
	for rows.Next() {
		l, err := scanShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// ClaimShareUse counts one opening of a live link, atomically: it fails with
// ErrNotFound when the link is revoked, expired or used up (so two racing
// requests can't both take the last use).
func (s *Store) ClaimShareUse(ctx context.Context, id string, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE share_links SET uses = uses + 1
		WHERE id = ? AND revoked_at = 0 AND expires_at > ? AND (max_uses = 0 OR uses < max_uses)`, id, now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeShareLink revokes a link (idempotent: the first revocation time is
// kept). ErrNotFound if there is no such link.
func (s *Store) RevokeShareLink(ctx context.Context, id string, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE share_links SET revoked_at = CASE WHEN revoked_at = 0 THEN ? ELSE revoked_at END WHERE id = ?`, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
