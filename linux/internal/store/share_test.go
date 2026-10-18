package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShareLinkHashingLookupAndClaims(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()

	l, tok, err := s.CreateShareLink(ctx, "ev-1", "for police", now.Add(time.Hour), 2, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || strings.ContainsAny(tok, "+/=") { // 32 bytes, base64url, unpadded
		t.Fatalf("token shape: %q", tok)
	}
	// Only the hash is stored.
	var stored string
	if err := s.DB.QueryRow(`SELECT token_hash FROM share_links WHERE id = ?`, l.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != HashShareToken(tok) || stored == tok || len(stored) != 64 {
		t.Fatalf("stored %q", stored)
	}

	got, err := s.ShareLinkByToken(ctx, tok)
	if err != nil || got.ID != l.ID || got.Note != "for police" || got.MaxUses != 2 {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	// The stored hash itself is not a token.
	for _, bad := range []string{"", "nope", tok[:42], tok + "x", stored} {
		if _, err := s.ShareLinkByToken(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("guess %q: %v", bad, err)
		}
	}

	// Max uses: exactly two claims succeed.
	for i := 0; i < 2; i++ {
		if err := s.ClaimShareUse(ctx, l.ID, now); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	if err := s.ClaimShareUse(ctx, l.ID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("third claim: %v", err)
	}
	if got, _ = s.ShareLinkByID(ctx, l.ID); got.Uses != 2 || got.Live(now) {
		t.Fatalf("used up: %+v", got)
	}

	// Expiry and revocation.
	l2, _, _ := s.CreateShareLink(ctx, "ev-1", "", now.Add(time.Minute), 0, "admin")
	if err := s.ClaimShareUse(ctx, l2.ID, now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired claim: %v", err)
	}
	if err := s.RevokeShareLink(ctx, l2.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimShareUse(ctx, l2.ID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked claim: %v", err)
	}
	if got, _ = s.ShareLinkByID(ctx, l2.ID); got.RevokedAt != now.Unix() || got.Live(now) {
		t.Fatalf("revoked: %+v", got)
	}
	if err := s.RevokeShareLink(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke missing: %v", err)
	}
	list, err := s.ShareLinks(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %d %v", len(list), err)
	}
}
