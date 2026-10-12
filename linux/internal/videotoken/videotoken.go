// Package videotoken signs and verifies video tokens (PLATFORM.md, "Client
// API v2"; api/openapi.yaml, "Video"): short-lived, Ed25519-signed claims
// that let a client fetch one camera's video without its API token.
//
// Format: "v1.<payload>.<signature>", both unpadded base64url. The payload
// is the JSON claims; the signature is Ed25519 over the ASCII bytes
// "v1.<payload>". The management server signs; whoever serves the video
// (the management server's gateway in phase 1, the recording server in
// phase 2) verifies offline with the management server's public keys.
//
// The package depends on the standard library only, so the recording side
// can use it without importing the management side.
package videotoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Right is what a token lets its holder fetch.
type Right string

const (
	Live     Right = "live"     // live HLS and WebRTC
	Playback Right = "playback" // recorded segments and playback playlists
	Export   Right = "export"   // recorded footage, for saving a copy
)

const (
	// MaxLifetime is the longest a token may live (exp - iat).
	MaxLifetime = 10 * time.Minute
	// ClockSkew is how far the verifier's clock may be behind or ahead of
	// the signer's.
	ClockSkew = 30 * time.Second

	prefix = "v1."
)

var (
	ErrMalformed     = errors.New("videotoken: malformed token")
	ErrBadSignature  = errors.New("videotoken: bad signature")
	ErrExpired       = errors.New("videotoken: expired")
	ErrWrongAudience = errors.New("videotoken: issued for another server")
)

var b64 = base64.RawURLEncoding.Strict()

// Claims is a token's payload. IssuedAt and Expires are Unix seconds.
type Claims struct {
	Camera   string  `json:"cam"`
	Subject  string  `json:"sub"` // user id
	Rights   []Right `json:"rights"`
	Audience string  `json:"aud"` // recording server id
	IssuedAt int64   `json:"iat"`
	Expires  int64   `json:"exp"`
	ID       string  `json:"jti"`
}

// Has reports whether the claims grant r.
func (c Claims) Has(r Right) bool {
	for _, have := range c.Rights {
		if have == r {
			return true
		}
	}
	return false
}

// ExpiresAt is the expiry as a time.
func (c Claims) ExpiresAt() time.Time { return time.Unix(c.Expires, 0) }

// valid checks the claims' shape (not time or audience).
func (c Claims) valid() error {
	if c.Camera == "" || c.Subject == "" || c.Audience == "" || c.ID == "" || len(c.Rights) == 0 {
		return ErrMalformed
	}
	for _, r := range c.Rights {
		if !ValidRight(r) {
			return ErrMalformed
		}
	}
	if c.Expires <= c.IssuedAt || time.Duration(c.Expires-c.IssuedAt)*time.Second > MaxLifetime {
		return ErrMalformed
	}
	return nil
}

// ValidRight reports whether r is one of the known rights.
func ValidRight(r Right) bool { return r == Live || r == Playback || r == Export }

// MARK: - Signing

// Signer issues tokens with the management server's private key.
type Signer struct {
	key ed25519.PrivateKey
	// Now is the clock (nil = time.Now); tests move it.
	Now func() time.Time
}

func NewSigner(key ed25519.PrivateKey) *Signer { return &Signer{key: key} }

// PublicKey is the key verifiers need.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.key.Public().(ed25519.PublicKey) }

func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sign issues a token for c (Camera, Subject, Rights, Audience) valid for
// ttl, which must be positive and at most MaxLifetime. It fills in IssuedAt,
// Expires and a random ID and returns the claims it signed.
func (s *Signer) Sign(c Claims, ttl time.Duration) (string, Claims, error) {
	if ttl <= 0 || ttl > MaxLifetime {
		return "", Claims{}, fmt.Errorf("videotoken: lifetime %v is outside (0, %v]", ttl, MaxLifetime)
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", Claims{}, err
	}
	now := s.now()
	c.IssuedAt = now.Unix()
	c.Expires = now.Add(ttl).Unix()
	c.ID = hex.EncodeToString(jti)
	if err := c.valid(); err != nil {
		return "", Claims{}, fmt.Errorf("videotoken: incomplete claims (camera, subject, audience and known rights are required)")
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", Claims{}, err
	}
	signed := prefix + b64.EncodeToString(payload)
	return signed + "." + b64.EncodeToString(ed25519.Sign(s.key, []byte(signed))), c, nil
}

// MARK: - Verifying

// Verifier checks tokens for one audience against one or more public keys
// (several during a key rotation).
type Verifier struct {
	audience string
	keys     []ed25519.PublicKey
	// Now is the clock (nil = time.Now); tests move it.
	Now func() time.Time
}

// NewVerifier accepts tokens for audience signed by any of keys.
func NewVerifier(audience string, keys ...ed25519.PublicKey) *Verifier {
	return &Verifier{audience: audience, keys: keys}
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify checks the token's format, signature, expiry (with ClockSkew) and
// audience, and returns its claims. Errors are ErrMalformed,
// ErrBadSignature, ErrExpired or ErrWrongAudience.
func (v *Verifier) Verify(token string) (Claims, error) {
	if !strings.HasPrefix(token, prefix) {
		return Claims{}, ErrMalformed
	}
	dot := strings.LastIndexByte(token, '.')
	if dot < len(prefix) {
		return Claims{}, ErrMalformed
	}
	signed, sigPart := token[:dot], token[dot+1:]
	payload, err1 := b64.DecodeString(signed[len(prefix):])
	sig, err2 := b64.DecodeString(sigPart)
	if err1 != nil || err2 != nil || len(sig) != ed25519.SignatureSize {
		return Claims{}, ErrMalformed
	}
	if !v.signedByUs([]byte(signed), sig) {
		return Claims{}, ErrBadSignature
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil || c.valid() != nil {
		return Claims{}, ErrMalformed
	}
	now := v.now()
	if now.Add(-ClockSkew).Unix() >= c.Expires {
		return Claims{}, ErrExpired
	}
	if time.Unix(c.IssuedAt, 0).After(now.Add(ClockSkew)) {
		return Claims{}, ErrMalformed // issued in the future: a clock is badly off
	}
	if c.Audience != v.audience {
		return Claims{}, ErrWrongAudience
	}
	return c, nil
}

func (v *Verifier) signedByUs(msg, sig []byte) bool {
	for _, k := range v.keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, msg, sig) {
			return true
		}
	}
	return false
}

// MARK: - Key encoding

// EncodePublicKey is the form public keys travel in (link.Snapshot):
// standard base64.
func EncodePublicKey(k ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(k) }

// ParsePublicKeys decodes EncodePublicKey strings.
func ParsePublicKeys(encoded []string) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, len(encoded))
	for _, e := range encoded {
		b, err := base64.StdEncoding.DecodeString(e)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("videotoken: bad public key %q", e)
		}
		keys = append(keys, ed25519.PublicKey(b))
	}
	return keys, nil
}
