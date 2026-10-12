package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel-linux/internal/directory/directorytest"
	"sentinel-linux/internal/store"
)

const (
	testGuards = "CN=Guards,OU=Groups,DC=corp,DC=example"
	testAdmins = "CN=VMS Admins,OU=Groups,DC=corp,DC=example"
)

// ldapSite is a server with a fake directory and LDAP sign-in turned on
// (Guards → Operator scoped to the "Warehouse" camera group). It returns
// an Admin's access token and the group id.
func ldapSite(t *testing.T) (*Server, *directorytest.LDAP, string, string) {
	t.Helper()
	s := newCompanionServer(t)
	f := &directorytest.LDAP{
		BindDN: "CN=svc,DC=corp,DC=example", BindPassword: "svc-secret",
		Users: []directorytest.User{
			{Name: "alice", DN: "CN=Alice,OU=People,DC=corp,DC=example", Password: "alice-pw", Groups: []string{testGuards}},
			{Name: "bob", DN: "CN=Bob,OU=People,DC=corp,DC=example", Password: "bob-pw", Groups: []string{"CN=Cafeteria,DC=corp,DC=example"}},
			{Name: "root", DN: "CN=Root,OU=People,DC=corp,DC=example", Password: "root-pw", Groups: []string{testAdmins}},
		},
	}
	s.dir.ldapDial = f.Dial
	g, err := s.Store.SaveCameraGroup(context.Background(), "", "Warehouse", nil)
	if err != nil {
		t.Fatal(err)
	}
	admin := v2SignIn(t, s, "admin", "Admin")["accessToken"].(string)
	body := `{"ldap":{"enabled":true,"url":"ldaps://dc1.corp.example","bindDn":"CN=svc,DC=corp,DC=example","bindPassword":"svc-secret",
		"userBaseDn":"DC=corp,DC=example","userFilter":"(uid={name})",
		"roleMappings":[{"group":"` + testAdmins + `","role":"Admin"},{"group":"` + testGuards + `","role":"Operator","groupIds":["` + g.ID + `"]}]},
		"oidc":{"enabled":false,"issuer":"","clientId":""}}`
	if code, m := v2Call(t, s.Handler(), "PUT", "/api/v2/directory", admin, body); code != 200 {
		t.Fatalf("save settings: %d %v", code, m)
	}
	return s, f, admin, g.ID
}

func TestDirectorySettingsSecretsSealedAndKept(t *testing.T) {
	s, _, admin, _ := ldapSite(t)
	h := s.Handler()
	code, m := v2Call(t, h, "GET", "/api/v2/directory", admin, "")
	ldap := m["ldap"].(map[string]any)
	if code != 200 || ldap["hasBindPassword"] != true || ldap["bindPassword"] != nil || ldap["groupAttribute"] != "memberOf" {
		t.Fatalf("get: %d %v", code, m)
	}
	ctx := context.Background()
	if raw := s.Store.Setting(ctx, ldapSetting, "") + s.Store.Setting(ctx, ldapBindPasswordSetting, ""); strings.Contains(raw, "svc-secret") {
		t.Fatal("bind password stored in the clear")
	}

	// Saving what GET returned (no secret) keeps the stored secret.
	ldap["url"] = "ldaps://dc2.corp.example"
	body, _ := json.Marshal(m)
	if code, m := v2Call(t, h, "PUT", "/api/v2/directory", admin, string(body)); code != 200 {
		t.Fatalf("resave: %d %v", code, m)
	}
	if got := s.ldapSettings(ctx); got.BindPassword != "svc-secret" || got.URL != "ldaps://dc2.corp.example" {
		t.Fatalf("after resave: %+v", got)
	}
	if !auditHas(t, s, "Changed LDAP sign-in") {
		t.Fatal("settings change not audited")
	}

	// Validation, permissions.
	bad := strings.Replace(string(body), "ldaps://dc2.corp.example", "ldap://dc2.corp.example", 1)
	if code, m := v2Call(t, h, "PUT", "/api/v2/directory", admin, bad); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("plaintext LDAP: %d %v", code, m)
	}
	op := v2SignIn(t, s, "op", "Operator")["accessToken"].(string)
	if code, _ := v2Call(t, h, "GET", "/api/v2/directory", op, ""); code != 403 {
		t.Fatalf("operator read settings: %d", code)
	}

	// Public providers
	code, m = v2Call(t, h, "GET", "/api/v2/auth/providers", "", "")
	if code != 200 || m["local"] != true || m["ldap"] != true || m["oidc"].(map[string]any)["enabled"] != false {
		t.Fatalf("providers: %d %v", code, m)
	}
}

