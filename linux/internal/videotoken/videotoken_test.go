package videotoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func at(tm time.Time) func() time.Time { return func() time.Time { return tm } }

func base() Claims {
	return Claims{Camera: "CAM-1", Subject: "user-1", Rights: []Right{Live, Playback}, Audience: "local"}
}

func sign(t *testing.T, s *Signer, c Claims, ttl time.Duration) string {
	t.Helper()
	tok, _, err := s.Sign(c, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := NewSigner(newKey(t))
	s.Now = at(t0)
	tok, signed, err := s.Sign(base(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "v1.") || strings.Count(tok, ".") != 2 || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token shape: %q", tok)
	}
	if signed.IssuedAt != t0.Unix() || signed.Expires != t0.Add(5*time.Minute).Unix() || len(signed.ID) != 32 {
		t.Errorf("signed claims %+v", signed)
	}
	v := NewVerifier("local", s.PublicKey())
	v.Now = at(t0.Add(time.Minute))
	got, err := v.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Camera != "CAM-1" || got.Subject != "user-1" || got.Audience != "local" || got.ID != signed.ID ||
		!got.Has(Live) || !got.Has(Playback) || got.Has(Export) || !got.ExpiresAt().Equal(t0.Add(5*time.Minute)) {
		t.Errorf("verified claims %+v", got)
	}
	// Each token has its own id.
	if _, again, _ := s.Sign(base(), time.Minute); again.ID == signed.ID {
		t.Error("jti repeated")
	}
}

func TestPayloadIsTheContractJSON(t *testing.T) {
	s := NewSigner(newKey(t))
	s.Now = at(t0)
	tok := sign(t, s, base(), time.Minute)
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cam", "sub", "rights", "aud", "iat", "exp", "jti"} {
		if _, ok := m[k]; !ok {
			t.Errorf("payload lacks %q: %s", k, raw)
		}
	}
	if len(m) != 7 {
		t.Errorf("payload has extra keys: %s", raw)
	}
	// The signature covers exactly "v1.<payload>".
	sig, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[2])
	if !ed25519.Verify(s.PublicKey(), []byte(tok[:strings.LastIndex(tok, ".")]), sig) {
		t.Error("signature is not over v1.<payload>")
	}
}

