package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// videoTokenKeySetting holds the management server's video-token signing
// key: the Ed25519 seed, sealed with the install key like camera passwords.
const videoTokenKeySetting = "video_token_key"

// VideoTokenKey returns the Ed25519 key that signs video tokens, creating
// it on first use. Concurrent first calls agree on one key: the insert
// keeps whichever row landed first and everyone reads that back.
func (s *Store) VideoTokenKey(ctx context.Context) (ed25519.PrivateKey, error) {
	if sealed := s.Setting(ctx, videoTokenKeySetting, ""); sealed != "" {
		return s.openVideoKey(sealed)
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	sealed, err := s.Seal(base64.StdEncoding.EncodeToString(seed))
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`,
		videoTokenKeySetting, sealed); err != nil {
		return nil, err
	}
	sealed = s.Setting(ctx, videoTokenKeySetting, "")
	if sealed == "" {
		return nil, errors.New("video token key was not saved")
	}
	return s.openVideoKey(sealed)
}

func (s *Store) openVideoKey(sealed string) (ed25519.PrivateKey, error) {
	plain, err := s.Unseal(sealed)
	if err != nil {
		return nil, fmt.Errorf("video token key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(plain)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("video token key is corrupt")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
