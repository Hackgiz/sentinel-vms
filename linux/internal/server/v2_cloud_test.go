package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/cloud"
	"sentinel-linux/internal/store"
)

const (
	testCloudToken  = "scs_k7f3q9x2ab_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA_"
	testBackupToken = "scb_k7f3q9x2ab_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

// fakeControlPlane is Sentinel Cloud's control plane on 127.0.0.1:0.
type fakeControlPlane struct {
	srv *httptest.Server

	mu        sync.Mutex
	approve   bool // poll answers the token (else 202)
	issued    bool
	deletes   int
	registers []string // Authorization headers of /push/v1/register
	features  []string
	plan      string
	rejectEnt bool // GET /v1/entitlement answers 401
	entCalls  int

	// Linking with an account (wave 13).
	jwt         string         // the session token /v1/link/approve accepts
	approveAuth []string       // Authorization headers approve got
	approveFail int            // non-zero: approve answers this status …
	approveBody map[string]any // … with this body
	config      map[string]any // GET /v1/config; nil = 404
	configGate  chan struct{}  // non-nil: GET /v1/config waits for it to close
	configCalls int
}

// testAccountJWT stands for a Supabase session token (never a real one).
const testAccountJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEifQ.c2lnbmF0dXJlLWZvci1zZXJ2ZXItdGVzdHM"

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{approve: true, features: []string{"relay", "push", "backup"}, plan: "cloud"}
	ent := func() cloud.Entitlement {
		end := "2026-11-19T00:00:00Z"
		return cloud.Entitlement{ServerID: "k7f3q9x2ab", Plan: f.plan, Status: "active", PeriodEnd: &end, Features: f.features,
			Quotas: cloud.Quotas{BackupBytes: 10 << 30, BackupDays: 30, Servers: 1}, RelayHost: "k7f3q9x2ab-cloud.example",
			AccountEmailMasked: "e***@example.com", PushRelayURL: f.srv.URL, BackupURL: f.srv.URL + "/v1/backup"}
	}
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testCloudToken {
				reply(w, 401, map[string]string{"error": "unknown token"})
				return
			}
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/link/start", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"deviceCode": "dev-1", "userCode": "ABCD-EFGH", "verifyURL": "https://site.example/cloud?code=ABCD-EFGH",
			"expiresAt": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "interval": 5})
	})
	mux.HandleFunc("POST /v1/link/poll", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.issued:
			reply(w, 404, map[string]string{"error": "expired"})
		case !f.approve:
			reply(w, 202, map[string]bool{"pending": true})
		default:
			f.issued = true
			reply(w, 200, map[string]any{"serverToken": testCloudToken, "serverId": "k7f3q9x2ab", "entitlement": ent()})
		}
	})
	mux.HandleFunc("POST /v1/link/approve", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		f.approveAuth = append(f.approveAuth, auth)
		switch {
		case f.approveFail != 0:
			reply(w, f.approveFail, f.approveBody)
		case f.jwt == "" || auth != "Bearer "+f.jwt:
			reply(w, 401, map[string]string{"error": "Sign in again: your session has expired."})
		case in["userCode"] != "ABCD-EFGH":
			reply(w, 404, map[string]string{"error": "That code is unknown or has expired."})
		default:
			f.approve = true
			reply(w, 200, map[string]string{"serverName": "Office"})
		}
	})
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.configCalls++
		gate := f.configGate
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.config == nil {
			reply(w, 404, map[string]string{"error": "not found"})
			return
		}
		reply(w, 200, f.config)
	})
	mux.HandleFunc("GET /v1/entitlement", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.entCalls++
		if f.rejectEnt {
			reply(w, 401, map[string]string{"error": "unknown token"})
			return
		}
		reply(w, 200, ent())
	}))
	mux.HandleFunc("DELETE /v1/servers/self", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deletes++
		f.mu.Unlock()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /v1/backup/tokens", authed(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]string{"token": testBackupToken, "expiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	}))
	mux.HandleFunc("GET /v1/backup/usage", authed(func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"bytesUsed": 1234, "objects": 2, "bytesQuota": 10 << 30, "days": 30, "oldestAt": nil})
	}))
	mux.HandleFunc("POST /push/v1/register", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.registers = append(f.registers, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testCloudToken {
			reply(w, 401, map[string]string{"error": "server token required"})
			return
		}
		reply(w, 200, map[string]string{"installId": "k7f3q9x2ab", "secret": "push-secret-for-tests"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// withCloud attaches a client for f to s, as StartCloud does.
func withCloud(t *testing.T, s *Server, f *fakeControlPlane) *cloud.Client {
	t.Helper()
	c := &cloud.Client{BaseURL: f.srv.URL, Store: s.Store, Version: "test", Platform: "linux", AllowInsecure: true, PollEvery: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.runCloud(ctx, c)
	return c
}

func waitCloud(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestV2CloudStatusUnlinked(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	if code, _ := v2Call(t, h, "GET", "/api/v2/cloud", "", ""); code != 401 {
		t.Fatalf("anonymous %d", code)
	}
	// Viewers read the status lines too (WAVE13); changing anything stays changeSettings.
	if code, m := v2Call(t, h, "GET", "/api/v2/cloud", viewer, ""); code != 200 || m["linked"] != false || m["plan"] != "free" || !hasNull(m, "signIn") {
		t.Fatalf("viewer %d %v", code, m)
	}
	for _, op := range [][2]string{{"PUT", "/api/v2/cloud"}, {"POST", "/api/v2/cloud/link"}, {"DELETE", "/api/v2/cloud/link"}, {"POST", "/api/v2/cloud/refresh"}} {
		body := ""
		if op[0] == "PUT" {
			body = `{"backup":false}`
		}
		if code, _ := v2Call(t, h, op[0], op[1], viewer, body); code != 403 {
			t.Fatalf("viewer %s %s: %d", op[0], op[1], code)
		}
	}
	code, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
	if code != 200 || m["linked"] != false || m["serverId"] != "" || m["plan"] != "free" || m["status"] != "inactive" || m["periodEnd"] != nil {
		t.Fatalf("status %d %v", code, m)
	}
	if f, ok := m["features"].([]any); !ok || len(f) != 0 {
		t.Fatalf("features %v", m["features"])
	}
	if _, ok := m["link"]; ok {
		t.Fatal("link without a pending code")
	}
	if m["push"].(map[string]any)["state"] != "off" || m["remote"].(map[string]any)["state"] != "off" {
		t.Fatalf("push/remote %v", m)
	}
	// Without a client (no control plane configured) linking is unavailable.
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, ""); code != 503 || errCode(m) != "unavailable" {
		t.Fatalf("link without client %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cloud/remote", viewer, ""); code != 200 || m["remoteURL"] != nil || m["via"] != nil {
		t.Fatalf("remote %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/cloud/backup-access", viewer, ""); code != 404 {
		t.Fatalf("backup access unlinked %d", code)
	}
	// The preferences can be set before linking.
	if code, m := v2Call(t, h, "PUT", "/api/v2/cloud", admin, `{"backup":false}`); code != 200 || m["backup"].(map[string]any)["enabled"] != false {
		t.Fatalf("put %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/cloud", admin, `{"nope":1}`); code != 400 {
		t.Fatalf("unknown field %d", code)
	}
}

func TestV2CloudLinkUseAndUnlink(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.approve = false
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	ctx := context.Background()

	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/link", viewer, ""); code != 403 {
		t.Fatalf("viewer link %d", code)
	}
	code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, "")
	link, _ := m["link"].(map[string]any)
	if code != 200 || m["linked"] != false || link["userCode"] != "ABCD-EFGH" || link["verifyURL"] != "https://site.example/cloud?code=ABCD-EFGH" || link["expiresAt"] == "" {
		t.Fatalf("link %d %v", code, m)
	}
	// The pending code is for changeSettings only: anyone holding it could
	// approve this server into their own account.
	if _, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); m["link"] == nil {
		t.Fatalf("admin doesn't see the pending code: %v", m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/cloud", viewer, ""); code != 200 || m["link"] != nil {
		t.Fatalf("viewer sees the pending code: %d %v", code, m)
	}
	f.mu.Lock()
	f.approve = true
	f.mu.Unlock()
	waitCloud(t, "linked", func() bool { _, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); return m["linked"] == true })

	_, m = v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
	if m["serverId"] != "k7f3q9x2ab" || m["plan"] != "cloud" || m["periodEnd"] != "2026-11-19T00:00:00Z" || m["accountEmailMasked"] != "e***@example.com" {
		t.Fatalf("linked status %v", m)
	}
	if _, ok := m["link"]; ok {
		t.Fatal("link still pending after linking")
	}
	if r := m["remote"].(map[string]any); r["url"] != "https://k7f3q9x2ab-cloud.example" || r["enabled"] != true {
		t.Fatalf("remote %v", r)
	}
	if !auditActions(t, s, "Settings")["Linked Sentinel Cloud"] {
		t.Fatal("link not audited")
	}
	// Push moved to Sentinel Cloud and registered with the server token.
	if s.pushMode() != "cloud" {
		t.Fatalf("push mode %q", s.pushMode())
	}
	waitCloud(t, "push registered", func() bool { return s.Store.Setting(ctx, cloudPushInstallSetting, "") == "k7f3q9x2ab" })
	f.mu.Lock()
	regs := append([]string(nil), f.registers...)
	f.mu.Unlock()
	if len(regs) != 1 || regs[0] != "Bearer "+testCloudToken {
		t.Fatalf("register auth %v", regs)
	}
	if raw := s.Store.Setting(ctx, cloudPushSecretSetting, ""); raw == "" || strings.Contains(raw, "push-secret") {
		t.Fatal("push secret not sealed")
	}
	if s.pushSender() == nil {
		t.Fatal("no push sender in mode cloud")
	}
	if ps := s.pushStatus(ctx); ps.Mode != "cloud" || !ps.CloudReady || !ps.Ready {
		t.Fatalf("v1 push status %+v", ps)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
	if m["push"].(map[string]any)["state"] != "cloud" {
		t.Fatalf("push state %v", m["push"])
	}

	// Phones: the stable address, also in pairing and the companion header.
	code, m = v2Call(t, h, "GET", "/api/v2/cloud/remote", viewer, "")
	if code != 200 || m["remoteURL"] != "https://k7f3q9x2ab-cloud.example" || m["via"] != "cloud" {
		t.Fatalf("remote %d %v", code, m)
	}
	if s.remoteURL() != "https://k7f3q9x2ab-cloud.example" {
		t.Fatalf("remoteURL() %q", s.remoteURL())
	}
	code, m = v2Call(t, h, "GET", "/api/v2/cloud/backup-access", viewer, "")
	if code != 200 || m["token"] != testBackupToken || m["serverId"] != "k7f3q9x2ab" || m["baseURL"] != f.srv.URL+"/v1/backup" || m["expiresAt"] == "" {
		t.Fatalf("backup access %d %v", code, m)
	}

	// Toggles, audited.
	code, m = v2Call(t, h, "PUT", "/api/v2/cloud", admin, `{"remote":false}`)
	if code != 200 || m["remote"].(map[string]any)["enabled"] != false || m["remote"].(map[string]any)["state"] != "off" {
		t.Fatalf("remote off %d %v", code, m)
	}
	if _, m := v2Call(t, h, "GET", "/api/v2/cloud/remote", viewer, ""); m["remoteURL"] != nil {
		t.Fatalf("remote URL while off %v", m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/cloud", admin, `{"backup":false}`); code != 200 {
		t.Fatal("backup off")
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/cloud/backup-access", viewer, ""); code != 404 {
		t.Fatalf("backup access while off %d", code)
	}
	acts := auditActions(t, s, "Settings")
	if !acts["Turned off remote access"] || !acts["Turned off offsite backup"] {
		t.Fatalf("toggles not audited: %v", acts)
	}
	// Same value again: no change, no audit noise.
	if code, _ := v2Call(t, h, "PUT", "/api/v2/cloud", admin, `{"remote":false}`); code != 200 {
		t.Fatal("repeat put")
	}

	// Unlink: tells the control plane, forgets locally, push back to off.
	code, m = v2Call(t, h, "DELETE", "/api/v2/cloud/link", admin, "")
	if code != 200 || m["linked"] != false || m["serverId"] != "" {
		t.Fatalf("unlink %d %v", code, m)
	}
	f.mu.Lock()
	deletes := f.deletes
	f.mu.Unlock()
	if deletes != 1 || !auditActions(t, s, "Settings")["Unlinked Sentinel Cloud"] {
		t.Fatalf("unlink: %d deletes, audit %v", deletes, auditActions(t, s, "Settings"))
	}
	if s.pushMode() != "off" || s.Store.Setting(ctx, cloudPushInstallSetting, "") != "" {
		t.Fatal("push still on Sentinel Cloud after unlinking")
	}
	if s.remoteURL() != "" {
		t.Fatal("remote URL after unlinking")
	}
}

func TestV2CloudRejectedTokenKeepsTheLink(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	ctx := context.Background()
	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, ""); code != 200 {
		t.Fatal("link")
	}
	waitCloud(t, "linked", func() bool { _, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); return m["linked"] == true })

	// The cached entitlement is 10 minutes old, so reading the status
	// refreshes it, and the control plane answers 401.
	f.mu.Lock()
	f.rejectEnt = true
	f.mu.Unlock()
	_ = s.Store.SetSetting(ctx, cloud.KeyEntitlementAt, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	code, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
	r, _ := m["remote"].(map[string]any)
	if code != 200 || m["linked"] != true || m["serverId"] != "k7f3q9x2ab" || m["status"] != "inactive" || m["plan"] != "cloud" ||
		len(m["features"].([]any)) != 0 || r["state"] != "inactive" || r["error"] != cloud.RejectedMessage {
		t.Fatalf("rejected token: %d %v", code, m)
	}
	if m["push"].(map[string]any)["state"] != "off" {
		t.Fatalf("push with a rejected token %v", m["push"])
	}
	if s.remoteURL() != "" || auditActions(t, s, "Settings")["Unlinked Sentinel Cloud"] {
		t.Fatal("rejected token treated as an unlink")
	}
	if s.Store.Setting(ctx, cloud.KeyToken, "") == "" {
		t.Fatal("token dropped")
	}
}

func TestV2CloudCancelPendingLink(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.approve = false
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, ""); code != 200 {
		t.Fatal("link")
	}
	code, m := v2Call(t, h, "DELETE", "/api/v2/cloud/link", admin, "")
	if _, pending := m["link"]; code != 200 || pending || m["linked"] != false {
		t.Fatalf("cancel %d %v", code, m)
	}
	f.mu.Lock()
	deletes := f.deletes
	f.mu.Unlock()
	if deletes != 0 {
		t.Fatal("DELETE /v1/servers/self without a server token")
	}
	if auditActions(t, s, "Settings")["Unlinked Sentinel Cloud"] {
		t.Fatal("cancelling a code audited as an unlink")
	}
}

func TestV2CloudPushModeNeedsPlan(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "admin", "Admin")
	_ = admin
	f := newFakeControlPlane(t)
	f.features = []string{"relay"}
	withCloud(t, s, f)
	if s.cloudPushReady(context.Background()) || s.pushStatus(context.Background()).CloudReady {
		t.Fatal("ready without a link")
	}
	// v1 push settings refuse mode cloud until the plan includes push.
	sess := adminSession(t, s, h)
	req := httptest.NewRequest("PUT", "/api/v1/push", strings.NewReader(`{"mode":"cloud"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sess)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Sentinel Cloud") {
		t.Fatalf("mode cloud without a plan: %d %s", w.Code, w.Body)
	}
}

// adminSession signs a new Admin in on v1 and returns the session cookie.
func adminSession(t *testing.T, s *Server, h http.Handler) *http.Cookie {
	t.Helper()
	v2Token(t, s, "boss", "Admin") // creates the user with the shared test password
	req := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"name":"boss","password":"correct horse battery"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session: %d %s", w.Code, w.Body)
	return nil
}

func TestRemoteHandlerServesOnlyRemoteRoutes(t *testing.T) {
	s := newCompanionServer(t)
	h := s.RemoteHandler()
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := get("/health"); w.Code != 200 || w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("health %d %v", w.Code, w.Header())
	}
	if w := get("/api/v2/openapi.yaml"); w.Code != 200 {
		t.Fatalf("spec %d", w.Code)
	}
	for _, p := range []string{"/", "/index.html", "/api/v1/status", "/api/v1/cameras", "/live/c1/index.m3u8", "/recordings/c1/x.mp4", "/assets/app.js"} {
		if w := get(p); w.Code != 404 {
			t.Errorf("%s: %d (want 404)", p, w.Code)
		}
	}
	if w := get("/cameras"); w.Code != 401 {
		t.Fatalf("phone API %d", w.Code)
	}
	if w := get("/api/v2/cameras"); w.Code != 401 {
		t.Fatalf("v2 %d", w.Code)
	}
	if w := get("/api/v2/nothing"); w.Code != 404 || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatalf("v2 404 %d %s", w.Code, w.Body)
	}
	if w := get("/v/not-a-token/live/c1/index.m3u8"); w.Code == 404 && strings.Contains(w.Body.String(), `"not found"`) {
		t.Fatalf("video gateway not mounted: %s", w.Body)
	}
	if w := get("/share/unknown-token"); w.Code != 410 {
		t.Fatalf("share %d", w.Code)
	}

	// v2 sign-in works through it (the 2.0+ app over the relay or tunnel),
	// and failures are recorded against the forwarded client address.
	v2Token(t, s, "op", "Operator")
	req := httptest.NewRequest("POST", "/api/v2/auth/token", strings.NewReader(`{"grantType":"password","name":"op","password":"correct horse battery","client":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "accessToken") {
		t.Fatalf("sign in through the remote origin: %d %s", w.Code, w.Body)
	}
	req = httptest.NewRequest("POST", "/api/v2/auth/token", strings.NewReader(`{"grantType":"password","name":"op","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:50001"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	entries, _ := s.Store.AuditLog(context.Background(), 20)
	found := false
	for _, e := range entries {
		if e.Action == "Failed sign-in" && strings.Contains(e.Detail, "203.0.113.9") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed sign-in not recorded against the forwarded address: %+v", entries)
	}
}

func TestProxiedClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "10.0.0.1, 203.0.113.9")
	if got := proxiedClientIP(r); got != "203.0.113.9" {
		t.Fatalf("xff %q", got)
	}
	r.Header.Set("CF-Connecting-IP", "198.51.100.7")
	if got := proxiedClientIP(r); got != "198.51.100.7" {
		t.Fatalf("cf %q", got)
	}
	r.RemoteAddr = "192.0.2.4:1" // not a local proxy: headers ignored
	if got := proxiedClientIP(r); got != "" {
		t.Fatalf("remote peer trusted: %q", got)
	}
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Del("CF-Connecting-IP")
	r.Header.Set("X-Forwarded-For", "garbage")
	if got := proxiedClientIP(r); got != "" {
		t.Fatalf("garbage %q", got)
	}
	r.Header.Set("X-Forwarded-Host", "k7f3q9x2ab-cloud.example")
	if got := proxiedHost(r); got != "k7f3q9x2ab-cloud.example" {
		t.Fatalf("host %q", got)
	}
	for _, bad := range []string{"evil.example/path", "a b", "x@y"} {
		r.Header.Set("X-Forwarded-Host", bad)
		if got := proxiedHost(r); got != "" {
			t.Fatalf("host %q accepted", bad)
		}
	}
	r.Header.Set("X-Forwarded-Host", "k7f3q9x2ab-cloud.example")
	r.RemoteAddr = "192.0.2.4:1"
	if got := proxiedHost(r); got != "" {
		t.Fatal("host from a remote peer trusted")
	}

	// Through the handler: the public host is what the origin sees, so a
	// same-origin browser call from it passes the cross-site check.
	s := newCompanionServer(t)
	h := s.RemoteHandler()
	req := httptest.NewRequest("POST", "/api/v2/auth/revoke", strings.NewReader(`{"refreshToken":"x"}`))
	req.Host = "k7f3q9x2ab-cloud.example" // the relay's host: relayHost
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://custom.example")
	req.Header.Set("X-Forwarded-Host", "custom.example")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "x"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Fatalf("same-origin call through the relay refused: %s", w.Body)
	}
}

func TestCloudBackupSource(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	src := cloudSource{s}
	if id, err := src.LatestAlarmID(ctx); err != nil || id != "" {
		t.Fatalf("empty: %q %v", id, err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	var ids []string
	for i, kind := range []string{"Person", "Vehicle", "Storage"} {
		cam := "cam-1"
		if kind == "Storage" {
			cam = ""
		}
		a, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: cam, CameraName: "Front door", Kind: kind, Title: kind + " detected",
			Severity: "Warning", At: base.Add(time.Duration(i) * time.Minute), Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if id, _ := src.LatestAlarmID(ctx); id != ids[2] {
		t.Fatalf("latest %q", id)
	}
	all, err := src.AlarmsAfter(ctx, "-", base.Add(-time.Minute), 10)
	if err != nil || len(all) != 3 || all[0].ID != ids[0] || all[2].ID != ids[2] {
		t.Fatalf("from start: %v %v", all, err)
	}
	after, _ := src.AlarmsAfter(ctx, ids[0], base.Add(-time.Minute), 10)
	if len(after) != 2 || after[0].ID != ids[1] {
		t.Fatalf("after first: %v", after)
	}
	if none, _ := src.AlarmsAfter(ctx, ids[2], base.Add(-time.Minute), 10); len(none) != 0 {
		t.Fatalf("after last: %v", none)
	}
	if recent, _ := src.AlarmsAfter(ctx, "-", base.Add(90*time.Second), 10); len(recent) != 1 {
		t.Fatalf("since: %v", recent)
	}
	var rec map[string]any
	_ = json.Unmarshal(all[0].Record, &rec)
	if rec["id"] != ids[0] || rec["cameraId"] != "cam-1" || rec["kind"] != "Person" || !isMs(rec["createdAt"]) {
		t.Fatalf("record %v", rec)
	}
	if all[0].Meta["cameraName"] != "Front door" || all[0].CreatedAt.Unix() != base.Unix() {
		t.Fatalf("meta %v", all[0].Meta)
	}
	if _, err := src.Snapshot(ctx, ids[2]); err != cloud.ErrNoSnapshot {
		t.Fatalf("health alarm snapshot: %v", err)
	}
	if _, err := src.Snapshot(ctx, ids[0]); err != cloud.ErrNoSnapshot { // no event linked
		t.Fatalf("no event: %v", err)
	}

	// Clips: evidence locked for alarms, oldest first, after the cursor.
	lockedAt := time.Now().Unix()
	for i, id := range ids[:2] {
		if err := s.Store.AddEvidence(ctx, &store.Evidence{ID: "EV-" + string(rune('A'+i)), CaseID: "C-" + string(rune('A'+i)), AlarmID: id, CameraID: "cam-1",
			File: "x.mp4", Size: 100, SHA256: strings.Repeat("0", 64), LockedAt: lockedAt, LockedBy: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	clips, err := src.ClipsAfter(ctx, time.Unix(lockedAt-1, 0), "", 10)
	if err != nil || len(clips) != 2 || clips[0].EvidenceID != "EV-A" || clips[0].AlarmID != ids[0] || clips[0].Size != 100 {
		t.Fatalf("clips %v %v", clips, err)
	}
	if rest, _ := src.ClipsAfter(ctx, time.Unix(lockedAt, 0), "EV-A", 10); len(rest) != 1 || rest[0].EvidenceID != "EV-B" {
		t.Fatalf("after EV-A: %v", rest)
	}
}

func TestPipeResponse(t *testing.T) {
	pr, pw := io.Pipe()
	w := &pipeResponse{h: http.Header{}, pw: pw, status: make(chan int, 1)}
	go func() {
		_, _ = w.Write([]byte("clip bytes"))
		w.finish()
	}()
	if code := <-w.status; code != 200 {
		t.Fatalf("status %d", code)
	}
	if b, _ := io.ReadAll(pr); string(b) != "clip bytes" {
		t.Fatalf("body %q", b)
	}
	pr2, pw2 := io.Pipe()
	w2 := &pipeResponse{h: http.Header{}, pw: pw2, status: make(chan int, 1)}
	go w2.finish() // wrote nothing
	if code := <-w2.status; code != 500 {
		t.Fatalf("empty handler status %d", code)
	}
	pr2.Close()
}

// MARK: - Wave 13: link with a signed-in account, refresh, signIn

// v2Raw sends a v2 request and returns the status and the raw body.
func v2Raw(t *testing.T, h http.Handler, method, path, bearer, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// settingsHold reports whether any setting or audit entry contains s.
func settingsHold(t *testing.T, srv *Server, s string) bool {
	t.Helper()
	rows, err := srv.Store.DB.Query(`SELECT value FROM settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		if strings.Contains(v, s) {
			return true
		}
		if plain, err := srv.Store.Unseal(v); err == nil && strings.Contains(plain, s) {
			return true
		}
	}
	entries, _ := srv.Store.AuditLog(context.Background(), 500)
	for _, e := range entries {
		if strings.Contains(e.Detail, s) || strings.Contains(e.Action, s) {
			return true
		}
	}
	return false
}

func accountBody(token string) string {
	b, _ := json.Marshal(map[string]string{"accessToken": token})
	return string(b)
}

func TestV2CloudLinkWithAccount(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.approve, f.jwt = false, testAccountJWT // only the account's approval lets the poll answer
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	ctx := context.Background()

	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/link", viewer, accountBody(testAccountJWT)); code != 403 {
		t.Fatalf("viewer %d", code)
	}
	// One call: linked when it answers, no code shown.
	code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT))
	if code != 200 || m["linked"] != true || m["serverId"] != "k7f3q9x2ab" || m["plan"] != "cloud" || m["status"] != "active" || m["accountEmailMasked"] != "e***@example.com" {
		t.Fatalf("link with account: %d %v", code, m)
	}
	if _, ok := m["link"]; ok {
		t.Fatal("a code in the answer")
	}
	if r := m["remote"].(map[string]any); r["url"] != "https://k7f3q9x2ab-cloud.example" {
		t.Fatalf("remote %v", r)
	}
	f.mu.Lock()
	auths := append([]string(nil), f.approveAuth...)
	f.mu.Unlock()
	if len(auths) != 1 || auths[0] != "Bearer "+testAccountJWT {
		t.Fatalf("approve auth %v", auths)
	}
	// Audited with who did it, and push moved to Sentinel Cloud, as the code flow does.
	entries, _ := s.Store.AuditLog(ctx, 50)
	audited := false
	for _, e := range entries {
		if e.Area == "Settings" && e.Action == "Linked Sentinel Cloud" && e.User == "admin" && strings.Contains(e.Detail, "k7f3q9x2ab") {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("link not audited: %+v", entries)
	}
	if s.pushMode() != "cloud" {
		t.Fatalf("push mode %q", s.pushMode())
	}
	waitCloud(t, "push registered", func() bool { return s.Store.Setting(ctx, cloudPushInstallSetting, "") == "k7f3q9x2ab" })
	// The account token is kept nowhere.
	if settingsHold(t, s, testAccountJWT) {
		t.Fatal("the account token was stored")
	}

	// Linked: a second sign-in is refused; the code flow too, as before.
	code, raw := v2Raw(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT))
	if code != 409 || !jsonIs(raw, `{"error":{"code":"cloud_already_linked","message":"this server is already linked to Sentinel Cloud; unlink it first"}}`) {
		t.Fatalf("second link: %d %s", code, raw)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, ""); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("code flow while linked: %d %v", code, m)
	}
	f.mu.Lock()
	n := len(f.approveAuth)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("approved %d times", n)
	}
}

// jsonIs reports whether raw is the same JSON as want.
func jsonIs(raw, want string) bool {
	var a, b any
	if json.Unmarshal([]byte(raw), &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

func TestV2CloudLinkWithAccountErrors(t *testing.T) {
	limitMsg := "Sentinel Cloud links one server. Upgrade to Sentinel Cloud Business to link more."
	cases := []struct {
		name   string
		setup  func(f *fakeControlPlane)
		body   string
		status int
		want   string // the exact JSON answer
	}{
		{name: "sign-in rejected", setup: func(f *fakeControlPlane) { f.jwt = "another.session.token" }, body: accountBody(testAccountJWT), status: 401,
			want: `{"error":{"code":"cloud_sign_in","message":"Sign in again: your session has expired."}}`},
		{name: "not a token", body: accountBody("not a token"), status: 401,
			want: `{"error":{"code":"cloud_sign_in","message":"sign in to your Sentinel Cloud account again"}}`},
		{name: "server limit", setup: func(f *fakeControlPlane) {
			f.approveFail, f.approveBody = 402, map[string]any{"error": limitMsg, "upgradeURL": "https://site.example/cloud#plans"}
		}, body: accountBody(testAccountJWT), status: 402,
			want: `{"error":{"code":"cloud_server_limit","message":"` + limitMsg + `","upgradeURL":"https://site.example/cloud#plans"}}`},
		{name: "server limit without an upgrade address", setup: func(f *fakeControlPlane) {
			f.approveFail, f.approveBody = 402, map[string]any{"error": limitMsg}
		}, body: accountBody(testAccountJWT), status: 402,
			want: `{"error":{"code":"cloud_server_limit","message":"` + limitMsg + `","upgradeURL":""}}`},
		{name: "control plane fails", setup: func(f *fakeControlPlane) {
			f.approveFail, f.approveBody = 500, map[string]any{"error": "internal error"}
		}, body: accountBody(testAccountJWT), status: 502,
			want: `{"error":{"code":"cloud_unreachable","message":"Sentinel Cloud: internal error"}}`},
		{name: "rate limited", setup: func(f *fakeControlPlane) {
			f.approveFail, f.approveBody = 429, map[string]any{"error": "Too many attempts. Wait a minute and try again."}
		}, body: accountBody(testAccountJWT), status: 429,
			want: `{"error":{"code":"too_many_requests","message":"Sentinel Cloud: Too many attempts. Wait a minute and try again."}}`},
		{name: "unreachable", setup: func(f *fakeControlPlane) { f.srv.Close() }, body: accountBody(testAccountJWT), status: 502},
		{name: "empty object", body: `{}`, status: 400},
		{name: "empty token", body: `{"accessToken":"  "}`, status: 400},
		{name: "token not a string", body: `{"accessToken":5}`, status: 400},
		{name: "unknown field", body: `{"accessToken":"` + testAccountJWT + `","email":"x@example.com"}`, status: 400},
		{name: "not JSON", body: `accessToken=` + testAccountJWT, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCompanionServer(t)
			h := s.Handler()
			f := newFakeControlPlane(t)
			f.approve, f.jwt = false, testAccountJWT
			if tc.setup != nil {
				tc.setup(f)
			}
			withCloud(t, s, f)
			admin := v2Token(t, s, "admin", "Admin")
			code, raw := v2Raw(t, h, "POST", "/api/v2/cloud/link", admin, tc.body)
			if code != tc.status || (tc.want != "" && !jsonIs(raw, tc.want)) {
				t.Fatalf("%d %s, want %d %s", code, raw, tc.status, tc.want)
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(raw), &m)
			if tc.status == 502 && errCode(m) != "cloud_unreachable" || tc.status == 400 && errCode(m) != "bad_request" {
				t.Fatalf("code %q", errCode(m))
			}
			if strings.Contains(raw, testAccountJWT) {
				t.Fatal("the answer echoes the account token")
			}
			_, st := v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
			if st["linked"] != false || auditActions(t, s, "Settings")["Linked Sentinel Cloud"] || settingsHold(t, s, testAccountJWT) {
				t.Fatalf("after a failed link: %v", st)
			}
		})
	}

	// Without Sentinel Cloud set up on this server: unavailable, as the code flow.
	s := newCompanionServer(t)
	admin := v2Token(t, s, "admin", "Admin")
	if code, m := v2Call(t, s.Handler(), "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT)); code != 503 || errCode(m) != "unavailable" {
		t.Fatalf("no client: %d %v", code, m)
	}
}

func TestV2CloudLinkEmptyBodyKeepsTheCodeFlow(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.approve, f.jwt = false, testAccountJWT
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	for _, body := range []string{"", " \n"} {
		code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, body)
		link, _ := m["link"].(map[string]any)
		if code != 200 || m["linked"] != false || link["userCode"] != "ABCD-EFGH" {
			t.Fatalf("body %q: %d %v", body, code, m)
		}
	}
	f.mu.Lock()
	n := len(f.approveAuth)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("the code flow approved by itself")
	}
	// Signing in while that code is pending links right away and drops it.
	code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT))
	if _, pending := m["link"]; code != 200 || m["linked"] != true || pending {
		t.Fatalf("account link over a pending code: %d %v", code, m)
	}
}

