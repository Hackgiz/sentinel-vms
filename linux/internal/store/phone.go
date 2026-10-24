package store

// Phone cameras (WAVE23-PHONE-CAMERA.md section A): the push key an iPhone
// trades for push tokens. Only its SHA-256 is stored; the key itself is a
// random 32-byte secret shown once when it is made.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// NewPushKey returns a new push key (32 random bytes, unpadded base64url)
// and the hash to store.
func NewPushKey() (key, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	key = base64.RawURLEncoding.EncodeToString(raw)
	return key, pushKeyHash(key), nil
}

func pushKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// SetCameraPushKey stores a phone camera's push key hash ("" revokes it);
// the old key stops working at once. ErrNotFound if there is no camera.
func (s *Store) SetCameraPushKey(ctx context.Context, cameraID, hash string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE cameras SET push_key_hash = ? WHERE id = ?`, hash, cameraID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CheckCameraPushKey reports whether key is the phone camera's current push
// key (constant-time on the hashes). False when the camera has none.
func (s *Store) CheckCameraPushKey(ctx context.Context, cameraID, key string) (bool, error) {
	var stored string
	err := s.DB.QueryRowContext(ctx, `SELECT push_key_hash FROM cameras WHERE id = ? AND source = 'phone'`, cameraID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	if stored == "" || key == "" {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(pushKeyHash(key))) == 1, nil
}
