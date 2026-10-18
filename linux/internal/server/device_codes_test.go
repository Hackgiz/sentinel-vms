package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// issueDeviceCode starts a device sign-in from ip and returns the grant.
func issueDeviceCode(t *testing.T, h http.Handler, ip, name string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v2/device-codes", strings.NewReader(`{"deviceName":"`+name+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = ip + ":5555"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

// rewindLastPoll pretends the device last polled `ago` earlier, so tests
// don't sleep through the interval.
func rewindLastPoll(t *testing.T, s *Server, ago time.Duration) {
	t.Helper()
	if _, err := s.Store.DB.Exec(`UPDATE device_codes SET last_poll_at = ?`, time.Now().Add(-ago).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func pollToken(t *testing.T, h http.Handler, deviceCode string) (int, map[string]any) {
	t.Helper()
	return v2Call(t, h, "POST", "/api/v2/device-codes/token", "", `{"deviceCode":"`+deviceCode+`"}`)
}

func TestDeviceCodeFullFlowViewerCapped(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	admin := v2SignIn(t, s, "boss", "Admin")
	adminTok := admin["accessToken"].(string)

	code, g := issueDeviceCode(t, h, "10.0.0.5", "Lobby TV")
	if code != 200 {
		t.Fatalf("issue: %d %v", code, g)
	}
	userCode, deviceCode := g["userCode"].(string), g["deviceCode"].(string)
	if len(userCode) != 9 || userCode[4] != '-' || store.NormalizeUserCode(userCode) == "" {
		t.Fatalf("user code %q", userCode)
	}
	if strings.ContainsAny(userCode, "AEIOU01LB8") {
		t.Fatalf("ambiguous characters in %q", userCode)
	}
	if g["expiresIn"].(float64) != 600 || g["interval"].(float64) != 5 || !strings.HasSuffix(g["verifyUrl"].(string), "/link") {
		t.Fatalf("grant %v", g)
	}

	// Hashed at rest: neither code appears in the table.
	var uh, dh, name string
	if err := s.Store.DB.QueryRow(`SELECT user_code_hash, device_code_hash, device_name FROM device_codes`).Scan(&uh, &dh, &name); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(deviceCode))
	if uh == store.NormalizeUserCode(userCode) || dh == deviceCode || dh != hex.EncodeToString(sum[:]) || name != "Lobby TV" {
		t.Fatalf("stored %q %q %q", uh, dh, name)
	}

	// First poll: pending. Immediate second poll: slow_down.
	if code, m := pollToken(t, h, deviceCode); code != 400 || errCode(m) != "authorization_pending" {
		t.Fatalf("pending: %d %v", code, m)
	}
	if code, m := pollToken(t, h, deviceCode); code != 400 || errCode(m) != "slow_down" {
		t.Fatalf("slow_down: %d %v", code, m)
	}
	rewindLastPoll(t, s, 6*time.Second)
	if code, m := pollToken(t, h, deviceCode); code != 400 || errCode(m) != "authorization_pending" {
		t.Fatalf("pending after waiting: %d %v", code, m)
	}

	// Approval needs a signed-in user.
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/approve", "", `{"userCode":"`+userCode+`"}`); code != 401 {
		t.Fatalf("anonymous approve: %d", code)
	}
	// Lookup shows the device name; typed codes are forgiving.
	typed := strings.ToLower(strings.ReplaceAll(userCode, "-", " "))
	code, info := v2Call(t, h, "POST", "/api/v2/device-codes/lookup", adminTok, `{"userCode":"`+typed+`"}`)
	if code != 200 || info["deviceName"] != "Lobby TV" || info["role"] != "Viewer" {
		t.Fatalf("lookup: %d %v", code, info)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/device-codes/approve", adminTok, `{"userCode":"`+userCode+`"}`); code != 200 {
		t.Fatalf("approve: %d %v", code, m)
	}
	// Decided once.
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/approve", adminTok, `{"userCode":"`+userCode+`"}`); code != 404 {
		t.Fatalf("second approve: %d", code)
	}

	rewindLastPoll(t, s, 6*time.Second)
	code, tok := pollToken(t, h, deviceCode)
	if code != 200 || tok["tokenType"] != "Bearer" {
		t.Fatalf("redeem: %d %v", code, tok)
	}
	access := tok["accessToken"].(string)

	// Single use: the device code is spent.
	rewindLastPoll(t, s, 6*time.Second)
	if code, m := pollToken(t, h, deviceCode); code != 400 || errCode(m) != "expired_token" {
		t.Fatalf("second redeem: %d %v", code, m)
	}

	// Viewer cap: an Admin's TV may watch but not manage.
	code, me := v2Call(t, h, "GET", "/api/v2/me", access, "")
	if code != 200 || me["name"] != "boss" || me["role"] != "Viewer" {
		t.Fatalf("me: %d %v", code, me)
	}
	if perms := me["permissions"].([]any); len(perms) != 1 || perms[0] != "viewLive" {
		t.Fatalf("permissions %v", perms)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/cameras", access, ""); code != 200 {
		t.Fatalf("cameras: %d", code)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/users", access, ""); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("users as TV: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/users", adminTok, ""); code != 200 {
		t.Fatalf("users as admin: %d", code)
	}
	// The cap survives a refresh.
	code, tok2 := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+tok["refreshToken"].(string)+`"}`)
	if code != 200 {
		t.Fatalf("refresh: %d %v", code, tok2)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/users", tok2["accessToken"].(string), ""); code != 403 {
		t.Fatalf("users after refresh: %d %v", code, m)
	}

	// Audited: issue, approve, redeem.
	for _, action := range []string{"Issued device sign-in code", "Approved device sign-in", "Signed in"} {
		var n int
		if err := s.Store.DB.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = ?`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("no audit entry %q", action)
		}
	}
}

func TestDeviceCodeDenyAndExpiry(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	op := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)

	_, g := issueDeviceCode(t, h, "10.0.0.6", "Desk TV")
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/deny", op, `{"userCode":"`+g["userCode"].(string)+`"}`); code != 200 {
		t.Fatalf("deny: %d", code)
	}
	if code, m := pollToken(t, h, g["deviceCode"].(string)); code != 400 || errCode(m) != "access_denied" {
		t.Fatalf("denied poll: %d %v", code, m)
	}

	// An expired code can be neither approved nor redeemed.
	_, g2 := issueDeviceCode(t, h, "10.0.0.6", "Desk TV")
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/approve", op, `{"userCode":"`+g2["userCode"].(string)+`"}`); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if _, err := s.Store.DB.Exec(`UPDATE device_codes SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if code, m := pollToken(t, h, g2["deviceCode"].(string)); code != 400 || errCode(m) != "expired_token" {
		t.Fatalf("expired poll: %d %v", code, m)
	}
	_, g3 := issueDeviceCode(t, h, "10.0.0.6", "Desk TV")
	if _, err := s.Store.DB.Exec(`UPDATE device_codes SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/approve", op, `{"userCode":"`+g3["userCode"].(string)+`"}`); code != 404 {
		t.Fatalf("approve expired: %d", code)
	}
	if code, m := pollToken(t, h, "sdc_nonsense"); code != 400 || errCode(m) != "expired_token" {
		t.Fatalf("unknown code: %d %v", code, m)
	}
}

func TestDeviceCodeIssueRateLimitedPerIP(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	for i := 0; i < deviceCodeIssueLimit; i++ {
		if code, m := issueDeviceCode(t, h, "10.0.0.7", "TV"); code != 200 {
			t.Fatalf("issue %d: %d %v", i, code, m)
		}
	}
	if code, m := issueDeviceCode(t, h, "10.0.0.7", "TV"); code != 429 || errCode(m) != "too_many_requests" {
		t.Fatalf("over limit: %d %v", code, m)
	}
	if code, _ := issueDeviceCode(t, h, "10.0.0.8", "TV"); code != 200 {
		t.Fatalf("other address: %d", code)
	}
}

func TestDeviceCodeGuessLimit(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	op := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)
	_, g := issueDeviceCode(t, h, "10.0.0.9", "TV")
	for i := 0; i < deviceCodeGuessLimit; i++ {
		if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/lookup", op, `{"userCode":"CCCC-DDDD"}`); code != 404 {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	// Locked out even for the right code.
	if code, m := v2Call(t, h, "POST", "/api/v2/device-codes/lookup", op, `{"userCode":"`+g["userCode"].(string)+`"}`); code != 429 {
		t.Fatalf("after guesses: %d %v", code, m)
	}
}

func TestDeviceCodeKeepsApproverScope(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	admin := v2SignIn(t, s, "admin", "Admin")["accessToken"].(string)
	op := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)

	// Two cameras; the operator sees only the one in their group.
	mk := func(name string) string {
		code, c := v2Call(t, h, "POST", "/api/v2/cameras", admin, `{"name":"`+name+`","rtspUrl":"rtsp://10.0.0.1/`+name+`"}`)
		if code != 201 && code != 200 {
			t.Fatalf("camera: %d %v", code, c)
		}
		return c["id"].(string)
	}
	inside, outside := mk("inside"), mk("outside")
	code, grp := v2Call(t, h, "POST", "/api/v2/camera-groups", admin, `{"name":"Lobby","cameraIds":["`+inside+`"]}`)
	if code != 201 && code != 200 {
		t.Fatalf("group: %d %v", code, grp)
	}
	u, err := s.Store.UserByName(ctx, "op")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{grp["id"].(string)}}); err != nil {
		t.Fatal(err)
	}

	_, g := issueDeviceCode(t, h, "10.0.0.10", "TV")
	if code, _ := v2Call(t, h, "POST", "/api/v2/device-codes/approve", op, `{"userCode":"`+g["userCode"].(string)+`"}`); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	code, tok := pollToken(t, h, g["deviceCode"].(string))
	if code != 200 {
		t.Fatalf("redeem: %d %v", code, tok)
	}
	tv := tok["accessToken"].(string)
	if code, _ := v2Call(t, h, "GET", "/api/v2/cameras/"+inside, tv, ""); code != 200 {
		t.Fatalf("in-scope camera: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/cameras/"+outside, tv, ""); code != 404 {
		t.Fatalf("out-of-scope camera: %d", code)
	}
	// Operators may acknowledge alarms; their TV may not.
	if code, _ := v2Call(t, h, "POST", "/api/v2/alarms/nope/state", tv, `{"state":"Acknowledged"}`); code != 403 {
		t.Fatalf("TV acknowledging: %d", code)
	}
}
