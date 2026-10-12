package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/push"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

// signIn returns a session cookie for a new user with the given role.
func signIn(t *testing.T, s *Server, name, role string) *http.Cookie {
	t.Helper()
	u, err := s.Store.CreateUser(context.Background(), name, role, "x")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.Store.CreateSession(context.Background(), u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: tok}
}

func doWeb(t *testing.T, h http.Handler, c *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRulesAPIPermissionsCRUDAndAudit(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	op := signIn(t, s, "op", "Operator")

	rule := `{"name":"After-hours dock","enabled":true,"kinds":["Person"],"minSeverity":"Warning","schedule":true,"fromMinute":1320,"toMinute":360,"delivery":"push","instructions":"Call the site manager."}`
	if rec := doWeb(t, h, op, "POST", "/api/v1/rules", rule); rec.Code != http.StatusForbidden {
		t.Fatalf("operator create: %d", rec.Code)
	}
	if rec := doWeb(t, h, op, "GET", "/api/v1/rules", ""); rec.Code != http.StatusOK {
		t.Fatalf("operator list: %d", rec.Code)
	}
	rec := doWeb(t, h, admin, "POST", "/api/v1/rules", rule)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := doWeb(t, h, admin, "POST", "/api/v1/rules", `{"name":"bad","schedule":true,"fromMinute":60,"toMinute":60}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid rule accepted: %d", rec.Code)
	}
	// Disable it.
	disabled := strings.Replace(rule, `"enabled":true`, `"enabled":false`, 1)
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/rules/"+created.ID, disabled); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Rules []struct {
			Enabled bool `json:"enabled"`
		} `json:"rules"`
	}
	_ = json.Unmarshal(doWeb(t, h, admin, "GET", "/api/v1/rules", "").Body.Bytes(), &list)
	if len(list.Rules) != 1 || list.Rules[0].Enabled {
		t.Fatalf("list after disable: %+v", list)
	}
	if rec := doWeb(t, h, admin, "DELETE", "/api/v1/rules/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := doWeb(t, h, admin, "DELETE", "/api/v1/rules/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
	entries, _ := s.Store.AuditLog(context.Background(), 50)
	got := map[string]bool{}
	for _, e := range entries {
		if e.Area == "Alarm Rules" {
			got[e.Action] = true
		}
	}
	if !got["Created rule"] || !got["Disabled rule"] || !got["Deleted rule"] {
		t.Fatalf("audit actions = %v", got)
	}
	if rec := doWeb(t, h, admin, "POST", "/api/v1/rules/suggested", ""); rec.Code != http.StatusCreated {
		t.Fatalf("suggested: %d", rec.Code)
	}
	if rs, _ := s.Store.AlarmRules(context.Background()); len(rs) != 4 {
		t.Fatalf("suggested rules = %d", len(rs))
	}
}

func TestInstructionsInPhoneAndDashboardAlarms(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	ctx := context.Background()
	h := s.Handler()
	admin := signIn(t, s, "admin", "Admin")
	c := addCam(t, s)
	a, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "Person detected",
		Severity: "Critical", Detail: "Door detected a person.", At: time.Now(), Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Zero rules: the phone gets an empty instructions list, as before.
	if got := s.phoneAlarm(ctx, a)["instructions"]; got == nil || len(got.([]rules.Instruction)) != 0 {
		t.Fatalf("no rules → %v", got)
	}
	rec := doWeb(t, h, admin, "POST", "/api/v1/rules", `{"name":"Dock","enabled":true,"kinds":["Person"],"delivery":"dashboard","instructions":"Call the site manager at 555-0100."}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	b, _ := json.Marshal(s.phoneAlarm(ctx, a))
	if !strings.Contains(string(b), `"instructions":[{"rule":"Dock","text":"Call the site manager at 555-0100."}]`) {
		t.Fatalf("phone payload: %s", b)
	}
	rec = doWeb(t, h, admin, "GET", "/api/v1/alarms", "")
	if !strings.Contains(rec.Body.String(), `"instructions":[{"rule":"Dock"`) {
		t.Fatalf("dashboard alarms: %s", rec.Body)
	}
}

type recordingSender struct {
	mu   sync.Mutex
	sent []push.Notification
	fail map[string]error
}

func (r *recordingSender) Name() string { return "recording" }
func (r *recordingSender) Send(_ context.Context, n push.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail[n.Token]; err != nil {
		return err
	}
	r.sent = append(r.sent, n)
	return nil
}
func (r *recordingSender) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.sent) }