func TestV2CloudRefresh(t *testing.T) {
	// No client: the status, nothing to refresh.
	s0 := newCompanionServer(t)
	admin0 := v2Token(t, s0, "admin", "Admin")
	if code, m := v2Call(t, s0.Handler(), "POST", "/api/v2/cloud/refresh", admin0, ""); code != 200 || m["linked"] != false {
		t.Fatalf("no client: %d %v", code, m)
	}

	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.jwt = testAccountJWT
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.entCalls
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/refresh", viewer, ""); code != 403 {
		t.Fatalf("viewer %d", code)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, ""); code != 200 || m["linked"] != false || calls() != 0 {
		t.Fatalf("unlinked: %d %v, %d calls", code, m, calls())
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT)); code != 200 {
		t.Fatal("link")
	}
	// The owner subscribes to Business on the website.
	f.mu.Lock()
	f.plan = "business"
	f.mu.Unlock()
	// Reading the status doesn't refresh an entitlement this fresh …
	if _, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); m["plan"] != "cloud" || calls() != 0 {
		t.Fatalf("status read: %v, %d calls", m["plan"], calls())
	}
	// … the refresh does, right away.
	code, m := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, "")
	if code != 200 || m["plan"] != "business" || m["linked"] != true || calls() != 1 {
		t.Fatalf("refresh: %d %v, %d calls", code, m, calls())
	}
	// At most once per 10 seconds: a second one answers the cached status.
	f.mu.Lock()
	f.plan = "cloud"
	f.mu.Unlock()
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, ""); code != 200 || m["plan"] != "business" || calls() != 1 {
		t.Fatalf("second refresh: %d %v, %d calls", code, m["plan"], calls())
	}
	// A failing refresh still answers the (cached) status.
	f.srv.Close()
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, ""); code != 200 || m["linked"] != true {
		t.Fatalf("control plane gone: %d %v", code, m)
	}
}

