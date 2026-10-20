package cloud

// The client against an httptest control plane on 127.0.0.1:0.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memStore is an in-memory Store. Seal is reversible but never the
// plain text, so tests can see a secret was sealed.
type memStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemStore() *memStore { return &memStore{m: map[string]string{}} }

func (s *memStore) Setting(_ context.Context, key, fallback string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return v
	}
	return fallback
}

func (s *memStore) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *memStore) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return "sealed:" + base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func (s *memStore) Unseal(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(sealed, "sealed:")
	if !ok {
		return "", errors.New("not sealed")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	return string(b), err
}

const testBackupToken = "scb_k7f3q9x2ab_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"

// fakeCloud is the control plane (section 1), with backup storage.
type fakeCloud struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	pendingPoll int    // 202s before the approval
	pollGone    bool   // answer 404 (expired)
	issued      bool   // the token went out once
	token       string // the server token it issues
	ent         Entitlement
	entStatus   int // non-zero: GET /v1/entitlement answers this
	start       map[string]string
	deletes     int
	tokens      int
	objects     map[string][]byte
	metas       map[string]string
	types       map[string]string
	quota       int64
	putFail     int // answer 500 to this many PUTs
	objDeletes  []string
	usageCalls  int
	entCalls    int // GET /v1/entitlement

	// Linking: each start issues device-code-<starts>; only the latest polls.
	starts       int
	startCalls   int            // start requests, refused or not
	startStatus  int            // non-zero: start answers this
	needApproval bool           // poll answers 202 until approve accepted the code
	approved     bool           // … which it did (reset by start)
	jwt          string         // the account session token approve accepts
	approveAuth  []string       // Authorization headers approve got
	approveCode  string         // the userCode approve got last
	approveFail  int            // non-zero: approve answers this status …
	approveBody  map[string]any // … with this body
	polls        int
	pollFail     int    // answer 500 to this many polls
	onPoll       func() // runs at each poll, before answering

	config      any           // GET /v1/config answers this; nil = 404
	configFail  int           // non-zero: GET /v1/config answers this status
	configGate  chan struct{} // non-nil: GET /v1/config waits for it to close
	configCalls int
}

// testJWT stands for a Supabase session token (never a real one).
const testJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyLTEiLCJlbWFpbCI6ImVqQGV4YW1wbGUuY29tIn0.c2lnbmF0dXJlLWZvci10ZXN0cy1vbmx5"

func newFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	end := "2026-11-19T00:00:00Z"
	f := &fakeCloud{t: t, token: testToken, objects: map[string][]byte{}, metas: map[string]string{}, types: map[string]string{}, quota: 10 << 30}
	f.ent = Entitlement{ServerID: "k7f3q9x2ab", Plan: "cloud", Status: "active", PeriodEnd: &end,
		Features: []string{"push", "backup"}, Quotas: Quotas{BackupBytes: 10 << 30, BackupDays: 30, Servers: 1},
		RelayHost: "k7f3q9x2ab-cloud.example", AccountEmailMasked: "e***@example.com",
		IssuedAt: "2026-10-19T20:00:00Z", RefreshAfter: time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339)}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			tok := f.token
			f.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+tok {
				writeErr(w, 401, "unknown server token")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /v1/link/start", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.startCalls++
		if f.startStatus != 0 {
			writeErr(w, f.startStatus, "start refused")
			return
		}
		f.start, f.approved = in, false
		f.starts++
		writeJSONT(w, 200, map[string]any{"deviceCode": fmt.Sprintf("device-code-%d", f.starts), "userCode": "ABCD-EFGH",
			"verifyURL": "https://site.example/cloud?code=ABCD-EFGH", "expiresAt": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "interval": 5})
	})
	mux.HandleFunc("POST /v1/link/approve", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		f.approveAuth, f.approveCode = append(f.approveAuth, auth), in["userCode"]
		switch {
		case f.approveFail != 0:
			writeJSONT(w, f.approveFail, f.approveBody)
		case f.jwt == "" || auth != "Bearer "+f.jwt:
			writeErr(w, 401, "Sign in again: your session has expired.")
		case in["userCode"] != "ABCD-EFGH" || f.starts == 0:
			writeErr(w, 404, "That code is unknown or has expired.")
		case f.approved:
			writeErr(w, 409, "This code has already been approved.")
		default:
			f.approved = true
			writeJSONT(w, 200, map[string]string{"serverName": f.start["serverName"]})
		}
	})
	mux.HandleFunc("POST /v1/link/poll", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		hook := f.onPoll
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		switch {
		case in["deviceCode"] != fmt.Sprintf("device-code-%d", f.starts) || f.pollGone || f.issued:
			writeErr(w, 404, "expired")
		case f.pollFail > 0:
			f.pollFail--
			writeErr(w, 500, "try again")
		case f.needApproval && !f.approved:
			writeJSONT(w, 202, map[string]bool{"pending": true})
		case f.pendingPoll > 0:
			f.pendingPoll--
			writeJSONT(w, 202, map[string]bool{"pending": true})
		default:
			f.issued = true
			writeJSONT(w, 200, map[string]any{"serverToken": f.token, "serverId": "k7f3q9x2ab", "entitlement": f.ent})
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
		switch {
		case f.configFail != 0:
			writeErr(w, f.configFail, "configuration unavailable")
		case f.config == nil:
			writeErr(w, 404, "not found")
		default:
			writeJSONT(w, 200, f.config)
		}
	})
	mux.HandleFunc("GET /v1/entitlement", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.entCalls++
		if f.entStatus != 0 {
			writeErr(w, f.entStatus, "nope")
			return
		}
		writeJSONT(w, 200, f.ent)
	}))
	mux.HandleFunc("DELETE /v1/servers/self", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deletes++
		f.mu.Unlock()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /v1/backup/tokens", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		writeJSONT(w, 200, map[string]string{"token": testBackupToken, "expiresAt": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	}))
	mux.HandleFunc("PUT /v1/backup/objects/{key...}", authed(func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		body, err := io.ReadAll(r.Body)
		if err != nil || int64(len(body)) != r.ContentLength {
			writeErr(w, 400, "length mismatch")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.putFail > 0 {
			f.putFail--
			writeErr(w, 500, "try later")
			return
		}
		if _, ok := f.objects[key]; ok {
			writeErr(w, 409, "exists")
			return
		}
		if f.used()+int64(len(body)) > f.quota {
			writeErr(w, 413, "over quota")
			return
		}
		f.objects[key], f.metas[key], f.types[key] = body, r.Header.Get("X-Sentinel-Meta"), r.Header.Get("Content-Type")
		writeJSONT(w, 201, map[string]any{"key": key, "size": len(body), "bytesUsed": f.used(), "bytesQuota": f.quota})
	}))
	mux.HandleFunc("DELETE /v1/backup/objects/{key...}", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.objects, r.PathValue("key"))
		f.objDeletes = append(f.objDeletes, r.PathValue("key"))
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /v1/backup/objects/{key...}", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, ok := f.objects[r.PathValue("key")]
		if !ok {
			writeErr(w, 404, "no such object")
			return
		}
		w.WriteHeader(206)
		_, _ = w.Write(b[:min(1, len(b))])
	}))
	mux.HandleFunc("GET /v1/backup/usage", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.usageCalls++
		writeJSONT(w, 200, map[string]any{"bytesUsed": f.used(), "objects": len(f.objects), "bytesQuota": f.quota, "days": 30, "oldestAt": nil})
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// used: caller holds f.mu.
func (f *fakeCloud) used() int64 {
	var n int64
	for _, b := range f.objects {
		n += int64(len(b))
	}
	return n
}

func (f *fakeCloud) counts() (deletes, tokens int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes, f.tokens
}

func (f *fakeCloud) object(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return b, ok
}

func writeJSONT(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSONT(w, status, map[string]string{"error": msg})
}

// newTestClient is a started client against f.
func newTestClient(t *testing.T, f *fakeCloud, st *memStore) *Client {
	t.Helper()
	c := &Client{BaseURL: f.srv.URL, Store: st, Version: "9.9.9-test", Platform: "linux", AllowInsecure: true,
		ServerName: func() string { return "Office Mac" }, PollEvery: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx)
	return c
}

// linkNow stores a link as if the owner had approved it.
func linkNow(t *testing.T, st *memStore, ent Entitlement) {
	t.Helper()
	ctx := context.Background()
	sealed, _ := st.Seal(testToken)
	raw, _ := json.Marshal(ent)
	_ = st.SetSetting(ctx, KeyToken, sealed)
	_ = st.SetSetting(ctx, KeyServerID, "k7f3q9x2ab")
	_ = st.SetSetting(ctx, KeyEntitlement, string(raw))
	_ = st.SetSetting(ctx, KeyEntitlementAt, time.Now().UTC().Format(time.RFC3339))
}

func TestClientLinkFlow(t *testing.T) {
	f := newFakeCloud(t)
	f.pendingPoll = 2
	st := newMemStore()
	c := newTestClient(t, f, st)
	type linked struct {
		who string
		e   Entitlement
	}
	got := make(chan linked, 1)
	c.OnLinked = func(who string, e Entitlement) { got <- linked{who, e} }
	ctx := context.Background()

	info, err := c.Link(ctx, "admin")
	if err != nil || info.UserCode != "ABCD-EFGH" || info.VerifyURL != "https://site.example/cloud?code=ABCD-EFGH" || info.ExpiresAt == "" {
		t.Fatalf("link: %+v %v", info, err)
	}
	f.mu.Lock()
	start := f.start
	f.mu.Unlock()
	if start["serverName"] != "Office Mac" || start["version"] != "9.9.9-test" || start["platform"] != "linux" {
		t.Fatalf("start body %v", start)
	}
	// Pending: the status carries the code; asking again returns the same code.
	if s := c.Status(ctx); s.Linked || s.Link == nil || s.Link.UserCode != "ABCD-EFGH" {
		t.Fatalf("pending status %+v", s)
	}
	if again, err := c.Link(ctx, "admin"); err != nil || again != info {
		t.Fatalf("second link %+v %v", again, err)
	}

	l := recv(t, got)
	if l.who != "admin" || !l.e.Has(FeatureBackup) {
		t.Fatalf("OnLinked %+v", l)
	}
	if raw := st.Setting(ctx, KeyToken, ""); raw == "" || strings.Contains(raw, testToken) {
		t.Fatalf("token not sealed: %q", raw)
	}
	if tok, _ := st.Unseal(st.Setting(ctx, KeyToken, "")); tok != testToken {
		t.Fatal("sealed token doesn't open")
	}
	if st.Setting(ctx, KeyServerID, "") != "k7f3q9x2ab" || st.Setting(ctx, KeyEntitlementAt, "") == "" {
		t.Fatal("server id or entitlement time not saved")
	}
	s := c.Status(ctx)
	if !s.Linked || s.Link != nil || s.ServerID != "k7f3q9x2ab" || s.ServerName != "Office Mac" || s.Plan != "cloud" || s.Status != "active" ||
		s.AccountEmailMasked != "e***@example.com" || s.Quotas.BackupDays != 30 || s.PeriodEnd == nil || *s.PeriodEnd != "2026-11-19T00:00:00Z" {
		t.Fatalf("linked status %+v", s)
	}
	if s.Remote.URL != "https://k7f3q9x2ab-cloud.example" || s.Remote.State != StateInactive || !s.Remote.Enabled {
		t.Fatalf("remote without the relay feature %+v", s.Remote)
	}
	if _, err := c.Link(ctx, "admin"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("link while linked: %v", err)
	}
}

func TestClientLinkExpiresAndCancels(t *testing.T) {
	f := newFakeCloud(t)
	f.pollGone = true
	st := newMemStore()
	c := newTestClient(t, f, st)
	ctx := context.Background()
	if _, err := c.Link(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pending cleared", func() bool { return c.Status(ctx).Link == nil })
	if c.Linked(ctx) {
		t.Fatal("linked after an expired code")
	}

	f.mu.Lock()
	f.pollGone, f.pendingPoll = false, 1000
	f.mu.Unlock()
	if _, err := c.Link(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if !c.CancelLink() || c.Status(ctx).Link != nil || c.CancelLink() {
		t.Fatal("cancel")
	}
	// Unlink with only a pending code never calls /v1/servers/self.
	if _, err := c.Link(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Unlink(ctx); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlink while pending: %v", err)
	}
	if n, _ := f.counts(); c.Status(ctx).Link != nil || n != 0 {
		t.Fatalf("pending left or DELETE sent (%d)", n)
	}
}

func TestClientLinkRejectsMalformedToken(t *testing.T) {
	f := newFakeCloud(t)
	f.token = "scs_otherid234_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA_" // relay id ≠ serverId
	st := newMemStore()
	c := newTestClient(t, f, st)
	ctx := context.Background()
	if _, err := c.Link(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "poll done", func() bool { return c.Status(ctx).Link == nil })
	if c.Linked(ctx) || st.Setting(ctx, KeyToken, "") != "" {
		t.Fatal("stored a token for another server")
	}
}

func TestClientRefreshAndRejectedToken(t *testing.T) {
	f := newFakeCloud(t)
	st := newMemStore()
	linkNow(t, st, f.ent)
	c := newTestClient(t, f, st)
	reasons := make(chan string, 2)
	c.OnUnlinked = func(r string) { reasons <- r }
	ctx := context.Background()

	f.mu.Lock()
	f.ent.Plan, f.ent.Features = "business", []string{"relay", "push", "backup"}
	f.ent.Quotas.BackupBytes = 100 << 30
	f.mu.Unlock()
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if e := c.Entitlement(ctx); e.Plan != "business" || !e.Has(FeatureRelay) || e.Quotas.BackupBytes != 100<<30 {
		t.Fatalf("refreshed %+v", e)
	}
	// An entitlement naming another server is ignored.
	c.saveEntitlement(ctx, Entitlement{ServerID: "zzzzzzzzzz", Plan: "free"})
	if c.Entitlement(ctx).Plan != "business" {
		t.Fatal("took another server's entitlement")
	}
	// A failure keeps the cached entitlement.
	f.mu.Lock()
	f.entStatus = 503
	f.mu.Unlock()
	if err := c.Refresh(ctx); err == nil || c.Entitlement(ctx).Plan != "business" {
		t.Fatalf("503: %v %+v", err, c.Entitlement(ctx))
	}
	if d := c.nextRefresh(ctx); d != refreshFailMin {
		t.Fatalf("retry after failure in %v", d)
	}
	// 401: the token is rejected, but the server stays linked (a control
	// plane bug must not unlink every server). Nothing is active; the
	// refresh is retried hourly.
	f.mu.Lock()
	f.entStatus = 401
	f.mu.Unlock()
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("401 refresh succeeded")
	}
	select {
	case r := <-reasons:
		t.Fatalf("unlinked (%q) on a 401 from /v1/entitlement", r)
	case <-time.After(50 * time.Millisecond):
	}
	if !c.Linked(ctx) || st.Setting(ctx, KeyServerID, "") != "k7f3q9x2ab" || st.Setting(ctx, KeyEntitlement, "") == "" {
		t.Fatal("link settings dropped on a 401")
	}
	s := c.Status(ctx)
	if !s.Linked || s.Status != "inactive" || len(s.Features) != 0 || s.Features == nil || s.Plan != "business" {
		t.Fatalf("rejected status %+v", s)
	}
	if s.Remote.State != StateInactive || s.Remote.Error != RejectedMessage || !strings.Contains(s.Backup.Error, "rejected") {
		t.Fatalf("rejected remote/backup %+v %+v", s.Remote, s.Backup)
	}
	if c.RemoteURL(ctx) != "" {
		t.Fatal("remote URL handed out with a rejected token")
	}
	if _, err := c.BackupAccess(ctx); !errors.Is(err, ErrBackupOff) {
		t.Fatalf("backup access with a rejected token: %v", err)
	}
	if d := c.nextRefresh(ctx); d != time.Hour {
		t.Fatalf("rejected: next refresh in %v, want an hour", d)
	}
	// Accepted again: everything comes back.
	f.mu.Lock()
	f.entStatus = 0
	f.mu.Unlock()
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if s := c.Status(ctx); s.Status != "active" || !c.Entitlement(ctx).Has(FeatureRelay) || s.Remote.Error == RejectedMessage {
		t.Fatalf("after recovery %+v", s)
	}
}

func TestClientUnlink(t *testing.T) {
	f := newFakeCloud(t)
	st := newMemStore()
	linkNow(t, st, f.ent)
	_ = st.SetSetting(context.Background(), KeyBackupCursor, "A1")
	_ = st.SetSetting(context.Background(), KeyRemote, "off")
	c := newTestClient(t, f, st)
	reasons := make(chan string, 2)
	c.OnUnlinked = func(r string) { reasons <- r }
	ctx := context.Background()
	remoteErr, err := c.Unlink(ctx)
	if err != nil || remoteErr != nil {
		t.Fatalf("unlink: %v %v", remoteErr, err)
	}
	if n, _ := f.counts(); recv(t, reasons) != "unlinked" || n != 1 {
		t.Fatalf("deletes %d", n)
	}
	for _, k := range []string{KeyToken, KeyServerID, KeyEntitlement, KeyEntitlementAt, KeyBackupCursor} {
		if v := st.Setting(ctx, k, ""); v != "" {
			t.Errorf("%s = %q after unlink", k, v)
		}
	}
	if st.Setting(ctx, KeyRemote, "") != "off" {
		t.Error("the remote preference was forgotten")
	}
	s := c.Status(ctx)
	if s.Linked || s.ServerID != "" || s.Plan != "free" || s.Status != "inactive" || s.PeriodEnd != nil || len(s.Features) != 0 {
		t.Fatalf("unlinked status %+v", s)
	}
	if _, err := c.Unlink(ctx); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("second unlink %v", err)
	}

	// Sentinel Cloud unreachable: unlinked here anyway, remoteErr set.
	linkNow(t, st, f.ent)
	f.srv.Close()
	remoteErr, err = c.Unlink(ctx)
	if err != nil || remoteErr == nil || c.Linked(ctx) {
		t.Fatalf("offline unlink: %v %v linked=%v", remoteErr, err, c.Linked(ctx))
	}
}

func TestClientStaleEntitlementIsFree(t *testing.T) {
	f := newFakeCloud(t)
	f.ent.Features = []string{"relay", "push", "backup"}
	st := newMemStore()
	linkNow(t, st, f.ent)
	c := &Client{BaseURL: f.srv.URL, Store: st, AllowInsecure: true}
	ctx := context.Background()
	if c.RemoteURL(ctx) != "https://k7f3q9x2ab-cloud.example" {
		t.Fatal("fresh entitlement: no remote URL")
	}
	_ = st.SetSetting(ctx, KeyEntitlementAt, time.Now().Add(-8*24*time.Hour).UTC().Format(time.RFC3339))
	e := c.Entitlement(ctx)
	if e.Plan != "free" || len(e.Features) != 0 || e.RelayHost == "" {
		t.Fatalf("stale %+v", e)
	}
	if c.RemoteURL(ctx) != "" {
		t.Fatal("stale entitlement still gives the remote URL")
	}
	s := c.Status(ctx)
	if !s.Linked || s.Plan != "free" || s.Remote.State != StateInactive || !strings.Contains(s.Remote.Error, "week") {
		t.Fatalf("stale status %+v", s)
	}
	if d := c.nextRefresh(ctx); d != refreshFailMin {
		t.Fatalf("stale refresh in %v", d)
	}
}

func TestClientRemoteURLAndToggles(t *testing.T) {
	f := newFakeCloud(t)
	f.ent.Features = []string{"relay", "backup"}
	st := newMemStore()
	c := &Client{BaseURL: f.srv.URL, Store: st, AllowInsecure: true}
	ctx := context.Background()
	if c.RemoteURL(ctx) != "" {
		t.Fatal("unlinked remote URL")
	}
	linkNow(t, st, f.ent)
	if c.RemoteURL(ctx) != "https://k7f3q9x2ab-cloud.example" {
		t.Fatal("linked remote URL")
	}
	if err := c.SetRemote(ctx, false); err != nil || c.RemoteURL(ctx) != "" || st.Setting(ctx, KeyRemote, "") != "off" {
		t.Fatal("remote off")
	}
	_ = c.SetRemote(ctx, true)
	_ = st.SetSetting(ctx, KeyBackupCursor, "A9")
	if err := c.SetBackup(ctx, false); err != nil || st.Setting(ctx, KeyBackup, "") != "off" || st.Setting(ctx, KeyBackupCursor, "x") != "" {
		t.Fatal("backup off keeps the cursor")
	}
	if s := c.Status(ctx); s.Backup.Enabled || !s.Remote.Enabled {
		t.Fatalf("toggles %+v", s)
	}
	// A relay host that isn't a bare host name is never handed out.
	bad := f.ent
	bad.RelayHost = "evil.example/path"
	linkNow(t, st, bad)
	if c.RemoteURL(ctx) != "" {
		t.Fatal("bad relay host accepted")
	}
}

func TestClientBackupAccess(t *testing.T) {
	f := newFakeCloud(t)
	st := newMemStore()
	c := &Client{BaseURL: f.srv.URL, Store: st, AllowInsecure: true}
	ctx := context.Background()
	if _, err := c.BackupAccess(ctx); !errors.Is(err, ErrBackupOff) {
		t.Fatalf("unlinked: %v", err)
	}
	linkNow(t, st, f.ent)
	a, err := c.BackupAccess(ctx)
	if err != nil || a.Token != testBackupToken || a.ServerID != "k7f3q9x2ab" || a.BaseURL != f.srv.URL+"/v1/backup" || a.ExpiresAt == "" {
		t.Fatalf("access %+v %v", a, err)
	}
	if b, _ := c.BackupAccess(ctx); b != a {
		t.Fatal("not reused")
	}
	if _, n := f.counts(); n != 1 {
		t.Fatalf("%d tokens issued", n)
	}
	_ = c.SetBackup(ctx, false)
	if _, err := c.BackupAccess(ctx); !errors.Is(err, ErrBackupOff) {
		t.Fatalf("backup off: %v", err)
	}
	_ = c.SetBackup(ctx, true)
	noBackup := f.ent
	noBackup.Features = []string{"push"}
	linkNow(t, st, noBackup)
	if _, err := c.BackupAccess(ctx); !errors.Is(err, ErrBackupOff) {
		t.Fatalf("plan without backup: %v", err)
	}
}

func TestStatusJSONShape(t *testing.T) {
	st := newMemStore()
	c := &Client{Store: st}
	raw, _ := json.Marshal(c.Status(context.Background()))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"linked", "serverId", "serverName", "accountEmailMasked", "plan", "status", "periodEnd", "features", "quotas", "remote", "push", "backup", "signIn"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
	if m["signIn"] != nil {
		t.Errorf("signIn filled by the client in %s", raw)
	}
	if _, ok := m["link"]; ok {
		t.Error("link present without a pending code")
	}
	if m["periodEnd"] != nil || m["features"] == nil || len(m["features"].([]any)) != 0 {
		t.Errorf("periodEnd/features in %s", raw)
	}
	b := m["backup"].(map[string]any)
	if v, ok := b["lastUploadAt"]; !ok || v != nil {
		t.Errorf("lastUploadAt in %s", raw)
	}
	r := m["remote"].(map[string]any)
	if r["state"] != "off" || r["enabled"] != true || r["url"] != "" || r["error"] != "" {
		t.Errorf("remote in %s", raw)
	}
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("zero time in %s", raw)
	}
}

