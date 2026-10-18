package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUserCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := newUserCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != UserCodeLength || NormalizeUserCode(c) != c {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 499 {
		t.Fatalf("only %d distinct codes in 500", len(seen))
	}
	for typed, want := range map[string]string{
		"cdfg-hjkm":   "CDFGHJKM",
		" CDFG HJKM ": "CDFGHJKM",
		"CDFGHJK":     "", // short
		"CDFG-HJKO":   "", // O is not in the alphabet
		"CDFG-HJK1":   "", // nor 1
		"CDFG-HJK8":   "", // nor 8
		"CDFG-HJKB":   "", // nor B (reads like 8)
		"CDFGHJKMN":   "", // long
	} {
		if got := NormalizeUserCode(typed); got != want {
			t.Errorf("NormalizeUserCode(%q) = %q, want %q", typed, got, want)
		}
	}
	if FormatUserCode("CDFGHJKM") != "CDFG-HJKM" {
		t.Fatal("format")
	}
}

func TestDeviceCodeLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	hash := "not-a-real-hash"
	u, err := s.CreateUser(ctx, "ana", "Operator", hash)
	if err != nil {
		t.Fatal(err)
	}

	userCode, deviceCode, dc, err := s.CreateDeviceCode(ctx, "Lobby TV", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(deviceCode, "sdc_") || dc.ExpiresAt-dc.CreatedAt != 600 {
		t.Fatalf("code %q %+v", deviceCode, dc)
	}
	// Not approved yet: nothing to redeem.
	if _, _, err := s.RedeemDeviceCode(ctx, dc.ID, "Lobby TV", time.Minute, time.Hour); !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("redeem pending: %v", err)
	}
	if _, err := s.DecideDeviceCode(ctx, userCode, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideDeviceCode(ctx, userCode, u.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decided twice: %v", err)
	}
	p, got, err := s.RedeemDeviceCode(ctx, dc.ID, "Lobby TV", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != DeviceCappedRole || !strings.HasPrefix(p.FamilyID, DeviceTokenFamilyPrefix) {
		t.Fatalf("redeemed %+v %+v", got, p)
	}
	if _, _, err := s.RedeemDeviceCode(ctx, dc.ID, "Lobby TV", time.Minute, time.Hour); !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("redeemed twice: %v", err)
	}
	// Every lookup of the token is capped; ordinary tokens are not.
	if v, err := s.AccessTokenUser(ctx, p.AccessToken); err != nil || v.Role != "Viewer" || v.ID != u.ID {
		t.Fatalf("access user %+v %v", v, err)
	}
	plain, _ := s.CreateTokenPair(ctx, u.ID, "phone", time.Minute, time.Hour)
	if v, err := s.AccessTokenUser(ctx, plain.AccessToken); err != nil || v.Role != "Operator" {
		t.Fatalf("plain user %+v %v", v, err)
	}
	_, ru, err := s.RefreshTokenPair(ctx, p.RefreshToken, time.Minute, time.Hour)
	if err != nil || ru.Role != "Viewer" {
		t.Fatalf("refresh %+v %v", ru, err)
	}
}