const (
	phoneTok  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	phoneTok2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestPushOnlyWhenRuleSaysSo(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	fake := &recordingSender{fail: map[string]error{phoneTok2: push.ErrInvalidToken}}
	s.push.override = fake
	go s.RunRules(ctx)
	time.Sleep(20 * time.Millisecond) // hook registered
	for _, tok := range []string{phoneTok, phoneTok2} {
		d, _, err := s.Store.CreatePairedDevice(ctx, "iPhone", "admin")
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Store.SetDeviceAPNSToken(ctx, d.ID, tok)
	}
	c := addCam(t, s)
	raise := func(kind, sev string, at time.Time) {
		t.Helper()
		if _, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: kind, Title: kind + " detected",
			Severity: sev, Detail: "Door detected something.", At: at, MergeSince: at.Add(time.Second), Origin: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() {
		time.Sleep(100 * time.Millisecond)
		wctx, wc := context.WithTimeout(ctx, 3*time.Second)
		defer wc()
		_ = s.pushQueue().Wait(wctx)
	}

	// Zero rules → no push, even with push configured.
	raise("Person", "Critical", time.Now())
	settle()
	if fake.count() != 0 {
		t.Fatal("pushed with zero rules")
	}

	admin := signIn(t, s, "admin", "Admin")
	if rec := doWeb(t, s.Handler(), admin, "POST", "/api/v1/rules", `{"name":"People","enabled":true,"kinds":["Person"],"delivery":"push"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	raise("Motion", "Warning", time.Now()) // no push rule for motion
	settle()
	if fake.count() != 0 {
		t.Fatal("motion pushed")
	}
	raise("Person", "Critical", time.Now().Add(time.Second))
	settle()
	if fake.count() != 1 {
		t.Fatalf("person push count = %d", fake.count())
	}
	n := fake.sent[0]
	if n.Token != phoneTok || n.Title != "Door" || n.CameraID != c.ID || n.AlarmID == "" || n.Body != "Door detected something." {
		t.Fatalf("notification = %+v", n)
	}
	// The dead token was cleared from its device.
	toks, _ := s.Store.PushTokens(ctx)
	if len(toks) != 1 {
		t.Fatalf("tokens after 410 = %v", toks)
	}
	// A second Person alarm on the same camera within a minute is coalesced.
	raise("Person", "Critical", time.Now().Add(2*time.Second))
	settle()
	if fake.count() != 1 {
		t.Fatalf("burst not coalesced: %d", fake.count())
	}
}

func TestPushQuietHoursLetCriticalThrough(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	s.push.override = &recordingSender{}
	ctx := context.Background()
	admin := signIn(t, s, "admin", "Admin")
	doWeb(t, s.Handler(), admin, "POST", "/api/v1/rules", `{"name":"All","enabled":true,"delivery":"push"}`)
	if rec := doWeb(t, s.Handler(), admin, "PUT", "/api/v1/push", `{"quietEnabled":true,"quietFrom":1320,"quietTo":420}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	night := time.Date(2026, 10, 9, 23, 30, 0, 0, time.UTC)
	day := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	warn := &store.Alarm{CameraID: "c1", Kind: "Motion", Severity: "Warning", CreatedAt: night.Unix()}
	if ok, why := s.pushDecision(ctx, warn, night); ok || why != "quiet hours" {
		t.Fatalf("warning at night: %v %s", ok, why)
	}
	crit := &store.Alarm{CameraID: "c1", Kind: "Person", Severity: "Critical", CreatedAt: night.Unix()}
	if ok, _ := s.pushDecision(ctx, crit, night); !ok {
		t.Fatal("critical should break through quiet hours")
	}
	warn.Kind = "Vehicle"
	if ok, _ := s.pushDecision(ctx, warn, day); !ok {
		t.Fatal("daytime warning should push")
	}
}

func TestPushSettingsValidation(t *testing.T) {
	s := newCompanionServer(t)
	admin := signIn(t, s, "admin", "Admin")
	op := signIn(t, s, "op", "Operator")
	h := s.Handler()
	if rec := doWeb(t, h, op, "GET", "/api/v1/push", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("operator push settings: %d", rec.Code)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/push", `{"mode":"direct"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("direct without key: %d", rec.Code)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/push", `{"apnsKey":"garbage"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad key: %d", rec.Code)
	}
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/push", `{"apnsTeamID":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad team: %d", rec.Code)
	}
	// Relay mode registers with the relay; a relay that isn't deployed gives a clear error.
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if rec := doWeb(t, h, admin, "PUT", "/api/v1/push", `{"relayURL":"http://relay.example.com"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("plain-http relay accepted: %d", rec.Code)
	}
	rec := doWeb(t, h, admin, "PUT", "/api/v1/push", `{"relayURL":"`+gone.URL+`"}`) // 127.0.0.1 is allowed
	if rec.Code != http.StatusOK {
		t.Fatalf("relay url: %d %s", rec.Code, rec.Body)
	}
	rec = doWeb(t, h, admin, "PUT", "/api/v1/push", `{"mode":"relay"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "isn't available") {
		t.Fatalf("relay register: %d %s", rec.Code, rec.Body)
	}
	if s.pushMode() != "off" {
		t.Fatal("mode changed despite failed registration")
	}
}

func TestStorageAlarm(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	send := func(typ string, pct float64) {
		t.Helper()
		e := link.Event{ID: link.NewID(), Type: typ, ServerID: "rs1", Storage: &link.Storage{FreePct: pct, FreeBytes: 2e9}}
		if err := s.Sink().Event(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	send(link.EventStorageLow, 3)
	alarms, _ := s.Store.Alarms(ctx, true, 10)
	if len(alarms) != 1 {
		t.Fatalf("got %d alarms at 3%% free, want 1", len(alarms))
	}
	id := alarms[0].ID
	send(link.EventStorageLow, 2)
	if alarms, _ = s.Store.Alarms(ctx, true, 10); len(alarms) != 1 {
		t.Fatal("re-raised while open")
	}
	send(link.EventStorageRecovered, 20)
	a, _ := s.Store.Alarm(ctx, id)
	if a.Kind != "Storage" || a.Severity != "Critical" || len(a.ResponseLog) != 2 {
		t.Fatalf("alarm = %+v", a)
	}
}
