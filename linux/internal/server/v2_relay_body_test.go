package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request relayed through Sentinel Cloud arrives with an unknown body
// length (-1) even when the phone sent no body. The JSON-only guard used to
// read that as a non-JSON body and answer 415 "send JSON", so on 5G the
// iPhone camera could not get a push token and an upload could not be
// completed (WAVE23). Bodiless means: unknown length and no Content-Type.
func TestRelayedBodilessRequestsPassTheJSONGuard(t *testing.T) {
	freshPushLimits(t)
	s := newCompanionServer(t)
	h := s.Handler()
	setCloudPlan(t, s, "cloud", "active")
	admin := v2Token(t, s, "boss", "Admin")
	id, key := newPhoneCamera(t, s, admin)

	relayed := func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, io.NopCloser(strings.NewReader("")))
		if r.ContentLength != -1 {
			t.Fatalf("test setup: want an unknown length like the relay gives, got %d", r.ContentLength)
		}
		return r
	}
	do := func(r *http.Request) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	// push-token: the real key must work with no body and no Content-Type.
	r := relayed("POST", "/api/v2/cameras/"+id+"/push-token")
	r.Header.Set(pushKeyHeader, key)
	if code := do(r); code != 200 {
		t.Fatalf("relayed push-token: %d, want 200", code)
	}

	// An upload's complete has no body either: it must reach the handler
	// (404 for an unknown upload), not die in the guard.
	r = relayed("POST", "/api/v2/evidence/uploads/00000000000000000000000000000000/complete")
	r.Header.Set("Authorization", "Bearer "+admin)
	if code := do(r); code != 404 {
		t.Fatalf("relayed upload complete: %d, want 404", code)
	}

	// A declared non-JSON body is still refused.
	r = relayed("POST", "/api/v2/cameras/"+id+"/push-token")
	r.Header.Set(pushKeyHeader, key)
	r.Header.Set("Content-Type", "text/plain")
	if code := do(r); code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain body: %d, want 415", code)
	}
}