func TestClientRunsRelayWhileItApplies(t *testing.T) {
	f := newFakeCloud(t)
	d, o := newFakeDO(t), newTestOrigin(t)
	var helloFeatures atomic.Value
	helloFeatures.Store([]string{"relay", "push", "backup"})
	hello := func(features ...string) []byte {
		b, _ := json.Marshal(Entitlement{ServerID: "k7f3q9x2ab", Plan: "cloud", Status: "active", Features: features,
			RelayHost: "k7f3q9x2ab-cloud.example", RelayURL: d.url()})
		return b
	}
	d.hello = func() []byte { return hello(helloFeatures.Load().([]string)...) }
	f.ent.Features = []string{"relay"}
	f.ent.RelayURL = d.url()
	st := newMemStore()
	linkNow(t, st, f.ent)
	c := &Client{BaseURL: f.srv.URL, Store: st, Version: "9.9.9-test", Platform: "mac", AllowInsecure: true, Origin: o.srv.URL,
		RetryInactiveAfter: 300 * time.Millisecond,
		Tune: func(r *Relay, b *Backup) {
			if r != nil {
				r.MinBackoff, r.MaxBackoff = 10*time.Millisecond, 50*time.Millisecond
			}
		}}
	entitlements := make(chan Entitlement, 8)
	c.OnEntitlement = func(e Entitlement) { entitlements <- e }
	unlinked := make(chan string, 1)
	c.OnUnlinked = func(r string) { unlinked <- r }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)

	conn := d.next(t)
	waitFor(t, "connected", func() bool { return c.Status(ctx).Remote.State == StateConnected })
	// The HELLO's entitlement is stored.
	if e := recv(t, entitlements); !e.Has(FeatureBackup) || !c.Entitlement(ctx).Has(FeaturePush) {
		t.Fatalf("hello entitlement %+v", e)
	}
	// Requests reach the origin.
	conn.sendJSON(FrameReq, 1, reqHead{M: "GET", P: "/hello"})
	conn.expect(t, 1, FrameRes)
	conn.body(t, 1)

	// Remote access off: the relay stops.
	_ = c.SetRemote(ctx, false)
	for range conn.frames {
	}
	if s := c.Status(ctx).Remote; s.State != StateOff || s.Enabled {
		t.Fatalf("off: %+v", s)
	}
	_ = c.SetRemote(ctx, true)
	conn = d.next(t)
	recv(t, entitlements)

	// A HELLO without the relay feature stops it: inactive.
	conn.send(FrameHello, 0, hello("push"))
	recv(t, entitlements)
	for range conn.frames {
	}
	waitFor(t, "inactive", func() bool { return c.Status(ctx).Remote.State == StateInactive })

	// The control plane lists the relay again: reconnect right away.
	helloFeatures.Store([]string{"relay"})
	start := time.Now()
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	conn = d.next(t)
	if waited := time.Since(start); waited > 250*time.Millisecond {
		t.Fatalf("reconnected only after %v", waited)
	}
	recv(t, entitlements) // the refresh
	recv(t, entitlements) // the HELLO

	// 4003 while the entitlement still lists the relay: wait out the window.
	conn.closeWith(ClosePlanInactive, "plan inactive")
	start = time.Now()
	waitFor(t, "inactive window", func() bool {
		s := c.Status(ctx).Remote
		return s.State == StateInactive && strings.Contains(s.Error, "trying again")
	})
	conn = d.next(t)
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("reconnected after %v, inside the inactive window", waited)
	}
	recv(t, entitlements)

	// The status read refreshes a stale entitlement (at most every maxAge).
	_ = st.SetSetting(ctx, KeyEntitlementAt, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	c.RefreshIfOlder(ctx, 5*time.Minute)
	if at, _ := parseTime(st.Setting(ctx, KeyEntitlementAt, "")); time.Since(at) > time.Minute {
		t.Fatal("stale entitlement not refreshed")
	}
	_ = st.SetSetting(ctx, KeyEntitlementAt, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339))
	c.RefreshIfOlder(ctx, 5*time.Minute) // throttled
	if at, _ := parseTime(st.Setting(ctx, KeyEntitlementAt, "")); time.Since(at) < time.Minute {
		t.Fatal("on-demand refresh not throttled")
	}
	recv(t, entitlements)

	// 4001 unlinks.
	conn.closeWith(CloseTokenRevoked, "revoked")
	if r := recv(t, unlinked); r != "revoked" || c.Linked(ctx) {
		t.Fatalf("after 4001: %q linked=%v", r, c.Linked(ctx))
	}
}

