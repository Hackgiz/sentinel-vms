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

	"sentinel-linux/internal/store"
	"sentinel-linux/internal/webhooks"
)

type hookSink struct {
	mu   sync.Mutex
	hits []hookHit
	srv  *httptest.Server
}
type hookHit struct {
	body []byte
	sig  string
}

func newHookSink(t *testing.T, status int) *hookSink {
	t.Helper()
	h := &hookSink{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.hits = append(h.hits, hookHit{b, r.Header.Get(webhooks.SignatureHeader)})
		h.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hookSink) wait(t *testing.T, n int) []hookHit {
	t.Helper()
	for i := 0; i < 300; i++ {
		h.mu.Lock()
		got := append([]hookHit(nil), h.hits...)
		h.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %d webhook calls", n)
	return nil
}

func (h *hookSink) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.hits)
}

func TestWebhookCRUDSecretWriteOnlyAndTest(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	op := v2Token(t, s, "op", "Operator")
	sink := newHookSink(t, 200)

	body := `{"name":"Ops","url":"` + sink.srv.URL + `","secret":"topsecret-value","events":["alarm.new"],"enabled":true}`
	if code, _ := v2Call(t, h, "POST", "/api/v2/webhooks", op, body); code != 403 {
		t.Fatalf("operator: %d", code)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Bad","url":"ftp://x"}`); code != 400 {
		t.Fatalf("bad url: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Bad","url":"`+sink.srv.URL+`","events":["nope"]}`); code != 400 {
		t.Fatalf("bad event: %d %v", code, m)
	}
	code, m := v2Call(t, h, "POST", "/api/v2/webhooks", admin, body)
	if code != 201 || m["hasSecret"] != true {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["id"].(string)

	// No GET body, and no audit entry, ever contains the secret.
	assertNoSecret := func(where string) {
		t.Helper()
		for _, path := range []string{"/api/v2/webhooks", "/api/v2/rules", "/api/v2/audit"} {
			rec := do(t, h, "GET", path, admin, "")
			if strings.Contains(rec.Body.String(), "topsecret-value") || strings.Contains(rec.Body.String(), "newsecret") {
				t.Fatalf("%s: secret leaked in GET %s: %s", where, path, rec.Body)
			}
		}
		var sealedInDB string
		_ = s.Store.DB.QueryRow(`SELECT secret FROM webhooks WHERE id = ?`, id).Scan(&sealedInDB)
		if strings.Contains(sealedInDB, "topsecret") {
			t.Fatalf("%s: secret stored in the clear", where)
		}
	}
	assertNoSecret("after create")

	// PUT without a secret keeps it; with one replaces it; "" clears.
	code, m = v2Call(t, h, "PUT", "/api/v2/webhooks/"+id, admin, `{"name":"Ops 2","url":"`+sink.srv.URL+`","events":[],"enabled":true}`)
	if code != 200 || m["hasSecret"] != true || m["name"] != "Ops 2" {
		t.Fatalf("keep: %d %v", code, m)
	}
	assertNoSecret("after keep")
	code, m = v2Call(t, h, "PUT", "/api/v2/webhooks/"+id, admin, `{"name":"Ops 2","url":"`+sink.srv.URL+`","secret":"newsecret","events":[]}`)
	if code != 200 || m["hasSecret"] != true {
		t.Fatalf("replace: %d %v", code, m)
	}
	assertNoSecret("after replace")

	// Test delivery is signed with the stored secret.
	code, m = v2Call(t, h, "POST", "/api/v2/webhooks/"+id+"/test", admin, "")
	if code != 200 || m["ok"] != true {
		t.Fatalf("test: %d %v", code, m)
	}
	hits := sink.wait(t, 1)
	if hits[0].sig != webhooks.Sign(hits[0].body, "newsecret") {
		t.Fatalf("signature %q for %s", hits[0].sig, hits[0].body)
	}
	var p webhooks.Payload
	_ = json.Unmarshal(hits[0].body, &p)
	if p.Event != "webhook.test" {
		t.Fatalf("payload %s", hits[0].body)
	}
	code, m = v2Call(t, h, "GET", "/api/v2/webhooks", admin, "")
	got := items(t, m)[0]
	if got["lastStatus"] != "ok (200)" || got["lastFiredAt"].(float64) == 0 {
		t.Fatalf("status not recorded: %v", got)
	}
	assertNoSecret("after test")

	// A failing endpoint reports ok:false, not an HTTP error.
	bad := newHookSink(t, 500)
	_, m = v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Down","url":"`+bad.srv.URL+`"}`)
	code, m = v2Call(t, h, "POST", "/api/v2/webhooks/"+m["id"].(string)+"/test", admin, "")
	if code != 200 || m["ok"] != false || m["status"] != "failed: HTTP 500" {
		t.Fatalf("failing test: %d %v", code, m)
	}

	if code, _ := v2Call(t, h, "DELETE", "/api/v2/webhooks/"+id, admin, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/webhooks/"+id, admin, ""); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
}

func TestDeleteInUseIs409(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	sink := newHookSink(t, 200)

	_, wh := v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"W","url":"`+sink.srv.URL+`"}`)
	_, tp := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"Nights","weekly":[{"days":[1],"start":"22:00","end":"06:00"}]}`)
	ruleBody := `{"name":"Night person","enabled":true,"cameraIds":[],"kinds":["Person"],"minSeverity":"Info","schedule":false,"fromMinute":0,"toMinute":0,"days":[],"delivery":"dashboard","instructions":"","timeProfileId":"` +
		tp["id"].(string) + `","actions":[{"type":"webhook","webhookId":"` + wh["id"].(string) + `"}]}`
	code, rule := v2Call(t, h, "POST", "/api/v2/rules", admin, ruleBody)
	if code != 201 || rule["timeProfileId"] != tp["id"] {
		t.Fatalf("rule: %d %v", code, rule)
	}
	code, m := v2Call(t, h, "DELETE", "/api/v2/time-profiles/"+tp["id"].(string), admin, "")
	if code != 409 || errCode(m) != "conflict" || !strings.Contains(m["error"].(map[string]any)["message"].(string), "Night person") {
		t.Fatalf("profile in use: %d %v", code, m)
	}
	if code, m = v2Call(t, h, "DELETE", "/api/v2/webhooks/"+wh["id"].(string), admin, ""); code != 409 {
		t.Fatalf("webhook in use: %d %v", code, m)
	}
	// Dangling references are rejected on save.
	if code, _ := v2Call(t, h, "POST", "/api/v2/rules", admin, strings.Replace(ruleBody, tp["id"].(string), "nope", 1)); code != 400 {
		t.Fatalf("unknown profile: %d", code)
	}
	// Free them and both delete.
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/rules/"+rule["id"].(string), admin, ""); code != 204 {
		t.Fatalf("delete rule: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/time-profiles/"+tp["id"].(string), admin, ""); code != 204 {
		t.Fatalf("delete profile: %d", code)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/webhooks/"+wh["id"].(string), admin, ""); code != 204 {
		t.Fatalf("delete webhook: %d", code)
	}
}

