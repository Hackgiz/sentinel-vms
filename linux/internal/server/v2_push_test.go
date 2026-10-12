package server

import (
	"context"
	"strings"
	"testing"
)

func TestV2PushRegistration(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	tok := v2SignIn(t, s, "viewer1", "Viewer")["accessToken"].(string)
	apns := strings.Repeat("ab12", 16)

	if code, m := v2Call(t, h, "PUT", "/api/v2/push/registration", "", `{"apnsToken":"`+apns+`"}`); code != 401 {
		t.Fatalf("anonymous: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/push/registration", tok, `{"apnsToken":"nothex!"}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("bad token: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/push/registration", tok, `{"apnsToken":"`+apns+`","deviceName":"Test phone"}`); code != 204 {
		t.Fatalf("register: %d %v", code, m)
	}
	toks, err := s.Store.PushTokens(ctx)
	if err != nil || len(toks) != 1 {
		t.Fatalf("push tokens %v %v", toks, err)
	}
	devs, _ := s.Store.PairedDevices(ctx)
	if len(devs) != 1 || devs[0].PairedBy != "viewer1" || devs[0].Name != "Test phone" || !devs[0].HasPush {
		t.Fatalf("device %+v", devs)
	}

	// Idempotent; a second user signing in on the same phone takes it over.
	if code, _ := v2Call(t, h, "PUT", "/api/v2/push/registration", tok, `{"apnsToken":"`+apns+`"}`); code != 204 {
		t.Fatalf("again: %d", code)
	}
	tok2 := v2SignIn(t, s, "viewer2", "Viewer")["accessToken"].(string)
	if code, _ := v2Call(t, h, "PUT", "/api/v2/push/registration", tok2, `{"apnsToken":"`+apns+`"}`); code != 204 {
		t.Fatalf("takeover: %d", code)
	}
	devs, _ = s.Store.PairedDevices(ctx)
	if len(devs) != 1 || devs[0].PairedBy != "viewer2" {
		t.Fatalf("after takeover %+v", devs)
	}

	// Only the owner can remove it.
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/push/registration?apnsToken="+apns, tok, ""); code != 204 {
		t.Fatalf("foreign delete: %d", code)
	}
	if devs, _ = s.Store.PairedDevices(ctx); len(devs) != 1 {
		t.Fatalf("foreign delete removed it: %+v", devs)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/push/registration?apnsToken="+apns, tok2, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if devs, _ = s.Store.PairedDevices(ctx); len(devs) != 0 {
		t.Fatalf("still registered: %+v", devs)
	}
}