func TestNextRefreshHonoursRefreshAfter(t *testing.T) {
	st := newMemStore()
	ent := Entitlement{ServerID: "k7f3q9x2ab", Plan: "cloud", Features: []string{"relay"}, RefreshAfter: time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)}
	linkNow(t, st, ent)
	c := &Client{Store: st}
	ctx := context.Background()
	if d := c.nextRefresh(ctx); d < 29*time.Minute || d > 30*time.Minute {
		t.Fatalf("refreshAfter in 30 min → %v", d)
	}
	ent.RefreshAfter = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	linkNow(t, st, ent)
	if d := c.nextRefresh(ctx); d != refreshMinDelay {
		t.Fatalf("past refreshAfter → %v", d)
	}
	ent.RefreshAfter = ""
	linkNow(t, st, ent)
	if d := c.nextRefresh(ctx); d != refreshEvery {
		t.Fatalf("no refreshAfter → %v", d)
	}
	c.failures = 3
	if d := c.nextRefresh(ctx); d != 4*refreshFailMin {
		t.Fatalf("3 failures → %v", d)
	}
	c.failures = 30
	if d := c.nextRefresh(ctx); d != refreshFailMax {
		t.Fatalf("many failures → %v", d)
	}
}

// MARK: - Wave 13: linking with a signed-in account, forced refresh, config

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// syncBuffer is a log sink that goroutines can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs sends slog's default logger, at debug level, to a buffer for
// the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// storeHolds reports whether any setting, sealed or not, contains s.
func storeHolds(st *memStore, s string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, v := range st.m {
		if strings.Contains(v, s) {
			return true
		}
		if plain, err := st.Unseal(v); err == nil && strings.Contains(plain, s) {
			return true
		}
	}
	return false
}