func TestTimeProfileRoutes(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2Token(t, s, "boss", "Admin")
	sup := v2Token(t, s, "sup", "Supervisor")
	if code, _ := v2Call(t, h, "GET", "/api/v2/time-profiles", sup, ""); code != 403 {
		t.Fatalf("supervisor: %d", code)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"X","weekly":[{"days":[1],"start":"9:00","end":"10:00"}]}`); code != 400 {
		t.Fatalf("bad time: %d %v", code, m)
	}
	code, m := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"Office","weekly":[{"days":[1,2],"start":"08:00","end":"17:00"}]}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"office","weekly":[]}`); code != 409 {
		t.Fatalf("duplicate: %d", code)
	}
	id := m["id"].(string)
	code, m = v2Call(t, h, "PUT", "/api/v2/time-profiles/"+id, admin, `{"name":"Office hours","weekly":[]}`)
	if code != 200 || m["name"] != "Office hours" || len(m["weekly"].([]any)) != 0 {
		t.Fatalf("update: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/time-profiles/missing", admin, `{"name":"a","weekly":[]}`); code != 404 {
		t.Fatalf("missing: %d", code)
	}
	_, m = v2Call(t, h, "GET", "/api/v2/time-profiles", admin, "")
	if len(items(t, m)) != 1 {
		t.Fatal(m)
	}
}

