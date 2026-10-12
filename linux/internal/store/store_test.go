package store

import (
	"context"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSealRoundTripAndTamper(t *testing.T) {
	s := open(t)
	sealed, err := s.Seal("p@ss!word")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Unseal(sealed); err != nil || got != "p@ss!word" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if _, err := s.Unseal(sealed[:len(sealed)-4] + "AAAA"); err == nil {
		t.Fatal("tampered ciphertext must not decrypt")
	}
}

func TestInstallKeyPersists(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	sealed, _ := a.Seal("secret")
	a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got, _ := b.Unseal(sealed); got != "secret" {
		t.Fatal("reopened store must decrypt with the same install key")
	}
}

func TestCameraPasswordKeptUnlessProvided(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	pw := "first"
	c, err := s.SaveCamera(ctx, "", CameraInput{Name: "Dock", RTSPURL: "rtsp://10.0.0.5/s1", Username: "admin", Password: &pw, Recording: true})
	if err != nil || !c.HasPassword {
		t.Fatalf("create: %v %+v", err, c)
	}
	// Edit without a password field keeps the stored one.
	c, _ = s.SaveCamera(ctx, c.ID, CameraInput{Name: "Dock 2", RTSPURL: "rtsp://10.0.0.5/s1", Username: "admin"})
	if got, _ := s.CameraPassword(c); got != "first" || c.Name != "Dock 2" {
		t.Fatalf("password after edit = %q name %q", got, c.Name)
	}
	empty := ""
	c, _ = s.SaveCamera(ctx, c.ID, CameraInput{Name: "Dock 2", RTSPURL: "rtsp://10.0.0.5/s1", Password: &empty})
	if c.HasPassword {
		t.Fatal("empty password should clear it")
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, a := range []string{"Signed in", "Added camera", "Deleted camera"} {
		if err := s.Audit(ctx, "Eric", "Test", a, "detail"); err != nil {
			t.Fatal(err)
		}
	}
	if n, broken, _ := s.VerifyAudit(ctx); n != 3 || broken != "" {
		t.Fatalf("intact chain: checked %d broken %q", n, broken)
	}
	// Someone edits the database directly.
	if _, err := s.DB.Exec(`UPDATE audit SET detail = 'nothing to see' WHERE action = 'Deleted camera'`); err != nil {
		t.Fatal(err)
	}
	if _, broken, _ := s.VerifyAudit(ctx); broken == "" {
		t.Fatal("edited entry must break the chain")
	}
}

func TestAuditChainDetectsDeletion(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, a := range []string{"a", "b", "c"} {
		_ = s.Audit(ctx, "Eric", "Test", a, "")
	}
	_, _ = s.DB.Exec(`DELETE FROM audit WHERE action = 'b'`)
	if _, broken, _ := s.VerifyAudit(ctx); broken == "" {
		t.Fatal("deleted entry must break the chain")
	}
}

func TestSessionsExpireAndHashTokens(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "Eric", "Admin", "hash")
	tok, _ := s.CreateSession(ctx, u.ID, -1) // already expired
	if _, err := s.SessionUser(ctx, tok); err == nil {
		t.Fatal("expired session must not authenticate")
	}
	var stored string
	_ = s.DB.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == tok {
		t.Fatal("raw session token must not be stored")
	}
}

func TestTokenPairRotationAndReuse(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "a", "Admin", "x")
	p, err := st.CreateTokenPair(ctx, u.ID, "test", time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.AccessTokenUser(ctx, p.AccessToken); err != nil || got.ID != u.ID {
		t.Fatalf("access: %v %v", got, err)
	}
	p2, got, err := st.RefreshTokenPair(ctx, p.RefreshToken, time.Minute, time.Hour)
	if err != nil || got.ID != u.ID || p2.FamilyID != p.FamilyID {
		t.Fatalf("refresh: %v %v", got, err)
	}
	if _, err := st.AccessTokenUser(ctx, p.AccessToken); err == nil {
		t.Fatal("old access token still valid")
	}
	if _, _, err := st.RefreshTokenPair(ctx, p.RefreshToken, time.Minute, time.Hour); err != ErrTokenReuse {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := st.AccessTokenUser(ctx, p2.AccessToken); err == nil {
		t.Fatal("family survived reuse")
	}
	if _, _, err := st.RefreshTokenPair(ctx, "srt_unknown", time.Minute, time.Hour); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
	// Expired access tokens don't resolve; deleting the user drops tokens.
	p3, _ := st.CreateTokenPair(ctx, u.ID, "", -time.Second, time.Hour)
	if _, err := st.AccessTokenUser(ctx, p3.AccessToken); err == nil {
		t.Fatal("expired access token accepted")
	}
	_ = st.DeleteUser(ctx, u.ID)
	if _, _, err := st.RefreshTokenPair(ctx, p3.RefreshToken, time.Minute, time.Hour); err != ErrNotFound {
		t.Fatalf("token outlived its user: %v", err)
	}
}
