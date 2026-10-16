package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/store"
)

type fakeHomeKit struct {
	st     HomeKitStatus
	resets int
}

func (f *fakeHomeKit) Status() HomeKitStatus { return f.st }

func (f *fakeHomeKit) ResetPairings(context.Context) error {
	f.resets++
	f.st.Paired = false
	f.st.SetupCode = "111-22-333"
	return nil
}

// ensureHomeKitColumn adds cameras.homekit the way schema 4 does, so the
// test also passes on a branch where migration 4 is not merged yet.
func ensureHomeKitColumn(t *testing.T, st *store.Store) {
	t.Helper()
	_, _ = st.DB.Exec(`ALTER TABLE cameras ADD COLUMN homekit INTEGER NOT NULL DEFAULT 1`) // duplicate column = already there
}

func homekitAuditActions(t *testing.T, s *Server, area string) map[string]bool {
	t.Helper()
	entries, _ := s.Store.AuditLog(context.Background(), 50)
	got := map[string]bool{}
	for _, e := range entries {
		if e.Area == area {
			got[e.Action] = true
		}
	}
	return got
}

func TestHomeKitAbsentIsUnsupported(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/homekit", ""},
		{"PUT", "/api/v1/homekit", `{"enabled":true}`},
		{"POST", "/api/v1/homekit/reset", ""},
		{"GET", "/api/v1/homekit/cameras", ""},
	} {
		rec := doWeb(t, h, admin, c.method, c.path, c.body)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"unsupported":"multi-server"`) {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestHomeKitAPIAuth(t *testing.T) {
	s := newCompanionServer(t)
	s.HomeKit = &fakeHomeKit{}
	h := s.Handler()
	op := signIn(t, s, "op", "Operator")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/homekit", ""},
		{"PUT", "/api/v1/homekit", `{"enabled":true}`},
		{"POST", "/api/v1/homekit/reset", ""},
	} {
		if rec := doWeb(t, h, op, c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Fatalf("operator %s %s: %d", c.method, c.path, rec.Code)
		}
		if rec := doWeb(t, h, &http.Cookie{Name: sessionCookie, Value: "nope"}, c.method, c.path, c.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestHomeKitStatusEnableAndReset(t *testing.T) {
	s := newCompanionServer(t)
	fake := &fakeHomeKit{st: HomeKitStatus{Paired: true, Cameras: 2}}
	s.HomeKit = fake
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	ctx := context.Background()

	rec := doWeb(t, h, admin, "GET", "/api/v1/homekit", "")
	var got HomeKitStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !got.Paired || got.Cameras != 2 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	if rec := doWeb(t, h, admin, "PUT", "/api/v1/homekit", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: %d", rec.Code)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/homekit", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}
	if v := s.Store.Setting(ctx, "homekit_enabled", "0"); v != "1" {
		t.Fatalf("setting = %q", v)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/homekit", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	if v := s.Store.Setting(ctx, "homekit_enabled", "1"); v != "0" {
		t.Fatalf("setting after disable = %q", v)
	}

	rec = doWeb(t, h, admin, "POST", "/api/v1/homekit/reset", "")
	got = HomeKitStatus{}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Paired || got.SetupCode != "111-22-333" || fake.resets != 1 {
		t.Fatalf("reset: %d %s resets=%d", rec.Code, rec.Body, fake.resets)
	}

	actions := homekitAuditActions(t, s, "homekit")
	if !actions["Turned Apple Home on"] || !actions["Turned Apple Home off"] || !actions["Reset Apple Home pairings"] {
		t.Fatalf("audit actions = %v", actions)
	}
}

func TestHomeKitCameraFlagRoundTrip(t *testing.T) {
	s := newCompanionServer(t)
	ensureHomeKitColumn(t, s.Store)
	s.HomeKit = &fakeHomeKit{}
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	ctx := context.Background()

	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://192.168.2.221:554/stream1"})
	if err != nil {
		t.Fatal(err)
	}
	list := func() []homekitCamera {
		rec := doWeb(t, h, admin, "GET", "/api/v1/homekit/cameras", "")
		var out []homekitCamera
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		return out
	}
	if l := list(); len(l) != 1 || l[0].ID != cam.ID || !l[0].HomeKit {
		t.Fatalf("default list = %+v", l)
	}

	sub := s.Bus.Subscribe("test", 0)
	changed := make(chan struct{}, 4)
	bus.Handle(sub, TopicConfigChanged, func(ConfigChanged) { changed <- struct{}{} })

	body := `{"name":"Door","rtspURL":"rtsp://192.168.2.221:554/stream1","homekit":false}`
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/cameras/"+cam.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if l := list(); len(l) != 1 || l[0].HomeKit {
		t.Fatalf("after off = %+v", l)
	}
	<-changed // ConfigChanged fired by the normal save flow

	// Omitting the field keeps it.
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/cameras/"+cam.ID, `{"name":"Front door","rtspURL":"rtsp://192.168.2.221:554/stream1"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if l := list(); l[0].HomeKit || l[0].Name != "Front door" {
		t.Fatalf("after rename = %+v", l)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/cameras/"+cam.ID, `{"name":"Front door","rtspURL":"rtsp://192.168.2.221:554/stream1","homekit":true}`); rec.Code != http.StatusOK {
		t.Fatalf("on: %d", rec.Code)
	}
	if l := list(); !l[0].HomeKit {
		t.Fatalf("after on = %+v", l)
	}
}