func TestClientLinkWithAccount(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeCloud(t)
	f.needApproval, f.jwt = true, testJWT
	st := newMemStore()
	c := newTestClient(t, f, st)
	type linked struct {
		who string
		e   Entitlement
	}
	got := make(chan linked, 2)
	c.OnLinked = func(who string, e Entitlement) { got <- linked{who, e} }
	ctx := context.Background()

	if err := c.LinkWithAccount(ctx, testJWT, "admin"); err != nil {
		t.Fatalf("link with account: %v", err)
	}
	l := recv(t, got)
	if l.who != "admin" || l.e.Plan != "cloud" || !l.e.Has(FeatureBackup) {
		t.Fatalf("OnLinked %+v", l)
	}
	f.mu.Lock()
	auths, code, polls, starts, start := append([]string(nil), f.approveAuth...), f.approveCode, f.polls, f.starts, f.start
	f.mu.Unlock()
	if starts != 1 || start["serverName"] != "Office Mac" || start["version"] != "9.9.9-test" || start["platform"] != "linux" {
		t.Fatalf("start %d %v", starts, start)
	}
	if len(auths) != 1 || auths[0] != "Bearer "+testJWT || code != "ABCD-EFGH" {
		t.Fatalf("approve: %v %q", auths, code)
	}
	if polls != 1 {
		t.Fatalf("collected after %d polls, want right away", polls)
	}
	// Saved exactly as the code flow saves it.
	if raw := st.Setting(ctx, KeyToken, ""); raw == "" || strings.Contains(raw, testToken) {
		t.Fatalf("token not sealed: %q", raw)
	}
	if tok, _ := st.Unseal(st.Setting(ctx, KeyToken, "")); tok != testToken {
		t.Fatal("sealed token doesn't open")
	}
	if st.Setting(ctx, KeyServerID, "") != "k7f3q9x2ab" || st.Setting(ctx, KeyEntitlementAt, "") == "" || st.Setting(ctx, KeyEntitlement, "") == "" {
		t.Fatal("server id or entitlement not saved")
	}
	s := c.Status(ctx)
	if !s.Linked || s.Link != nil || s.ServerID != "k7f3q9x2ab" || s.Plan != "cloud" || s.Status != "active" || s.AccountEmailMasked != "e***@example.com" {
		t.Fatalf("linked status %+v", s)
	}
	// The account token was used once and is kept nowhere.
	if storeHolds(st, testJWT) {
		t.Fatal("the account token is in the settings")
	}
	if out := logs.String(); strings.Contains(out, testJWT) || strings.Contains(out, "eyJhbGci") || !strings.Contains(out, "linked with an account") {
		t.Fatalf("logs: %s", out)
	}
	// Linked now: signing in again doesn't link a second time.
	if err := c.LinkWithAccount(ctx, testJWT, "admin"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("second link: %v", err)
	}
	f.mu.Lock()
	n := len(f.approveAuth)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("approved %d times", n)
	}
}

