package pki

// Camera credentials are sealed to one recording server's X25519 key
// (nacl/box anonymous: an ephemeral sender key, so only the recording
// server can open it and the management server needs no key of its own).

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

// SealKey is a recording server's X25519 key pair.
type SealKey struct {
	Public  [32]byte
	Private [32]byte
}

// NewSealKey makes a sealing key pair.
func NewSealKey() (*SealKey, error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SealKey{Public: *pub, Private: *priv}, nil
}

// SealKeyFromPrivate rebuilds the pair from its private half.
func SealKeyFromPrivate(priv []byte) (*SealKey, error) {
	if len(priv) != 32 {
		return nil, errors.New("pki: seal key must be 32 bytes")
	}
	k := &SealKey{}
	copy(k.Private[:], priv)
	pub, err := curve25519.X25519(k.Private[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	copy(k.Public[:], pub)
	return k, nil
}

// PublicString is the public key as it travels and is stored: standard
// base64.
func (k *SealKey) PublicString() string { return base64.StdEncoding.EncodeToString(k.Public[:]) }

// PrivateString is the private key for <data>/link/seal.key.
func (k *SealKey) PrivateString() string { return base64.StdEncoding.EncodeToString(k.Private[:]) }

// ParseSealKey decodes PrivateString.
func ParseSealKey(s string) (*SealKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("pki: seal key: %w", err)
	}
	return SealKeyFromPrivate(b)
}

// ParseSealPublicKey decodes a PublicString.
func ParseSealPublicKey(s string) (*[32]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("pki: bad seal public key")
	}
	var k [32]byte
	copy(k[:], b)
	return &k, nil
}

// Seal encrypts msg to pub; the result is standard base64.
func Seal(pub *[32]byte, msg []byte) (string, error) {
	out, err := box.SealAnonymous(nil, msg, pub, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a Seal result.
func (k *SealKey) Open(sealed string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, errors.New("pki: sealed value is not base64")
	}
	out, ok := box.OpenAnonymous(nil, b, &k.Public, &k.Private)
	if !ok {
		return nil, errors.New("pki: sealed value can't be opened with this key")
	}
	return out, nil
}