func TestV2CloudSignIn(t *testing.T) {
	config := map[string]any{
		"supabaseUrl":     "https://project.supabase.example",
		"supabaseAnonKey": "sb_publishable_test",
		"siteBase":        "https://site.example",
		"checkoutURL":     "https://project.supabase.example/functions/v1/sentinel-create-checkout",
		"portalURL":       "https://project.supabase.example/functions/v1/sentinel-portal",
		"plans": []any{
			map[string]any{"id": "cloud", "name": "Sentinel Cloud", "month": 4.99, "year": 49, "servers": 1, "backupGB": 10, "days": 30},
			map[string]any{"id": "business", "name": "Sentinel Cloud Business", "month": 19.99, "year": 199, "servers": 5, "backupGB": 100, "days": 90},
		},
	}
	want, _ := json.Marshal(config)

	// Without a client: null.
	s0 := newCompanionServer(t)
	admin0 := v2Token(t, s0, "admin", "Admin")
	if _, m := v2Call(t, s0.Handler(), "GET", "/api/v2/cloud", admin0, ""); !hasNull(m, "signIn") {
		t.Fatalf("no client: %v", m)
	}

	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	gate := make(chan struct{})
	f.config, f.jwt, f.configGate = config, testAccountJWT, gate // Sentinel Cloud is slow to answer
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	viewer := v2Token(t, s, "viewer", "Viewer")
	configCalls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.configCalls
	}
	// Nothing cached: the status never waits for Sentinel Cloud. signIn is
	// null and fetched once in the background.
	start := time.Now()
	for range 3 {
		if code, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); code != 200 || !hasNull(m, "signIn") {
			t.Fatalf("while fetching: %d %v", code, m["signIn"])
		}
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the status waited %v for Sentinel Cloud", waited)
	}
	waitCloud(t, "the background fetch", func() bool { return configCalls() == 1 })
	close(gate)
	waitCloud(t, "signIn", func() bool { _, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, ""); return m["signIn"] != nil })
	_, m := v2Call(t, h, "GET", "/api/v2/cloud", admin, "")
	got, _ := json.Marshal(m["signIn"])
	if !jsonIs(string(got), string(want)) {
		t.Fatalf("signIn\n got %s\nwant %s", got, want)
	}
	// Cached: every CloudStatus carries it, viewers' too, without asking again.
	_, m = v2Call(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT))
	if got, _ := json.Marshal(m["signIn"]); m["linked"] != true || !jsonIs(string(got), string(want)) {
		t.Fatalf("after linking: %s", got)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/cloud", viewer, "")
	if got, _ := json.Marshal(m["signIn"]); !jsonIs(string(got), string(want)) || configCalls() != 1 {
		t.Fatalf("viewer: %s, %d calls", got, configCalls())
	}

	// A control plane without it (or unreachable): null, and not asked again
	// on every read.
	s2 := newCompanionServer(t)
	f2 := newFakeControlPlane(t)
	withCloud(t, s2, f2)
	admin2 := v2Token(t, s2, "admin", "Admin")
	calls2 := func() int {
		f2.mu.Lock()
		defer f2.mu.Unlock()
		return f2.configCalls
	}
	if _, m := v2Call(t, s2.Handler(), "GET", "/api/v2/cloud", admin2, ""); !hasNull(m, "signIn") {
		t.Fatalf("no config: %v", m["signIn"])
	}
	waitCloud(t, "the failed fetch", func() bool { return calls2() == 1 })
	time.Sleep(20 * time.Millisecond)
	if _, m := v2Call(t, s2.Handler(), "GET", "/api/v2/cloud", admin2, ""); !hasNull(m, "signIn") || calls2() != 1 {
		t.Fatalf("after a failure: %v, %d calls", m["signIn"], calls2())
	}
}

