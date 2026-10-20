package cloud

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	b := EncodeFrame(FrameData, 0x01020304, []byte("hello"))
	if !bytes.Equal(b[:5], []byte{FrameData, 1, 2, 3, 4}) {
		t.Fatalf("header % x", b[:5])
	}
	f, err := DecodeFrame(b)
	if err != nil || f.Type != FrameData || f.Stream != 0x01020304 || string(f.Payload) != "hello" {
		t.Fatalf("decode: %+v %v", f, err)
	}
	if f, err := DecodeFrame(EncodeFrame(FrameHello, 0, []byte(`{}`))); err != nil || f.Stream != 0 {
		t.Fatalf("hello: %+v %v", f, err)
	}
	if f, err := DecodeFrame(EncodeFrame(FrameEnd, 7, nil)); err != nil || len(f.Payload) != 0 {
		t.Fatalf("end: %+v %v", f, err)
	}
}

func TestFrameRejectsMalformed(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":            {},
		"short":            {FrameReq, 0, 0, 1},
		"type 0":           EncodeFrame(0, 1, nil),
		"type 11":          EncodeFrame(11, 1, nil),
		"hello on stream":  EncodeFrame(FrameHello, 3, []byte(`{}`)),
		"req on control":   EncodeFrame(FrameReq, 0, []byte(`{}`)),
		"data on control":  EncodeFrame(FrameData, 0, []byte("x")),
		"data over 1 MiB":  EncodeFrame(FrameData, 1, make([]byte, MaxDataChunk+1)),
		"end with payload": EncodeFrame(FrameEnd, 1, []byte("x")),
		"wsmsg no kind":    EncodeFrame(FrameWSMsg, 1, nil),
		"wsmsg bad kind":   EncodeFrame(FrameWSMsg, 1, []byte{3, 'x'}),
		"huge head":        EncodeFrame(FrameReq, 1, make([]byte, maxHeadJSON+1)),
	} {
		if _, err := DecodeFrame(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := DecodeFrame(EncodeFrame(FrameData, 1, make([]byte, MaxDataChunk))); err != nil {
		t.Errorf("1 MiB DATA refused: %v", err)
	}
}

func TestParseToken(t *testing.T) {
	secret := strings.Repeat("A", 42) + "_"
	kind, id, sec, ok := ParseToken("scs_k7f3q9x2ab_" + secret)
	if !ok || kind != "scs" || id != "k7f3q9x2ab" || sec != secret {
		t.Fatalf("scs: %q %q %q %v", kind, id, sec, ok)
	}
	if kind, _, _, ok := ParseToken("scb_k7f3q9x2ab_" + secret); !ok || kind != "scb" {
		t.Fatal("scb refused")
	}
	for _, bad := range []string{
		"", "scs_k7f3q9x2ab", "scx_k7f3q9x2ab_" + secret, "scs_K7F3Q9X2AB_" + secret, "scs_k7f3q9x2a_" + secret,
		"scs_k7f3q9x2ab_" + secret + "A", "scs_k7f3q9x2ab_" + strings.Repeat("A", 42) + "=", " scs_k7f3q9x2ab_" + secret,
	} {
		if _, _, _, ok := ParseToken(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRequestValidation(t *testing.T) {
	for _, p := range []string{"/", "/api/v2/cameras?x=1", "/v/tok.en/live/c/index.m3u8"} {
		if !validPath(p) {
			t.Errorf("path %q refused", p)
		}
	}
	for _, p := range []string{"", "api", "//evil.example/x", "http://x/", "/a b", "/a\r\nX: y"} {
		if validPath(p) {
			t.Errorf("path %q accepted", p)
		}
	}
	if !validHeader("x-forwarded-for", "203.0.113.9") || validHeader("bad name", "x") || validHeader("x", "a\r\nb: c") || validHeader("", "x") {
		t.Error("header validation")
	}
}

func TestWireCloseCode(t *testing.T) {
	for in, want := range map[int]int{1000: 1000, 1005: 1000, 1006: 1001, 1015: 1001, 4001: 4001, 999: 1000, 5000: 1000, 1004: 1000, 1011: 1011} {
		if got := wireCloseCode(in); got != want {
			t.Errorf("%d → %d, want %d", in, got, want)
		}
	}
}

func TestCheckURL(t *testing.T) {
	if checkURL("https://cloud.example", false, false) != nil || checkURL("wss://cloud.example/v1/relay", true, false) != nil {
		t.Fatal("secure refused")
	}
	if checkURL("http://127.0.0.1:8787", false, false) == nil || checkURL("ws://127.0.0.1/v1/relay", true, false) == nil {
		t.Fatal("plain accepted without insecure")
	}
	if checkURL("http://127.0.0.1:8787", false, true) != nil || checkURL("ws://127.0.0.1/v1/relay", true, true) != nil {
		t.Fatal("plain refused with insecure")
	}
	if checkURL("ftp://x", false, true) == nil || checkURL("https://", false, false) == nil || checkURL("https://x", true, false) == nil {
		t.Fatal("bad scheme accepted")
	}
}

func TestEntitlementFreeAndHas(t *testing.T) {
	end := "2026-11-19T00:00:00Z"
	e := Entitlement{ServerID: "k7f3q9x2ab", Plan: "cloud", Status: "active", PeriodEnd: &end, Features: []string{"relay", "backup"},
		Quotas: Quotas{BackupBytes: 10 << 30, BackupDays: 30, Servers: 1}, RelayHost: "k7f3q9x2ab-cloud.example"}
	if !e.Has(FeatureRelay) || e.Has(FeaturePush) {
		t.Fatal("Has")
	}
	f := e.free()
	if f.Plan != "free" || f.Status != "inactive" || f.PeriodEnd != nil || len(f.Features) != 0 || f.Features == nil || f.Quotas != (Quotas{}) || f.RelayHost != e.RelayHost {
		t.Fatalf("free: %+v", f)
	}
}

func TestClipCursor(t *testing.T) {
	at := time.Unix(1_760_000_000, 0)
	got, id := parseClipCursor(clipCursor(at, "EV-1"))
	if !got.Equal(at) || id != "EV-1" {
		t.Fatalf("%v %q", got, id)
	}
	if got, id := parseClipCursor("junk"); !got.IsZero() || id != "" {
		t.Fatal("junk cursor")
	}
}