func TestSignerLimits(t *testing.T) {
	s := NewSigner(newKey(t))
	for _, ttl := range []time.Duration{0, -time.Second, MaxLifetime + time.Second, time.Hour} {
		if _, _, err := s.Sign(base(), ttl); err == nil {
			t.Errorf("ttl %v accepted", ttl)
		}
	}
	if _, _, err := s.Sign(base(), MaxLifetime); err != nil {
		t.Errorf("max lifetime refused: %v", err)
	}
	for name, mod := range map[string]func(*Claims){
		"no camera":     func(c *Claims) { c.Camera = "" },
		"no subject":    func(c *Claims) { c.Subject = "" },
		"no audience":   func(c *Claims) { c.Audience = "" },
		"no rights":     func(c *Claims) { c.Rights = nil },
		"unknown right": func(c *Claims) { c.Rights = []Right{"admin"} },
	} {
		c := base()
		mod(&c)
		if _, _, err := s.Sign(c, time.Minute); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestVerifyExpiryWithSkew(t *testing.T) {
	s := NewSigner(newKey(t))
	s.Now = at(t0)
	tok := sign(t, s, base(), 5*time.Minute)
	v := NewVerifier("local", s.PublicKey())
	for _, c := range []struct {
		at   time.Time
		want error
	}{
		{t0, nil},
		{t0.Add(5*time.Minute + 29*time.Second), nil}, // within the skew
		{t0.Add(5*time.Minute + ClockSkew), ErrExpired},
		{t0.Add(time.Hour), ErrExpired},
		{t0.Add(-20 * time.Second), nil},     // verifier slightly behind
		{t0.Add(-time.Minute), ErrMalformed}, // issued "in the future"
	} {
		v.Now = at(c.at)
		if _, err := v.Verify(tok); !errors.Is(err, c.want) {
			t.Errorf("at %v: err %v, want %v", c.at.Sub(t0), err, c.want)
		}
	}
}

func TestVerifyAudienceAndKeys(t *testing.T) {
	old, cur := NewSigner(newKey(t)), NewSigner(newKey(t))
	tokOld, tokCur := sign(t, old, base(), time.Minute), sign(t, cur, base(), time.Minute)

	// Rotation: both keys accepted while both are listed.
	both := NewVerifier("local", cur.PublicKey(), old.PublicKey())
	for _, tok := range []string{tokOld, tokCur} {
		if _, err := both.Verify(tok); err != nil {
			t.Errorf("rotation: %v", err)
		}
	}
	if _, err := NewVerifier("local", cur.PublicKey()).Verify(tokOld); !errors.Is(err, ErrBadSignature) {
		t.Errorf("retired key: %v", err)
	}
	if _, err := NewVerifier("local").Verify(tokCur); !errors.Is(err, ErrBadSignature) {
		t.Errorf("no keys: %v", err)
	}
	if _, err := NewVerifier("rs-2", cur.PublicKey()).Verify(tokCur); !errors.Is(err, ErrWrongAudience) {
		t.Errorf("other audience: %v", err)
	}
}

func TestVerifyRefusesTampering(t *testing.T) {
	s := NewSigner(newKey(t))
	tok := sign(t, s, base(), time.Minute)
	v := NewVerifier("local", s.PublicKey())
	parts := strings.Split(tok, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])

	reencode := func(mod func(map[string]any)) string {
		var m map[string]any
		_ = json.Unmarshal(payload, &m)
		mod(m)
		b, _ := json.Marshal(m)
		return "v1." + base64.RawURLEncoding.EncodeToString(b) + "." + parts[2]
	}
	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	cases := map[string]struct {
		tok  string
		want error
	}{
		"other camera":        {reencode(func(m map[string]any) { m["cam"] = "CAM-2" }), ErrBadSignature},
		"more rights":         {reencode(func(m map[string]any) { m["rights"] = []string{"live", "playback", "export"} }), ErrBadSignature},
		"later expiry":        {reencode(func(m map[string]any) { m["exp"] = m["exp"].(float64) + 3600 }), ErrBadSignature},
		"payload bit":         {"v1." + flip(parts[1], 5) + "." + parts[2], ErrBadSignature},
		"signature bit":       {"v1." + parts[1] + "." + flip(parts[2], 10), ErrBadSignature},
		"other signature":     {"v1." + parts[1] + "." + strings.Split(sign(t, s, base(), time.Minute), ".")[2], ErrBadSignature},
		"version":             {"v2." + parts[1] + "." + parts[2], ErrMalformed},
		"no signature":        {"v1." + parts[1], ErrMalformed},
		"empty signature":     {"v1." + parts[1] + ".", ErrMalformed},
		"short signature":     {"v1." + parts[1] + "." + parts[2][:40], ErrMalformed},
		"padded":              {tok + "==", ErrMalformed},
		"std base64":          {"v1." + strings.NewReplacer("-", "+", "_", "/").Replace(parts[1]) + "+/." + parts[2], ErrMalformed},
		"extra segment":       {"v1.x." + parts[1] + "." + parts[2], ErrMalformed},
		"empty":               {"", ErrMalformed},
		"garbage":             {"v1.!!!.???", ErrMalformed},
		"trailing whitespace": {tok + " ", ErrMalformed},
	}
	for name, c := range cases {
		if _, err := v.Verify(c.tok); !errors.Is(err, c.want) {
			t.Errorf("%s: err %v, want %v", name, err, c.want)
		}
	}
	if _, err := v.Verify(tok); err != nil {
		t.Fatalf("original stopped verifying: %v", err)
	}
}

// A correctly signed payload that breaks the claim rules is still refused:
// the verifier doesn't trust the signer to have checked.
func TestVerifyRefusesBadClaimsEvenIfSigned(t *testing.T) {
	key := newKey(t)
	v := NewVerifier("local", key.Public().(ed25519.PublicKey))
	v.Now = at(t0)
	mint := func(c Claims) string {
		b, _ := json.Marshal(c)
		signed := "v1." + base64.RawURLEncoding.EncodeToString(b)
		return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signed)))
	}
	good := Claims{Camera: "C", Subject: "u", Rights: []Right{Live}, Audience: "local", IssuedAt: t0.Unix(), Expires: t0.Add(time.Minute).Unix(), ID: "j"}
	if _, err := v.Verify(mint(good)); err != nil {
		t.Fatalf("hand-minted good token: %v", err)
	}
	for name, mod := range map[string]func(*Claims){
		"too long-lived": func(c *Claims) { c.Expires = c.IssuedAt + int64(MaxLifetime/time.Second) + 1 },
		"exp before iat": func(c *Claims) { c.Expires = c.IssuedAt - 1 },
		"unknown right":  func(c *Claims) { c.Rights = []Right{"admin"} },
		"no rights":      func(c *Claims) { c.Rights = nil },
		"no camera":      func(c *Claims) { c.Camera = "" },
		"no jti":         func(c *Claims) { c.ID = "" },
	} {
		c := good
		mod(&c)
		if _, err := v.Verify(mint(c)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	signedJunk := "v1." + base64.RawURLEncoding.EncodeToString([]byte("not json"))
	junk := signedJunk + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signedJunk)))
	if _, err := v.Verify(junk); !errors.Is(err, ErrMalformed) {
		t.Errorf("signed non-JSON: %v", err)
	}
}

func TestPublicKeyEncoding(t *testing.T) {
	s := NewSigner(newKey(t))
	keys, err := ParsePublicKeys([]string{EncodePublicKey(s.PublicKey())})
	if err != nil || len(keys) != 1 || !keys[0].Equal(s.PublicKey()) {
		t.Fatalf("round trip: %v %v", keys, err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := ParsePublicKeys([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