func TestDashboardCSPAllowsSupabase(t *testing.T) {
	csp := func(h http.Handler, path string) string {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Header().Get("Content-Security-Policy")
	}
	policy := func(connect string) string {
		return "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src " + connect +
			"; worker-src 'self' blob:; frame-ancestors 'none'"
	}
	s := newCompanionServer(t)
	// The dashboard: Supabase always, for the Sentinel Cloud card's sign-in, checkout and portal.
	if got := csp(s.Handler(), "/"); got != policy("'self' https://*.supabase.co") {
		t.Fatalf("dashboard without a client:\n%s", got)
	}
	// The internet-facing origin and the phone API never serve the dashboard: unchanged.
	if got := csp(s.RemoteHandler(), "/health"); got != policy("'self'") {
		t.Fatalf("remote handler:\n%s", got)
	}
	if got := csp(s.CompanionHandler(), "/health"); got != policy("'self'") {
		t.Fatalf("companion handler:\n%s", got)
	}

	// With the cached configuration: its sign-in and billing origins too,
	// when the wildcard doesn't cover them; nothing that isn't a plain origin.
	f := newFakeControlPlane(t)
	f.config = map[string]any{"supabaseUrl": "https://auth.example.com:8443/", "supabaseAnonKey": "sb_publishable_test",
		"siteBase": "https://site.example", "checkoutURL": "https://abc.supabase.co/functions/v1/sentinel-create-checkout",
		"portalURL": "https://pay.example.com/portal", "plans": []any{}}
	c := withCloud(t, s, f)
	h := s.Handler()
	if got := csp(h, "/"); got != policy("'self' https://*.supabase.co") {
		t.Fatalf("before the configuration is fetched:\n%s", got) // PeekConfig never fetches
	}
	if c.Config(context.Background()) == nil {
		t.Fatal("no configuration")
	}
	if got := csp(h, "/"); got != policy("'self' https://*.supabase.co https://auth.example.com:8443 https://pay.example.com") {
		t.Fatalf("with the configuration:\n%s", got)
	}
	if got := csp(h, "/api/v2/cloud"); !strings.Contains(got, "https://auth.example.com:8443") {
		t.Fatalf("API answers:\n%s", got)
	}

	f2 := newFakeControlPlane(t)
	f2.config = map[string]any{"supabaseUrl": "https://evil.example;script-src", "supabaseAnonKey": "k",
		"checkoutURL": "https://[::1]/p", "portalURL": "https://evil.example;connect-src/x", "plans": []any{}}
	s2 := newCompanionServer(t)
	c2 := withCloud(t, s2, f2)
	if c2.Config(context.Background()) == nil {
		t.Fatal("the client refused the configuration; the policy isn't being tested")
	}
	if got := csp(s2.Handler(), "/"); got != policy("'self' https://*.supabase.co") {
		t.Fatalf("injected origins:\n%s", got)
	}
}

