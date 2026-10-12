package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/store"
)

// fakeIssuer is an OpenID provider: discovery, JWKS, and a token endpoint
// that checks the client, redirect URI and PKCE verifier before signing
// an ID token. Tests play the browser: approve() stands in for the user
// signing in at the provider and returns the authorization code.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]issuerGrant
}

type issuerGrant struct {
	challenge, redirectURI, nonce string
	claims                        map[string]any
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key, codes: map[string]issuerGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		u := f.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": u, "authorization_endpoint": u + "/authorize", "token_endpoint": u + "/token", "jwks_uri": u + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"code"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		f.mu.Lock()
		g, found := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case id != "sentinel" || secret != "idp-secret":
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		case !found || r.Form.Get("redirect_uri") != g.redirectURI || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		claims := map[string]any{"iss": f.srv.URL, "aud": "sentinel", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": g.nonce}
		for k, v := range g.claims {
			claims[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": f.sign(t, claims)})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) sign(t *testing.T, claims map[string]any) string {
	b64 := base64.RawURLEncoding.EncodeToString
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	body, _ := json.Marshal(claims)
	signing := b64(hdr) + "." + b64(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

// approve checks the authorization request the server redirected to and
// returns a code for claims (a "nonce" claim overrides the request's).
func (f *fakeIssuer) approve(t *testing.T, authURL string, claims map[string]any) (code, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || !strings.HasPrefix(authURL, f.srv.URL+"/authorize?") {
		t.Fatalf("authorization URL %q", authURL)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "sentinel" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("authorization request %v", q)
	}
	g := issuerGrant{challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri"), nonce: q.Get("nonce"), claims: claims}
	if n, ok := claims["nonce"].(string); ok {
		g.nonce = n
	}
	code = "code-" + q.Get("state")[:8]
	f.mu.Lock()
	f.codes[code] = g
	f.mu.Unlock()
	return code, q.Get("state")
}

// ssoSite: OIDC on with the fake issuer; "vms-ops" → Operator scoped to a
// camera group, "vms-admins" → Admin.
func ssoSite(t *testing.T) (*Server, *fakeIssuer, string) {
	t.Helper()
	s := newCompanionServer(t)
	f := newFakeIssuer(t)
	g, err := s.Store.SaveCameraGroup(context.Background(), "", "Lobby", nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := v2SignIn(t, s, "admin", "Admin")["accessToken"].(string)
	body := `{"ldap":{"enabled":false,"url":""},"oidc":{"enabled":true,"displayName":"Sign in with Example ID","issuer":"` + f.srv.URL + `",
		"clientId":"sentinel","clientSecret":"idp-secret","publicUrl":"https://vms.example",
		"roleMappings":[{"group":"vms-admins","role":"Admin"},{"group":"vms-ops","role":"Operator","groupIds":["` + g.ID + `"]}]}}`
	if code, m := v2Call(t, s.Handler(), "PUT", "/api/v2/directory", admin, body); code != 200 {
		t.Fatalf("save: %d %v", code, m)
	}
	return s, f, g.ID
}

// browserGet sends a GET with cookies and returns the response.
func browserGet(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startSSO calls /start and returns the provider URL and the flow cookie.
func startSSO(t *testing.T, h http.Handler, query string) (string, *http.Cookie) {
	t.Helper()
	w := browserGet(h, "/api/v2/auth/oidc/start?"+query)
	c := cookieNamed(w, ssoCookie)
	if w.Code != http.StatusFound || c == nil || c.SameSite != http.SameSiteLaxMode || !c.HttpOnly {
		t.Fatalf("start: %d %s %v", w.Code, w.Body, c)
	}
	return w.Header().Get("Location"), c
}

func callback(h http.Handler, code, state string, c *http.Cookie) *httptest.ResponseRecorder {
	q := url.Values{"code": {code}, "state": {state}}
	if c == nil {
		return browserGet(h, "/api/v2/auth/oidc/callback?"+q.Encode())
	}
	return browserGet(h, "/api/v2/auth/oidc/callback?"+q.Encode(), c)
}

func TestSSOWebFlow(t *testing.T) {
	s, f, groupID := ssoSite(t)
	h := s.Handler()
	ctx := context.Background()

	code, m := v2Call(t, h, "GET", "/api/v2/auth/providers", "", "")
	if o := m["oidc"].(map[string]any); code != 200 || o["enabled"] != true || o["displayName"] != "Sign in with Example ID" {
		t.Fatalf("providers: %d %v", code, m)
	}

	authURL, fc := startSSO(t, h, "returnTo="+url.QueryEscape("/live?cam=2"))
	if q, _ := url.Parse(authURL); q.Query().Get("redirect_uri") != "https://vms.example/api/v2/auth/oidc/callback" {
		t.Fatalf("callback URL from publicUrl: %s", authURL)
	}
	idpCode, state := f.approve(t, authURL, map[string]any{"sub": "u-123", "preferred_username": "dana", "groups": []string{"staff", "vms-ops"}})
	w := callback(h, idpCode, state, fc)
	session := cookieNamed(w, sessionCookie)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/live?cam=2" || session == nil || session.Value == "" {
		t.Fatalf("callback: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if me := browserGet(h, "/api/v1/me", session); me.Code != 200 || !strings.Contains(me.Body.String(), `"name":"dana"`) || !strings.Contains(me.Body.String(), `"role":"Operator"`) {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
	u, _ := s.Store.UserByName(ctx, "dana")
	if src, _ := s.Store.UserSource(ctx, u.ID); src != store.SourceOIDC {
		t.Fatalf("source %q", src)
	}
	if sc, _ := s.Store.UserScope(ctx, u.ID); sc.AllCameras || len(sc.GroupIDs) != 1 || sc.GroupIDs[0] != groupID {
		t.Fatalf("scope %+v", sc)
	}
	// SSO users have no password here.
	if code, _ := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"dana","password":"anything-at-all"}`); code != 401 {
		t.Fatalf("password sign-in for an SSO user: %d", code)
	}

	// The state works once.
	if w := callback(h, idpCode, state, fc); w.Code != 400 {
		t.Fatalf("state reuse: %d", w.Code)
	}

	// Signing in again maps the role again (now an Admin), same user.
	authURL, fc = startSSO(t, h, "")
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "u-123", "preferred_username": "dana", "groups": "vms-admins"})
	if w := callback(h, idpCode, state, fc); w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("second sign-in: %d %v", w.Code, w.Header())
	}
	if u2, _ := s.Store.UserByName(ctx, "dana"); u2.ID != u.ID || u2.Role != "Admin" {
		t.Fatalf("re-mapped user %+v", u2)
	}
}

func TestSSONativeFlow(t *testing.T) {
	s, f, _ := ssoSite(t)
	h := s.Handler()
	authURL, fc := startSSO(t, h, "redirectUri="+url.QueryEscape("http://127.0.0.1:53111/cb")+"&state=client-xyz")
	idpCode, state := f.approve(t, authURL, map[string]any{"sub": "u-9", "preferred_username": "erin", "groups": []string{"vms-ops"}})
	w := callback(h, idpCode, state, fc)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc.Host != "127.0.0.1:53111" || loc.Path != "/cb" || loc.Query().Get("state") != "client-xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("native callback: %d %v", w.Code, w.Header())
	}
	if cookieNamed(w, sessionCookie) != nil {
		t.Fatal("native flow set a browser session")
	}
	code := loc.Query().Get("code")
	status, tok := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"oidc_code","code":"`+code+`","client":"Sentinel Operator"}`)
	if status != 200 || tok["user"].(map[string]any)["name"] != "erin" || tok["accessToken"] == nil {
		t.Fatalf("exchange: %d %v", status, tok)
	}
	if status, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"oidc_code","code":"`+code+`"}`); status != 401 || errCode(m) != "unauthorized" {
		t.Fatalf("code reuse: %d %v", status, m)
	}

	// The custom scheme works too.
	authURL, fc = startSSO(t, h, "redirectUri="+url.QueryEscape("sentinel-operator://signin"))
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "u-9", "preferred_username": "erin", "groups": []string{"vms-ops"}})
	if w := callback(h, idpCode, state, fc); !strings.HasPrefix(w.Header().Get("Location"), "sentinel-operator://signin?code=") {
		t.Fatalf("custom scheme: %v", w.Header())
	}
}

func TestSSORefusals(t *testing.T) {
	s, f, _ := ssoSite(t)
	h := s.Handler()
	ctx := context.Background()

	// Open redirects.
	for _, q := range []string{
		"returnTo=" + url.QueryEscape("//evil.example/"),
		"returnTo=" + url.QueryEscape("https://evil.example/"),
		"returnTo=" + url.QueryEscape(`/\evil.example`),
		"redirectUri=" + url.QueryEscape("https://evil.example/cb"),
		"redirectUri=" + url.QueryEscape("http://127.0.0.1@evil.example:80/"),
		"redirectUri=" + url.QueryEscape("http://192.168.1.5:8080/cb"),
	} {
		if w := browserGet(h, "/api/v2/auth/oidc/start?"+q); w.Code != 400 {
			t.Errorf("start %s: %d %v", q, w.Code, w.Header())
		}
	}

	// State: unknown, missing cookie, someone else's cookie.
	authURL, fc := startSSO(t, h, "")
	idpCode, state := f.approve(t, authURL, map[string]any{"sub": "x", "preferred_username": "x", "groups": []string{"vms-ops"}})
	if w := callback(h, idpCode, "forged-state", fc); w.Code != 400 {
		t.Fatalf("unknown state: %d", w.Code)
	}
	if w := callback(h, idpCode, state, nil); w.Code != 400 || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("no flow cookie: %d", w.Code)
	}
	authURL, _ = startSSO(t, h, "")
	_, other := startSSO(t, h, "")
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "x", "preferred_username": "x", "groups": []string{"vms-ops"}})
	if w := callback(h, idpCode, state, other); w.Code != 400 {
		t.Fatalf("other browser's cookie: %d", w.Code)
	}

	// Nonce mismatch (a replayed ID token): refused, no session.
	authURL, fc = startSSO(t, h, "")
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "x", "preferred_username": "x", "groups": []string{"vms-ops"}, "nonce": "stale"})
	w := callback(h, idpCode, state, fc)
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/?ssoError=") || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("nonce mismatch: %d %v", w.Code, w.Header())
	}

	// PKCE: the provider gets the verifier only from the server; a code
	// handed to a different flow fails the provider's PKCE check.
	authURL1, fc1 := startSSO(t, h, "")
	authURL2, fc2 := startSSO(t, h, "")
	code1, _ := f.approve(t, authURL1, map[string]any{"sub": "x", "preferred_username": "x", "groups": []string{"vms-ops"}})
	_, state2 := f.approve(t, authURL2, map[string]any{"sub": "x", "preferred_username": "x", "groups": []string{"vms-ops"}})
	_ = fc1
	if w := callback(h, code1, state2, fc2); !strings.HasPrefix(w.Header().Get("Location"), "/?ssoError=") || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("code from another flow: %d %v", w.Code, w.Header())
	}

	// Unmapped user: refused with a reason, not provisioned.
	authURL, fc = startSSO(t, h, "redirectUri="+url.QueryEscape("http://localhost:4000/done")+"&state=s1")
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "u-7", "preferred_username": "frank", "groups": []string{"staff"}})
	w = callback(h, idpCode, state, fc)
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Host != "localhost:4000" || loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" || loc.Query().Get("state") != "s1" {
		t.Fatalf("unmapped: %v", w.Header())
	}
	if _, err := s.Store.UserByName(ctx, "frank"); err == nil {
		t.Fatal("unmapped user provisioned")
	}

	// A local user's name can't be taken over through SSO.
	authURL, fc = startSSO(t, h, "")
	idpCode, state = f.approve(t, authURL, map[string]any{"sub": "evil", "preferred_username": "admin", "groups": []string{"vms-admins"}})
	if w := callback(h, idpCode, state, fc); cookieNamed(w, sessionCookie) != nil || !strings.HasPrefix(w.Header().Get("Location"), "/?ssoError=") {
		t.Fatalf("name takeover: %v", w.Header())
	}

	// Bad codes, and SSO off.
	if status, _ := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"oidc_code","code":"made-up"}`); status != 401 {
		t.Fatalf("made-up code: %d", status)
	}
	admin := v2SignIn(t, s, "admin2", "Admin")["accessToken"].(string)
	if code, m := v2Call(t, h, "PUT", "/api/v2/directory", admin, `{"ldap":{"enabled":false,"url":""},"oidc":{"enabled":false,"issuer":"","clientId":""}}`); code != 200 {
		t.Fatalf("turn off: %d %v", code, m)
	}
	if w := browserGet(h, "/api/v2/auth/oidc/start"); w.Code != 404 {
		t.Fatalf("start while off: %d", w.Code)
	}
	if code, m := v2Call(t, h, "GET", "/api/v2/directory", admin, ""); code != 200 || m["oidc"].(map[string]any)["hasClientSecret"] != true || m["oidc"].(map[string]any)["clientSecret"] != nil {
		t.Fatalf("secret kept and hidden: %v", m)
	}
}