func TestRuleGatedByTimeProfile(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	h := s.Handler()
	ctx := context.Background()
	admin := v2Token(t, s, "boss", "Admin")
	c := addCam(t, s)

	// 2024-06-03 is a Monday.
	mon := func(hh int) time.Time { return time.Date(2024, 6, 3, hh, 0, 0, 0, time.UTC) }
	_, tp := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"Mon 9-17","weekly":[{"days":[1],"start":"09:00","end":"17:00"}]}`)
	rule := `{"name":"Gated","enabled":true,"cameraIds":[],"kinds":["Person"],"minSeverity":"Info","schedule":false,"fromMinute":0,"toMinute":0,"days":[],"delivery":"dashboard","instructions":"Look!","timeProfileId":"` + tp["id"].(string) + `"}`
	code, r := v2Call(t, h, "POST", "/api/v2/rules", admin, rule)
	if code != 201 {
		t.Fatal(code, r)
	}
	instr := func(at time.Time) int {
		a := &store.Alarm{CameraID: c.ID, Kind: "Person", Severity: "Critical", CreatedAt: at.Unix()}
		return len(s.alarmInstructions(ctx, a))
	}
	if instr(mon(12)) != 1 || instr(mon(20)) != 0 || instr(mon(12).AddDate(0, 0, 1)) != 0 {
		t.Fatal("rule must fire only inside its profile")
	}
	// Editing the profile takes effect immediately (the rule cache is invalidated).
	v2Call(t, h, "PUT", "/api/v2/time-profiles/"+tp["id"].(string), admin, `{"name":"Mon 9-17","weekly":[{"days":[1],"start":"19:00","end":"21:00"}]}`)
	if instr(mon(12)) != 0 || instr(mon(20)) != 1 {
		t.Fatal("profile edit not applied")
	}
	// A rule without a profile is unchanged.
	v2Call(t, h, "PUT", "/api/v2/rules/"+r["id"].(string), admin, strings.Replace(rule, `,"timeProfileId":"`+tp["id"].(string)+`"`, "", 1))
	if instr(mon(12)) != 1 || instr(mon(3)) != 1 {
		t.Fatal("absent timeProfileId means today's behavior")
	}
}

func TestRuleWebhookActionAndSubscriptionFire(t *testing.T) {
	s := newCompanionServer(t)
	s.rules.loc = time.UTC
	h := s.Handler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	admin := v2Token(t, s, "boss", "Admin")
	c := addCam(t, s)
	go s.RunRules(ctx)
	time.Sleep(30 * time.Millisecond) // bus hook registered

	action, sub := newHookSink(t, 200), newHookSink(t, 200)
	_, wa := v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Action","url":"`+action.srv.URL+`","secret":"k1"}`)
	v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Sub","url":"`+sub.srv.URL+`","events":["alarm.new"]}`)
	off := newHookSink(t, 200)
	v2Call(t, h, "POST", "/api/v2/webhooks", admin, `{"name":"Off","url":"`+off.srv.URL+`","events":["alarm.new"],"enabled":false}`)
	// Time profile that is never active in this test: the rule must stay silent.
	_, never := v2Call(t, h, "POST", "/api/v2/time-profiles", admin, `{"name":"Never","weekly":[]}`)
	mkRule := func(name, profile string) string {
		extra := ""
		if profile != "" {
			extra = `,"timeProfileId":"` + profile + `"`
		}
		code, r := v2Call(t, h, "POST", "/api/v2/rules", admin, `{"name":"`+name+`","enabled":true,"cameraIds":[],"kinds":["Person"],"minSeverity":"Info","schedule":false,"fromMinute":0,"toMinute":0,"days":[],"delivery":"dashboard","instructions":""`+extra+`,"actions":[{"type":"webhook","webhookId":"`+wa["id"].(string)+`"}]}`)
		if code != 201 {
			t.Fatal(code, r)
		}
		return r["id"].(string)
	}
	gated := mkRule("Gated", never["id"].(string))
	ruleID := mkRule("Open", "")
	_ = gated

	if _, _, err := s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person", Title: "Person detected",
		Severity: "Critical", At: time.Now(), Origin: "test"}); err != nil {
		t.Fatal(err)
	}
	ah := action.wait(t, 1)
	sh := sub.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	if action.count() != 1 || sub.count() != 1 || off.count() != 0 {
		t.Fatalf("deliveries: action=%d sub=%d off=%d", action.count(), sub.count(), off.count())
	}
	var p webhooks.Payload
	if err := json.Unmarshal(ah[0].body, &p); err != nil || p.Event != "alarm.new" || p.Camera != c.Name || p.RuleID != ruleID || p.TS == 0 || p.Server == "" {
		t.Fatalf("action payload %s", ah[0].body)
	}
	if ah[0].sig != webhooks.Sign(ah[0].body, "k1") {
		t.Fatal("action delivery not signed")
	}
	if sh[0].sig != "" {
		t.Fatal("secretless webhook was signed")
	}
	var sp webhooks.Payload
	_ = json.Unmarshal(sh[0].body, &sp)
	if sp.RuleID != "" || sp.Event != "alarm.new" {
		t.Fatalf("subscription delivery carries a ruleId: %s", sh[0].body)
	}
	// last_status gets recorded.
	for i := 0; i < 100; i++ {
		_, m := v2Call(t, h, "GET", "/api/v2/webhooks", admin, "")
		done := false
		for _, w := range items(t, m) {
			if w["id"] == wa["id"] && w["lastStatus"] == "ok (200)" {
				done = true
			}
		}
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("last status not updated")
}
