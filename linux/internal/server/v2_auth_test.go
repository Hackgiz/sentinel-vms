package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel-linux/internal/auth"
)

// v2Call sends a v2 request with an optional bearer token and decodes the
// JSON answer into a map.
func v2Call(t *testing.T, h http.Handler, method, path, bearer, body string) (int, map[string]any) {
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
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// v2SignIn creates a user and signs in over v2; returns the token response.
func v2SignIn(t *testing.T, s *Server, name, role string) map[string]any {
	t.Helper()
	hash, _ := auth.HashPassword("correct horse battery")
	if _, err := s.Store.CreateUser(context.Background(), name, role, hash); err != nil {
		t.Fatal(err)
	}
	code, m := v2Call(t, s.Handler(), "POST", "/api/v2/auth/token", "",
		`{"grantType":"password","name":"`+name+`","password":"correct horse battery","client":"test"}`)
	if code != 200 {
		t.Fatalf("sign in: %d %v", code, m)
	}
	return m
}

func TestV2TokenSignInMeAndRefresh(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	tok := v2SignIn(t, s, "op", "Operator")
	access, refresh := tok["accessToken"].(string), tok["refreshToken"].(string)
	if tok["tokenType"] != "Bearer" || !strings.HasPrefix(access, "sat_") || !strings.HasPrefix(refresh, "srt_") {
		t.Fatalf("token response %v", tok)
	}
	if exp := int64(tok["expiresAt"].(float64)); exp < time.Now().Add(14*time.Minute).UnixMilli() || exp > time.Now().Add(16*time.Minute).UnixMilli() {
		t.Fatalf("access expiry %d", exp)
	}

	code, me := v2Call(t, h, "GET", "/api/v2/me", access, "")
	if code != 200 || me["name"] != "op" || me["role"] != "Operator" {
		t.Fatalf("me: %d %v", code, me)
	}
	perms := me["permissions"].([]any)
	if len(perms) != 4 { // acknowledgeAlarms, exportEvidence, viewLive, controlPtz
		t.Fatalf("operator permissions %v", perms)
	}

	// Refresh rotates both tokens; the old access token stops working.
	code, tok2 := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+refresh+`"}`)
	if code != 200 || tok2["accessToken"] == access || tok2["refreshToken"] == refresh {
		t.Fatalf("refresh: %d %v", code, tok2)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/me", access, ""); code != 401 || errCode(m) != "unauthorized" {
		t.Fatalf("old access token: %d %v", code, m)
	}
	access2 := tok2["accessToken"].(string)
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", access2, ""); code != 200 {
		t.Fatalf("new access token: %d", code)
	}

	// Replaying the rotated refresh token revokes the whole sign-in.
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+refresh+`"}`); code != 401 || errCode(m) != "token_reused" {
		t.Fatalf("reuse: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", access2, ""); code != 401 {
		t.Fatalf("family not revoked: %d", code)
	}
}

func TestV2TokenErrorsAndRevoke(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	tok := v2SignIn(t, s, "admin", "Admin")
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"admin","password":"nope"}`); code != 401 || errCode(m) != "unauthorized" {
		t.Fatalf("wrong password: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"magic"}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("bad grant: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","extra":1}`); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("unknown field: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/me", "", ""); code != 401 || errCode(m) != "unauthorized" {
		t.Fatalf("anonymous: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/nope", tok["accessToken"].(string), ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unknown endpoint: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "POST", "/api/v2/auth/revoke", "", `{"refreshToken":"`+tok["refreshToken"].(string)+`"}`); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", tok["accessToken"].(string), ""); code != 401 {
		t.Fatalf("revoked sign-in still works: %d", code)
	}
}

func TestV2AcceptsDashboardSession(t *testing.T) {
	s := newCompanionServer(t)
	ctx := context.Background()
	u, _ := s.Store.CreateUser(ctx, "viewer", "Viewer", "x")
	cookie, _ := s.Store.CreateSession(ctx, u.ID, time.Hour)
	r := httptest.NewRequest("GET", "/api/v2/me", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"permissions":["viewLive"]`) {
		t.Fatalf("cookie: %d %s", w.Code, w.Body)
	}
}

func TestV2OpenAPIServed(t *testing.T) {
	s := newCompanionServer(t)
	r := httptest.NewRequest("GET", "/api/v2/openapi.yaml", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "openapi: 3.1.0") {
		t.Fatalf("%d %.40s", w.Code, w.Body)
	}
}

// Native and Electron clients send their own Origin; bearer calls and token
// exchanges carry no cookie, so they are not refused as cross-site. Cookie
// calls still are.
func TestV2CrossOriginRules(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	tok := v2SignIn(t, s, "admin", "Admin")
	send := func(path, bearer, cookie, body string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "app://sentinel-operator")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := send("/api/v2/auth/token", "", "", `{"grantType":"password","name":"admin","password":"correct horse battery"}`); c != 200 {
		t.Fatalf("token exchange from an app origin: %d", c)
	}
	if c := send("/api/v2/auth/revoke", tok["accessToken"].(string), "", `{"refreshToken":"x"}`); c != 204 {
		t.Fatalf("bearer call from an app origin: %d", c)
	}
	if c := send("/api/v1/logout", "", "whatever", `{}`); c != http.StatusForbidden {
		t.Fatalf("cookie call from another origin: %d", c)
	}
}
