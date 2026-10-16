package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tasks"
)

func TestSiteInfoRoundTrip(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")

	if code, _ := v2Call(t, h, "GET", "/api/v2/site", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	code, m := v2Call(t, h, "GET", "/api/v2/site", viewer, "")
	if code != 200 || m["name"] != "" {
		t.Fatalf("viewer read: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/site", viewer, `{"name":"x","description":"","contact":""}`); code != 403 {
		t.Fatalf("viewer write: %d", code)
	}
	code, m = v2Call(t, h, "PUT", "/api/v2/site", admin, `{"name":"  Depot ","description":"Main yard","contact":"ops@example.com"}`)
	if code != 200 || m["name"] != "Depot" || m["contact"] != "ops@example.com" {
		t.Fatalf("put: %d %v", code, m)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/site", viewer, "")
	if m["name"] != "Depot" || m["description"] != "Main yard" {
		t.Fatalf("get after put: %v", m)
	}
	if got := s.Store.Setting(context.Background(), "site.name", ""); got != "Depot" {
		t.Fatalf("kv key site.name = %q", got)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/site", admin, `{"name":"`+strings.Repeat("a", 81)+`","description":"","contact":""}`); code != 400 {
		t.Fatalf("long name: %d", code)
	}
}

func TestThresholdsDefaultsAndValidation(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	sup := v2Token(t, s, "sup", "Supervisor")

	code, m := v2Call(t, h, "GET", "/api/v2/system/thresholds", admin, "")
	cpu, _ := m["cpu"].(map[string]any)
	if code != 200 || cpu["amber"] != float64(80) || cpu["red"] != float64(95) {
		t.Fatalf("defaults: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/system/thresholds", sup, ""); code != 200 {
		t.Fatalf("supervisor read: %d", code)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/system/thresholds", sup, `{"cpu":{"amber":70,"red":90},"mem":{"amber":60,"red":85},"disk":{"amber":75,"red":98}}`); code != 403 {
		t.Fatalf("supervisor write: %d", code)
	}
	body := `{"cpu":{"amber":70,"red":90},"mem":{"amber":60,"red":85},"disk":{"amber":75,"red":98}}`
	if code, _ := v2Call(t, h, "PUT", "/api/v2/system/thresholds", admin, body); code != 200 {
		t.Fatalf("put: %d", code)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/system/thresholds", admin, "")
	if disk := m["disk"].(map[string]any); disk["amber"] != float64(75) || disk["red"] != float64(98) {
		t.Fatalf("after put: %v", m)
	}
	if got := s.Store.Setting(context.Background(), "monitor.mem.red", ""); got != "85" {
		t.Fatalf("kv key monitor.mem.red = %q", got)
	}
	for _, bad := range []string{
		`{"cpu":{"amber":90,"red":80},"mem":{"amber":60,"red":85},"disk":{"amber":75,"red":98}}`,
		`{"cpu":{"amber":70,"red":101},"mem":{"amber":60,"red":85},"disk":{"amber":75,"red":98}}`,
		`{"cpu":{"amber":0,"red":90},"mem":{"amber":60,"red":85},"disk":{"amber":75,"red":98}}`,
	} {
		if code, _ := v2Call(t, h, "PUT", "/api/v2/system/thresholds", admin, bad); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	// A corrupt stored value falls back to the default.
	_ = s.Store.SetSetting(context.Background(), "monitor.cpu.amber", "junk")
	_, m = v2Call(t, h, "GET", "/api/v2/system/thresholds", admin, "")
	if m["cpu"].(map[string]any)["amber"] != float64(80) {
		t.Fatalf("corrupt value: %v", m["cpu"])
	}
}

func TestTasksEndpoint(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	op := v2Token(t, s, "op", "Operator")
	if code, _ := v2Call(t, h, "GET", "/api/v2/system/tasks", op, ""); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	task := tasks.Default.Start(tasks.KindDiscovery, "endpoint test scan")
	defer task.Finish()
	task.Progress(30)
	code, m := v2Call(t, h, "GET", "/api/v2/system/tasks", admin, "")
	if code != 200 {
		t.Fatalf("tasks: %d", code)
	}
	var found map[string]any
	for _, it := range items(t, m) {
		if it["title"] == "endpoint test scan" {
			found = it
		}
	}
	if found == nil || found["state"] != "running" || found["progress"] != float64(30) {
		t.Fatalf("task not listed: %v", m)
	}
}

func TestLogFiltering(t *testing.T) {
	text := strings.Join([]string{
		`time=T level=DEBUG msg=a`,
		`time=T level=INFO msg=b`,
		`time=T level=WARN msg=c`,
		`time=T level=ERROR msg=d`,
		`a line with no level`,
	}, "\n")
	if got := filterLogLines(text, "", 100); len(got) != 5 {
		t.Fatalf("all: %v", got)
	}
	got := filterLogLines(text, "warn", 100)
	if len(got) != 2 || got[0].Level != "WARN" || got[1].Level != "ERROR" {
		t.Fatalf("warn+: %v", got)
	}
	if got := filterLogLines(text, "info", 2); len(got) != 2 || got[1].Text != "a line with no level" {
		t.Fatalf("limit keeps the newest: %v", got)
	}
	if got := filterLogLines("", "", 5); got == nil || len(got) != 0 {
		t.Fatalf("empty must be a non-nil slice: %#v", got)
	}
}

func TestSystemLogsEndpoint(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	var lines []string
	for i := 0; i < 2500; i++ { // more than the ring would ever hand over
		lines = append(lines, "time=T level=INFO msg=line")
	}
	lines = append(lines, "time=T level=ERROR msg=boom")
	s.Logs = func(n int) string {
		if n < len(lines) {
			return strings.Join(lines[len(lines)-n:], "\n")
		}
		return strings.Join(lines, "\n")
	}
	admin := v2Token(t, s, "admin", "Admin")
	op := v2Token(t, s, "op", "Operator")
	if code, _ := v2Call(t, h, "GET", "/api/v2/system/logs", op, ""); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	code, m := v2Call(t, h, "GET", "/api/v2/system/logs?level=error", admin, "")
	got, _ := m["lines"].([]any)
	if code != 200 || len(got) != 1 || got[0].(map[string]any)["level"] != "ERROR" {
		t.Fatalf("level filter: %d %v", code, m)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/system/logs?limit=10", admin, "")
	if got, _ := m["lines"].([]any); len(got) != 10 {
		t.Fatalf("limit: %d", len(got))
	}
	for _, q := range []string{"?limit=0", "?limit=2001", "?level=loud", "?format=xml"} {
		if code, _ := v2Call(t, h, "GET", "/api/v2/system/logs"+q, admin, ""); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/system/logs?server=nope", admin, ""); code != 404 {
		t.Fatalf("unknown server: %d", code)
	}

	r := httptest.NewRequest("GET", "/api/v2/system/logs?format=text&level=error", nil)
	r.Header.Set("Authorization", "Bearer "+admin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || strings.TrimSpace(w.Body.String()) != "time=T level=ERROR msg=boom" {
		t.Fatalf("text download: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestConfigReportRedaction(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "admin", "Admin")
	op := v2Token(t, s, "op", "Operator")

	const (
		camPass   = "CamPassw0rd-XYZZY"
		camUser   = "camuser-xyzzy"
		urlSecret = "urltoken-XYZZY"
		hookSec   = "whsec-XYZZY"
		hookTok   = "hooktoken-XYZZY"
	)
	pw := camPass
	cam, err := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Gate", RTSPURL: "rtsp://" + camUser + ":" + urlSecret + "@10.0.0.7/stream?key=" + urlSecret,
		Username: camUser, Password: &pw, Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.ExecContext(ctx, `INSERT INTO webhooks(id, name, url, secret, events, enabled, created_at) VALUES('w1','Chat',?,?,'["alarm"]',1,1)`,
		"https://hooks.example.com/services/"+hookTok, hookSec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.ExecContext(ctx, `INSERT INTO time_profiles(id, name, body, created_at) VALUES('p1','Nights','{"weekly":[]}',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.ExecContext(ctx, `INSERT INTO roles(id, name, description, permissions, builtin, created_at) VALUES('r1','Guard','night guard','["view_live"]',0,1)`); err != nil {
		t.Fatal(err)
	}
	// Users: the sign-in helper stores real bcrypt hashes.
	var hashes []string
	users, _ := s.Store.Users(ctx)
	for _, u := range users {
		hashes = append(hashes, u.PasswordHash)
	}
	if len(hashes) == 0 || hashes[0] == "" {
		t.Fatal("fixture has no password hashes to look for")
	}

	if code, _ := v2Call(t, h, "GET", "/api/v2/system/config-report", op, ""); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	for _, format := range []string{"", "?format=html"} {
		r := httptest.NewRequest("GET", "/api/v2/system/config-report"+format, nil)
		r.Header.Set("Authorization", "Bearer "+admin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("report%s: %d %s", format, w.Code, w.Body.String())
		}
		body := w.Body.String()
		secrets := append([]string{camPass, camUser, urlSecret, hookSec, hookTok, "$2a$", "$2b$", "password_enc", "passwordHash", cam.ID + "@"}, hashes...)
		for _, sec := range secrets {
			if strings.Contains(body, sec) {
				t.Errorf("report%s leaks %q", format, sec)
			}
		}
		for _, want := range []string{"Gate", "10.0.0.7", "Chat", "hooks.example.com", "Nights", "Guard"} {
			if !strings.Contains(body, want) {
				t.Errorf("report%s lacks %q", format, want)
			}
		}
	}
	_, m := v2Call(t, h, "GET", "/api/v2/system/config-report", admin, "")
	cams := m["cameras"].([]any)
	if c := cams[0].(map[string]any); c["hasCredentials"] != true || c["mainStream"] != "rtsp://10.0.0.7/stream" {
		t.Fatalf("camera entry: %v", c)
	}
	if wh := m["webhooks"].([]any)[0].(map[string]any); wh["hasSecret"] != true || wh["endpoint"] != "https://hooks.example.com" {
		t.Fatalf("webhook entry: %v", wh)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/system/config-report?format=pdf", admin, ""); code != 400 {
		t.Fatalf("bad format: %d", code)
	}
}
