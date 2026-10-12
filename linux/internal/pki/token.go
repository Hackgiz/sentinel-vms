package pki

// Enrollment tokens: sen1.<secret>.<base64url SHA-256 of the CA DER>. The
// store keeps only the secret's hash; the pin lets a new recording server
// trust the management server's CA on its first connection.

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
)

const tokenPrefix = "sen1"

// ErrBadToken is a malformed enrollment token.
var ErrBadToken = errors.New("pki: malformed enrollment token (want sen1.<secret>.<pin>)")

// FormatToken builds the token an admin hands out.
func FormatToken(secret string, caPin []byte) string {
	return tokenPrefix + "." + secret + "." + base64.RawURLEncoding.EncodeToString(caPin)
}

// ParseToken splits a token into its secret and the CA pin (raw SHA-256).
func ParseToken(token string) (secret string, caPin []byte, err error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] == "" {
		return "", nil, ErrBadToken
	}
	pin, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(pin) != sha256.Size {
		return "", nil, ErrBadToken
	}
	return parts[1], pin, nil
}

// MatchesPin reports whether cert is the CA the pin names.
func MatchesPin(cert *x509.Certificate, pin []byte) bool {
	sum := sha256.Sum256(cert.Raw)
	return bytes.Equal(sum[:], pin)
}