func auditHas(t *testing.T, s *Server, action string) bool {
	t.Helper()
	entries, err := s.Store.AuditLog(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

func v1Login(t *testing.T, h http.Handler, name, password string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/login", strings.NewReader(`{"name":"`+name+`","password":"`+password+`"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLDAPSignInProvisionsUserWithRoleAndScope(t *testing.T) {
	s, _, admin, groupID := ldapSite(t)
	h := s.Handler()
	ctx := context.Background()

	w := v1Login(t, h, "alice", "alice-pw")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"Operator"`) {
		t.Fatalf("v1 login: %d %s", w.Code, w.Body)
	}
	u, err := s.Store.UserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if src, _ := s.Store.UserSource(ctx, u.ID); src != store.SourceLDAP {
		t.Fatalf("source %q", src)
	}
	if sc, _ := s.Store.UserScope(ctx, u.ID); sc.AllCameras || len(sc.GroupIDs) != 1 || sc.GroupIDs[0] != groupID {
		t.Fatalf("scope %+v", sc)
	}
	// The same person again (v2, different case) is the same user.
	code, tok := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"ALICE","password":"alice-pw"}`)
	if code != 200 || tok["user"].(map[string]any)["id"] != u.ID {
		t.Fatalf("v2 password grant: %d %v", code, tok)
	}
	if !auditHas(t, s, "Added directory user") {
		t.Fatal("provisioning not audited")
	}

	// Admin mapping: all cameras.
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"root","password":"root-pw"}`); code != 200 || m["user"].(map[string]any)["role"] != "Admin" {
		t.Fatalf("admin mapping: %d %v", code, m)
	}

	// Directory users' passwords can't be set here.
	if code, m := v2Call(t, h, "PATCH", "/api/v2/users/"+u.ID, admin, `{"password":"a-new-password"}`); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("set directory user's password: %d %v", code, m)
	}
	// …and the unusable local hash never signs them in once LDAP is off.
	off := `{"ldap":{"enabled":false,"url":""},"oidc":{"enabled":false,"issuer":"","clientId":""}}`
	if code, m := v2Call(t, h, "PUT", "/api/v2/directory", admin, off); code != 200 {
		t.Fatalf("turn off: %d %v", code, m)
	}
	if w := v1Login(t, h, "alice", "alice-pw"); w.Code != 401 {
		t.Fatalf("LDAP off: %d", w.Code)
	}
}

func TestLDAPSignInRefusals(t *testing.T) {
	s, f, _, _ := ldapSite(t)
	h := s.Handler()
	ctx := context.Background()

	// Unmapped: refused, not provisioned, audited.
	code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"bob","password":"bob-pw"}`)
	if code != 401 || !strings.Contains(m["error"].(map[string]any)["message"].(string), "role") {
		t.Fatalf("unmapped: %d %v", code, m)
	}
	if _, err := s.Store.UserByName(ctx, "bob"); err == nil {
		t.Fatal("unmapped user was provisioned")
	}
	if w := v1Login(t, h, "alice", "wrong"); w.Code != 401 {
		t.Fatalf("wrong password: %d", w.Code)
	}

	// A local user with the name wins; the directory isn't asked.
	hash := "$2a$10$abcdefghijklmnopqrstuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuuu" // never matches
	if _, err := s.Store.CreateUser(ctx, "root", "Viewer", hash); err != nil {
		t.Fatal(err)
	}
	n := len(f.Binds)
	if w := v1Login(t, h, "root", "root-pw"); w.Code != 401 || len(f.Binds) != n {
		t.Fatalf("local user shadowed by directory: %d, binds %d → %d", w.Code, n, len(f.Binds))
	}

	// Directory down: 503, doesn't count towards the lockout.
	f.Down = true
	for i := 0; i < 6; i++ {
		if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"zed","password":"x"}`); code != 503 || errCode(m) != "unavailable" {
			t.Fatalf("down: %d %v", code, m)
		}
	}
	f.Down = false

	// Same lockout as local sign-in.
	for i := 0; i < 5; i++ {
		v1Login(t, h, "alice", "wrong")
	}
	if w := v1Login(t, h, "alice", "alice-pw"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("lockout: %d", w.Code)
	}
}

func TestLDAPRefreshRechecksDirectory(t *testing.T) {
	s, f, _, _ := ldapSite(t)
	h := s.Handler()
	code, tok := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"password","name":"alice","password":"alice-pw"}`)
	if code != 200 {
		t.Fatalf("sign in: %d %v", code, tok)
	}
	code, tok = v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+tok["refreshToken"].(string)+`"}`)
	if code != 200 {
		t.Fatalf("refresh: %d %v", code, tok)
	}
	// alice leaves the Guards group: the next refresh is refused and her
	// sign-ins end.
	f.Users[0].Groups = nil
	if code, m := v2Call(t, h, "POST", "/api/v2/auth/token", "", `{"grantType":"refresh","refreshToken":"`+tok["refreshToken"].(string)+`"}`); code != 401 {
		t.Fatalf("refresh after removal: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "GET", "/api/v2/me", tok["accessToken"].(string), ""); code != 401 {
		t.Fatalf("access token survived: %d", code)
	}
}

func TestDirectoryTestEndpoint(t *testing.T) {
	s, _, admin, _ := ldapSite(t)
	h := s.Handler()
	settings := `{"enabled":false,"url":"ldaps://dc1.corp.example","bindDn":"CN=svc,DC=corp,DC=example","userBaseDn":"DC=corp,DC=example","userFilter":"(uid={name})",
		"roleMappings":[{"group":"` + testGuards + `","role":"Viewer"}]}`
	// The stored bind password is used when none is sent.
	code, m := v2Call(t, h, "POST", "/api/v2/directory/test", admin, `{"settings":`+settings+`,"name":"alice","password":"alice-pw"}`)
	if code != 200 || m["ok"] != true || m["role"] != "Viewer" || len(m["groups"].([]any)) != 1 {
		t.Fatalf("test ok: %d %v", code, m)
	}
	code, m = v2Call(t, h, "POST", "/api/v2/directory/test", admin, `{"settings":`+settings+`,"name":"bob","password":"bob-pw"}`)
	if code != 200 || m["ok"] != false || m["groups"] == nil {
		t.Fatalf("test unmapped: %d %v", code, m)
	}
	// Nothing was saved.
	if got := s.ldapSettings(context.Background()); !got.Enabled || got.RoleMappings[0].Role != "Admin" {
		t.Fatalf("test changed the settings: %+v", got)
	}
}