// hasNull reports whether m has key with a JSON null.
func hasNull(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v == nil
}

// A server linked while its account had no push (free plan, or the plan came
// later as a manual grant) moves push to Sentinel Cloud as soon as an
// entitlement with push arrives, not only at link time.
func TestV2CloudPushSwitchesWhenThePlanArrives(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	f := newFakeControlPlane(t)
	f.jwt = testAccountJWT
	f.plan = "free"
	f.features = []string{}
	withCloud(t, s, f)
	admin := v2Token(t, s, "admin", "Admin")
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/link", admin, accountBody(testAccountJWT)); code != 200 || m["linked"] != true {
		t.Fatalf("link: %d %v", code, m)
	}
	if mode := s.pushMode(); mode != "off" {
		t.Fatalf("push mode after a free link: %q", mode)
	}
	// The owner is granted Business (plan_override) after linking.
	f.mu.Lock()
	f.plan = "business"
	f.features = []string{"relay", "push", "backup"}
	f.mu.Unlock()
	if code, m := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, ""); code != 200 || m["plan"] != "business" {
		t.Fatalf("refresh: %d %v", code, m)
	}
	waitCloud(t, "push mode cloud", func() bool { return s.pushMode() == "cloud" })
	// An owner sending with their own APNs key is left alone.
	if err := s.Store.SetSetting(context.Background(), pushModeSetting, "direct"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.plan = "cloud"
	f.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	if code, _ := v2Call(t, h, "POST", "/api/v2/cloud/refresh", admin, ""); code != 200 {
		t.Fatal("second refresh")
	}
	if mode := s.pushMode(); mode != "direct" {
		t.Fatalf("direct push was overridden: %q", mode)
	}
}
