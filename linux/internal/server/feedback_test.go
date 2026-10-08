package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeedbackForwarding(t *testing.T) {
	var got []map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, m)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer gw.Close()

	s := newCompanionServer(t)
	s.FeedbackURL = gw.URL
	s.RecentLog = func() string { return "camera offline camera=Door" }
	ctx := context.Background()
	u, _ := s.Store.CreateUser(ctx, "viewer", "Viewer", "x")
	tok, _ := s.Store.CreateSession(ctx, u.ID, time.Hour)
	h := s.Handler()
	send := func(body string) int {
		r := httptest.NewRequest("POST", "/api/v1/feedback", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := send(`{"kind":"bug","message":"  "}`); c != 400 {
		t.Fatalf("empty message = %d", c)
	}
	if c := send(`{"kind":"bug","message":"recording stopped","contactEmail":"a@b.c","includeDiagnostics":true}`); c != 200 {
		t.Fatalf("bug = %d", c)
	}
	if c := send(`{"kind":"idea","message":"add zones","includeDiagnostics":true}`); c != 200 {
		t.Fatalf("idea = %d", c)
	}
	if got[0]["platform"] != "linux" || got[0]["kind"] != "bug" || got[0]["logTail"] != "camera offline camera=Door" || got[0]["contactEmail"] != "a@b.c" {
		t.Fatalf("bug payload %v", got[0])
	}
	if got[1]["kind"] != "idea" || got[1]["logTail"] != "" {
		t.Fatalf("ideas must not carry the log: %v", got[1])
	}
	for i := 0; i < 3; i++ {
		send(`{"kind":"question","message":"q"}`)
	}
	if c := send(`{"kind":"question","message":"one too many"}`); c != http.StatusTooManyRequests {
		t.Fatalf("6th submission in an hour = %d, want 429", c)
	}
}