func TestClientLinkWithAccountDropsPendingCode(t *testing.T) {
	f := newFakeCloud(t)
	f.needApproval, f.jwt = true, testJWT
	st := newMemStore()
	c := newTestClient(t, f, st)
	ctx := context.Background()
	if _, err := c.Link(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if c.Status(ctx).Link == nil {
		t.Fatal("no pending code")
	}
	if err := c.LinkWithAccount(ctx, testJWT, "admin"); err != nil {
		t.Fatal(err)
	}
	if s := c.Status(ctx); !s.Linked || s.Link != nil || c.CancelLink() {
		t.Fatalf("after linking with an account: %+v", s)
	}
	f.mu.Lock()
	starts := f.starts
	f.mu.Unlock()
	if starts != 2 {
		t.Fatalf("%d starts", starts)
	}
}

func TestClientLinkWithAccountWaitsForTheToken(t *testing.T) {
	f := newFakeCloud(t)
	f.needApproval, f.jwt, f.pendingPoll, f.pollFail = true, testJWT, 2, 2
	st := newMemStore()
	c := newTestClient(t, f, st)
	if err := c.LinkWithAccount(context.Background(), testJWT, "admin"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	polls := f.polls
	f.mu.Unlock()
	if polls != 5 || !c.Linked(context.Background()) {
		t.Fatalf("%d polls (want 2 failures, 2 pending, then the token)", polls)
	}
}

func TestClientLinkWithAccountFinishesWhenTheCallerLeaves(t *testing.T) {
	f := newFakeCloud(t)
	f.needApproval, f.jwt = true, testJWT
	st := newMemStore()
	c := newTestClient(t, f, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onPoll = cancel // the browser went away right after the approval
	if err := c.LinkWithAccount(ctx, testJWT, "admin"); err != nil {
		t.Fatal(err)
	}
	if !c.Linked(context.Background()) {
		t.Fatal("not linked")
	}
}

func TestClientLinkWithAccountOneAtATime(t *testing.T) {
	f := newFakeCloud(t)
	f.needApproval, f.jwt = true, testJWT
	st := newMemStore()
	c := newTestClient(t, f, st)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- c.LinkWithAccount(context.Background(), testJWT, "admin") }()
	}
	a, b := recv(t, errs), recv(t, errs)
	if (a == nil) == (b == nil) || !errors.Is(errors.Join(a, b), ErrAlreadyLinked) {
		t.Fatalf("two links: %v / %v", a, b)
	}
	f.mu.Lock()
	n := len(f.approveAuth)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("approved %d times", n)
	}
}

func TestClientLinkWithAccountErrors(t *testing.T) {
	limit := map[string]any{"error": "Sentinel Cloud links one server. Upgrade to Sentinel Cloud Business to link more.", "upgradeURL": "https://site.example/cloud#plans"}
	cases := []struct {
		name    string
		setup   func(f *fakeCloud)
		jwt     string
		want    error
		message string // "" = don't check
		upgrade string
		calls   bool // the control plane was asked
	}{
		{name: "sign-in rejected", setup: func(f *fakeCloud) { f.jwt = "another.session.token" }, jwt: testJWT, want: ErrSignIn,
			message: "Sign in again: your session has expired.", calls: true},
		{name: "email not confirmed", setup: func(f *fakeCloud) {
			f.approveFail, f.approveBody = 403, map[string]any{"error": "Confirm your email address first, then enter the code again."}
		}, jwt: testJWT, want: ErrSignIn, message: "Confirm your Sentinel Cloud account's email address first, then sign in again", calls: true},
		{name: "server limit", setup: func(f *fakeCloud) { f.approveFail, f.approveBody = 402, limit }, jwt: testJWT, want: ErrServerLimit,
			message: limit["error"].(string), upgrade: "https://site.example/cloud#plans", calls: true},
		{name: "server limit, unusable upgrade address", setup: func(f *fakeCloud) {
			f.approveFail, f.approveBody = 402, map[string]any{"error": "no more", "upgradeURL": "javascript:alert(1)"}
		}, jwt: testJWT, want: ErrServerLimit, message: "no more", calls: true},
		{name: "start refused", setup: func(f *fakeCloud) { f.startStatus = 503 }, jwt: testJWT, want: ErrUnreachable,
			message: "Sentinel Cloud: start refused", calls: true},
		{name: "approve fails", setup: func(f *fakeCloud) { f.approveFail, f.approveBody = 500, map[string]any{"error": "boom"} }, jwt: testJWT,
			want: ErrUnreachable, message: "Sentinel Cloud: boom", calls: true},
		{name: "code gone before collecting", setup: func(f *fakeCloud) { f.pollGone = true }, jwt: testJWT, want: ErrUnreachable, calls: true},
		{name: "malformed server token", setup: func(f *fakeCloud) { f.token = "scs_otherid234_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA_" },
			jwt: testJWT, want: ErrUnreachable, message: "Sentinel Cloud sent a malformed server token", calls: true},
		{name: "unreachable", setup: func(f *fakeCloud) { f.srv.Close() }, jwt: testJWT, want: ErrUnreachable},
		{name: "not a token", jwt: "not a token\r\nX-Evil: 1", want: ErrSignIn},
		{name: "empty token", jwt: "", want: ErrSignIn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCloud(t)
			f.needApproval, f.jwt = true, testJWT
			if tc.setup != nil {
				tc.setup(f)
			}
			st := newMemStore()
			c := newTestClient(t, f, st)
			linked := false
			c.OnLinked = func(string, Entitlement) { linked = true }
			err := c.LinkWithAccount(context.Background(), tc.jwt, "admin")
			var le *LinkError
			if !errors.Is(err, tc.want) || !errors.As(err, &le) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.message != "" && err.Error() != tc.message {
				t.Fatalf("message %q, want %q", err.Error(), tc.message)
			}
			if le.UpgradeURL != tc.upgrade {
				t.Fatalf("upgrade URL %q", le.UpgradeURL)
			}
			if linked || c.Linked(context.Background()) || st.Setting(context.Background(), KeyToken, "") != "" {
				t.Fatal("linked anyway")
			}
			if strings.Contains(err.Error(), testJWT) || storeHolds(st, testJWT) {
				t.Fatal("the account token leaked")
			}
			f.mu.Lock()
			asked := f.startCalls > 0 || len(f.approveAuth) > 0
			f.mu.Unlock()
			if tc.calls != asked {
				t.Fatalf("control plane asked: %v", asked)
			}
		})
	}

	// 429: Sentinel Cloud's own answer, to pass on as "slow down".
	f := newFakeCloud(t)
	f.approveFail, f.approveBody = 429, map[string]any{"error": "Too many attempts. Wait a minute and try again."}
	c := newTestClient(t, f, newMemStore())
	var api *APIError
	if err := c.LinkWithAccount(context.Background(), testJWT, "admin"); !errors.As(err, &api) || api.Status != 429 || api.Message != "Too many attempts. Wait a minute and try again." {
		t.Fatalf("429: %v", err)
	}
}

func TestClientCollectGivesUp(t *testing.T) {
	f := newFakeCloud(t)
	f.needApproval = true // never approved: pending until the deadline
	c := newTestClient(t, f, newMemStore())
	if _, err := c.call(context.Background(), http.MethodPost, f.srv.URL+"/v1/link/start", "", map[string]string{"platform": "linux"}, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.collect(ctx, "device-code-1")
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "in time") {
		t.Fatalf("collect: %v", err)
	}
}

func TestClientForceRefresh(t *testing.T) {
	f := newFakeCloud(t)
	st := newMemStore()
	clock := newTestClock()
	c := &Client{BaseURL: f.srv.URL, Store: st, AllowInsecure: true, Now: clock.Now}
	ctx := context.Background()
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.entCalls
	}
	setPlan := func(p string) {
		f.mu.Lock()
		f.ent.Plan = p
		f.mu.Unlock()
	}
	if err := c.ForceRefresh(ctx); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("unlinked: %v", err)
	}
	linkNow(t, st, f.ent)
	setPlan("business")
	// The entitlement is fresh, so the on-demand refresh does nothing …
	c.RefreshIfOlder(ctx, 5*time.Minute)
	if calls() != 0 {
		t.Fatal("on-demand refresh of a fresh entitlement")
	}
	// … and the forced one fetches it anyway.
	if err := c.ForceRefresh(ctx); err != nil || calls() != 1 || c.Entitlement(ctx).Plan != "business" {
		t.Fatalf("forced: %v, %d calls, %+v", err, calls(), c.Entitlement(ctx))
	}
	// At most once per 10 seconds.
	setPlan("cloud")
	clock.Add(9 * time.Second)
	if err := c.ForceRefresh(ctx); err != nil || calls() != 1 || c.Entitlement(ctx).Plan != "business" {
		t.Fatalf("inside 10 s: %v, %d calls", err, calls())
	}
	clock.Add(time.Second)
	if err := c.ForceRefresh(ctx); err != nil || calls() != 2 || c.Entitlement(ctx).Plan != "cloud" {
		t.Fatalf("after 10 s: %v, %d calls", err, calls())
	}
	// The on-demand throttle doesn't hold it back.
	_ = st.SetSetting(ctx, KeyEntitlementAt, formatTime(clock.Now().Add(-10*time.Minute)))
	c.RefreshIfOlder(ctx, 5*time.Minute)
	clock.Add(10 * time.Second)
	if err := c.ForceRefresh(ctx); err != nil || calls() != 4 {
		t.Fatalf("after an on-demand refresh: %v, %d calls", err, calls())
	}
	// Failures come back; a 401 marks the token rejected as Refresh does.
	f.mu.Lock()
	f.entStatus = 401
	f.mu.Unlock()
	clock.Add(10 * time.Second)
	if err := c.ForceRefresh(ctx); err == nil || calls() != 5 || c.Status(ctx).Status != "inactive" || c.Status(ctx).Remote.Error != RejectedMessage {
		t.Fatalf("401: %v %+v", err, c.Status(ctx))
	}
}

// testConfig is GET /v1/config as the contract has it.
func testConfig() map[string]any {
	return map[string]any{
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
}

func TestClientConfig(t *testing.T) {
	f := newFakeCloud(t)
	f.config = testConfig()
	clock := newTestClock()
	c := &Client{BaseURL: f.srv.URL, Store: newMemStore(), AllowInsecure: true, Now: clock.Now}
	ctx := context.Background()
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.configCalls
	}

	cfg := c.Config(ctx)
	if cfg == nil || cfg.SupabaseURL != "https://project.supabase.example" || cfg.SupabaseAnonKey != "sb_publishable_test" || cfg.SiteBase != "https://site.example" ||
		!strings.HasSuffix(cfg.CheckoutURL, "/sentinel-create-checkout") || !strings.HasSuffix(cfg.PortalURL, "/sentinel-portal") || len(cfg.Plans) != 2 ||
		cfg.Plans[0] != (Plan{ID: "cloud", Name: "Sentinel Cloud", Month: 4.99, Year: 49, Servers: 1, BackupGB: 10, Days: 30}) ||
		cfg.Plans[1] != (Plan{ID: "business", Name: "Sentinel Cloud Business", Month: 19.99, Year: 199, Servers: 5, BackupGB: 100, Days: 90}) {
		t.Fatalf("config %+v", cfg)
	}
	// It serializes back to exactly the control plane's object.
	var back, want map[string]any
	raw, _ := json.Marshal(cfg)
	_ = json.Unmarshal(raw, &back)
	wraw, _ := json.Marshal(testConfig())
	_ = json.Unmarshal(wraw, &want)
	if !jsonEqual(back, want) {
		t.Fatalf("round trip\n got %s\nwant %s", raw, wraw)
	}

	// Cached for an hour; each caller gets its own copy.
	cfg.Plans[0].Name, cfg.SupabaseURL = "changed", "changed"
	clock.Add(59 * time.Minute)
	if again := c.Config(ctx); again == nil || again.Plans[0].Name != "Sentinel Cloud" || again.SupabaseURL != "https://project.supabase.example" || calls() != 1 {
		t.Fatalf("cached: %+v, %d calls", again, calls())
	}
	f.mu.Lock()
	f.config.(map[string]any)["siteBase"] = "https://new.example"
	f.mu.Unlock()
	clock.Add(time.Minute + time.Second)
	if fresh := c.Config(ctx); fresh == nil || fresh.SiteBase != "https://new.example" || calls() != 2 {
		t.Fatalf("after an hour: %+v, %d calls", fresh, calls())
	}

	// Failing: nil, and not asked again for a minute.
	f.mu.Lock()
	f.configFail = 503
	f.mu.Unlock()
	clock.Add(time.Hour + time.Second)
	if c.Config(ctx) != nil || calls() != 3 {
		t.Fatalf("failing: %d calls", calls())
	}
	if c.Config(ctx) != nil || calls() != 3 {
		t.Fatalf("asked again right after a failure: %d calls", calls())
	}
	f.mu.Lock()
	f.configFail = 0
	f.mu.Unlock()
	clock.Add(time.Minute)
	if c.Config(ctx) == nil || calls() != 4 {
		t.Fatalf("recovered: %d calls", calls())
	}

	// A caller that gives up doesn't count as Sentinel Cloud failing.
	c2 := &Client{BaseURL: f.srv.URL, Store: newMemStore(), AllowInsecure: true}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if c2.Config(gone) != nil {
		t.Fatal("config with a cancelled context")
	}
	if c2.Config(ctx) == nil {
		t.Fatal("a cancelled caller blocked the next fetch")
	}

	// Unreachable: nil.
	f.srv.Close()
	c3 := &Client{BaseURL: f.srv.URL, Store: newMemStore(), AllowInsecure: true}
	if c3.Config(ctx) != nil {
		t.Fatal("config from a closed control plane")
	}
}

func TestClientCachedConfigNeverWaits(t *testing.T) {
	f := newFakeCloud(t)
	f.config = testConfig()
	gate := make(chan struct{})
	f.configGate = gate // Sentinel Cloud is slow to answer
	clock := newTestClock()
	c := &Client{BaseURL: f.srv.URL, Store: newMemStore(), AllowInsecure: true, Now: clock.Now}
	calls := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.configCalls
	}
	if c.PeekConfig() != nil || calls() != 0 {
		t.Fatal("peek fetched")
	}
	// Nothing cached: nil right away, one fetch in the background however
	// many callers ask meanwhile.
	start := time.Now()
	for range 10 {
		if c.CachedConfig() != nil {
			t.Fatal("config before the fetch finished")
		}
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %v for a slow control plane", waited)
	}
	waitFor(t, "the background fetch", func() bool { return calls() == 1 })
	close(gate)
	waitFor(t, "the cached config", func() bool { return c.CachedConfig() != nil })
	if calls() != 1 || c.PeekConfig() == nil || c.PeekConfig().SupabaseAnonKey != "sb_publishable_test" {
		t.Fatalf("%d fetches", calls())
	}
	// Over an hour old: the old one still comes back while a fresh one is fetched.
	f.mu.Lock()
	f.config.(map[string]any)["siteBase"] = "https://new.example"
	f.mu.Unlock()
	clock.Add(time.Hour)
	if cfg := c.CachedConfig(); cfg == nil || cfg.SiteBase != "https://site.example" {
		t.Fatalf("stale: %+v", cfg)
	}
	waitFor(t, "the refreshed config", func() bool { cfg := c.CachedConfig(); return cfg != nil && cfg.SiteBase == "https://new.example" })
	if calls() != 2 {
		t.Fatalf("%d fetches", calls())
	}
	// Failing: nil once the background fetch fails, and not asked again for a minute.
	f.mu.Lock()
	f.configFail = 503
	f.mu.Unlock()
	clock.Add(time.Hour)
	_ = c.CachedConfig()
	waitFor(t, "the failed fetch", func() bool { return c.PeekConfig() == nil })
	for range 3 {
		if c.CachedConfig() != nil {
			t.Fatal("config after a failure")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if calls() != 3 {
		t.Fatalf("%d fetches inside the minute after a failure", calls())
	}
	f.mu.Lock()
	f.configFail = 0
	f.mu.Unlock()
	clock.Add(time.Minute)
	_ = c.CachedConfig()
	waitFor(t, "the recovered config", func() bool { return c.CachedConfig() != nil })
	if calls() != 4 {
		t.Fatalf("%d fetches", calls())
	}
}

// jsonEqual compares decoded JSON values.
func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}

func TestClientConfigRejectsUnusable(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"valid":                    nil,
		"no plans":                 func(m map[string]any) { delete(m, "plans") },
		"no sign-in service":       func(m map[string]any) { delete(m, "supabaseUrl") },
		"no key":                   func(m map[string]any) { m["supabaseAnonKey"] = "" },
		"sign-in over plain HTTP":  func(m map[string]any) { m["supabaseUrl"] = "http://project.supabase.example" },
		"checkout not a web page":  func(m map[string]any) { m["checkoutURL"] = "javascript:alert(1)" },
		"portal over plain HTTP":   func(m map[string]any) { m["portalURL"] = "http://evil.example/portal" },
		"site base not an address": func(m map[string]any) { m["siteBase"] = "not a url" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			body := testConfig()
			if mutate != nil {
				mutate(body)
			}
			// HTTPS, without AllowInsecure, as in production.
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/config" {
					writeErr(w, 404, "not found")
					return
				}
				writeJSONT(w, 200, body)
			}))
			defer srv.Close()
			c := &Client{BaseURL: srv.URL, Store: newMemStore(), HTTP: srv.Client()}
			cfg := c.Config(context.Background())
			switch name {
			case "valid":
				if cfg == nil || len(cfg.Plans) != 2 {
					t.Fatalf("valid config refused: %+v", cfg)
				}
			case "no plans":
				if cfg == nil || cfg.Plans == nil || len(cfg.Plans) != 0 {
					t.Fatalf("no plans: %+v", cfg)
				}
			default:
				if cfg != nil {
					t.Fatalf("accepted %+v", cfg)
				}
			}
		})
	}
	// Not JSON at all.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html>") }))
	defer srv.Close()
	if cfg := (&Client{BaseURL: srv.URL, Store: newMemStore(), HTTP: srv.Client()}).Config(context.Background()); cfg != nil {
		t.Fatalf("HTML accepted: %+v", cfg)
	}
}
