package directory_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sentinel-linux/internal/directory"
	"sentinel-linux/internal/directory/directorytest"
)

const (
	opsGroup    = "CN=Security Ops,OU=Groups,DC=corp,DC=example"
	guardsGroup = "CN=Guards,OU=Groups,DC=corp,DC=example"
)

func fakeDirectory() *directorytest.LDAP {
	return &directorytest.LDAP{
		BindDN: "CN=svc,DC=corp,DC=example", BindPassword: "svc-secret",
		Users: []directorytest.User{
			{Name: "alice", DN: "CN=Alice,OU=People,DC=corp,DC=example", Password: "alice-pw", Groups: []string{guardsGroup, opsGroup},
				GUID: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}},
			{Name: "bob", DN: "CN=Bob,OU=People,DC=corp,DC=example", Password: "bob-pw", Groups: []string{"CN=Cafeteria,DC=corp,DC=example"}},
			{Name: "carol", DN: "CN=Carol,OU=People,DC=corp,DC=example", Password: "carol-pw", Groups: []string{guardsGroup}, Disabled: true},
			{Name: "*", DN: "CN=Star,DC=corp,DC=example", Password: "star-pw", Groups: []string{guardsGroup}},
		},
	}
}

func client(f *directorytest.LDAP) *directory.LDAP {
	s := directory.LDAPSettings{
		Enabled: true, URL: "ldaps://dc1.corp.example", BindDN: f.BindDN, BindPassword: f.BindPassword,
		UserBaseDN: "DC=corp,DC=example", UserFilter: "(uid={name})",
		RoleMappings: []directory.RoleMapping{
			// lower case and extra spaces: DNs compare as the directory would
			{Group: "cn=security ops, ou=groups, dc=corp, dc=example", Role: "Supervisor"},
			{Group: guardsGroup, Role: "Operator", GroupIDs: []string{"g1"}},
		},
	}.WithDefaults()
	return &directory.LDAP{Settings: s, Dial: f.Dial}
}

func TestLDAPAuthenticateMapsFirstMatchingGroup(t *testing.T) {
	f := fakeDirectory()
	id, err := client(f).Authenticate(context.Background(), " alice ", "alice-pw")
	if err != nil {
		t.Fatal(err)
	}
	// alice is in both groups; the first mapping (Security Ops) wins even
	// though her memberOf lists Guards first.
	if id.Mapping.Role != "Supervisor" || id.Name != "alice" || id.ExternalID != "guid:0102030405060708090a0b0c0d0e0f10" || len(id.Groups) != 2 {
		t.Fatalf("identity %+v", id)
	}
	if !id.Mapping.Scope().AllCameras {
		t.Fatal("a mapping without groups should scope to all cameras")
	}
	// service account first, then the user
	if len(f.Binds) != 2 || f.Binds[0] != f.BindDN || f.Binds[1] != "CN=Alice,OU=People,DC=corp,DC=example" {
		t.Fatalf("binds %v", f.Binds)
	}
}

func TestLDAPAuthenticateRefusals(t *testing.T) {
	ctx := context.Background()
	f := fakeDirectory()
	c := client(f)
	if _, err := c.Authenticate(ctx, "alice", "wrong"); !errors.Is(err, directory.ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := c.Authenticate(ctx, "nobody", "x"); !errors.Is(err, directory.ErrBadCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	// An empty password would be an unauthenticated bind: refused before
	// the directory is asked.
	n := len(f.Binds)
	if _, err := c.Authenticate(ctx, "alice", ""); !errors.Is(err, directory.ErrBadCredentials) || len(f.Binds) != n {
		t.Fatalf("empty password: %v, binds %v", err, f.Binds)
	}
	id, err := c.Authenticate(ctx, "bob", "bob-pw")
	if !errors.Is(err, directory.ErrNoRole) || id == nil || len(id.Groups) != 1 {
		t.Fatalf("unmapped user: %v %+v", err, id)
	}
	if _, err := c.Authenticate(ctx, "carol", "carol-pw"); !errors.Is(err, directory.ErrDisabled) {
		t.Fatalf("disabled user: %v", err)
	}
	f.Down = true
	var ue *directory.UnavailableError
	if _, err := c.Authenticate(ctx, "alice", "alice-pw"); !errors.As(err, &ue) {
		t.Fatalf("directory down: %v", err)
	}
}

func TestLDAPFilterEscaping(t *testing.T) {
	ctx := context.Background()
	f := fakeDirectory()
	c := client(f)
	for name, want := range map[string]string{
		"*":                `(uid=\2a)`,
		"*)(uid=*":         `(uid=\2a\29\28uid=\2a)`,
		`a\b`:              `(uid=a\5cb)`,
		"x)(|(objectClass": `(uid=x\29\28|\28objectClass)`,
		"nul\x00":          `(uid=nul\00)`,
	} {
		_, _ = c.Authenticate(ctx, name, "pw")
		if got := f.LastFilter(); got != want {
			t.Errorf("name %q: filter %q, want %q", name, got, want)
		}
	}
	// "*" matches only the account literally named "*".
	id, err := c.Authenticate(ctx, "*", "star-pw")
	if err != nil || id.ExternalID != "dn:CN=Star,DC=corp,DC=example" {
		t.Fatalf("literal star: %v %+v", err, id)
	}
	if _, err := c.Authenticate(ctx, "*", "alice-pw"); !errors.Is(err, directory.ErrBadCredentials) {
		t.Fatalf("wildcard must not reach alice: %v", err)
	}
}

func TestLDAPLookup(t *testing.T) {
	f := fakeDirectory()
	id, err := client(f).Lookup(context.Background(), "alice")
	if err != nil || id.Mapping.Role != "Supervisor" {
		t.Fatalf("lookup: %v %+v", err, id)
	}
	if len(f.Binds) != 1 { // service account only
		t.Fatalf("binds %v", f.Binds)
	}
}

func TestLDAPSettingsValidate(t *testing.T) {
	ok := directory.LDAPSettings{
		Enabled: true, URL: "ldaps://dc1:636", UserBaseDN: "DC=corp,DC=example",
		RoleMappings: []directory.RoleMapping{{Group: guardsGroup, Role: "Viewer", GroupIDs: []string{"g1"}}},
	}
	exists := func(id string) bool { return id == "g1" }
	if err := ok.WithDefaults().Validate(exists); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*directory.LDAPSettings){
		"plain ldap":       func(s *directory.LDAPSettings) { s.URL = "ldap://dc1" },
		"ldaps + starttls": func(s *directory.LDAPSettings) { s.StartTLS = true },
		"http url":         func(s *directory.LDAPSettings) { s.URL = "http://dc1" },
		"no base":          func(s *directory.LDAPSettings) { s.UserBaseDN = "" },
		"filter no name":   func(s *directory.LDAPSettings) { s.UserFilter = "(uid=bob)" },
		"bad filter":       func(s *directory.LDAPSettings) { s.UserFilter = "(uid={name}" },
		"bind no password": func(s *directory.LDAPSettings) { s.BindDN = "CN=svc,DC=corp" },
		"bad role":         func(s *directory.LDAPSettings) { s.RoleMappings[0].Role = "Root" },
		"unknown group":    func(s *directory.LDAPSettings) { s.RoleMappings[0].GroupIDs = []string{"nope"} },
		"no mappings":      func(s *directory.LDAPSettings) { s.RoleMappings = nil },
		"bad attribute":    func(s *directory.LDAPSettings) { s.GroupAttribute = "member Of" },
	} {
		s := ok
		s.RoleMappings = append([]directory.RoleMapping(nil), ok.RoleMappings...)
		mut(&s)
		if err := s.WithDefaults().Validate(exists); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	plainTLS := ok
	plainTLS.URL, plainTLS.StartTLS = "ldap://dc1:389", true
	if err := plainTLS.WithDefaults().Validate(exists); err != nil {
		t.Errorf("ldap + StartTLS: %v", err)
	}
}

func TestOIDCSettingsDefaultsAndValidate(t *testing.T) {
	s := directory.OIDCSettings{Enabled: true, Issuer: "https://idp.example", ClientID: "sentinel", Scopes: []string{"groups"},
		RoleMappings: []directory.RoleMapping{{Group: "vms-admins", Role: "Admin"}}, PublicURL: "https://vms.example/"}.WithDefaults()
	if strings.Join(s.Scopes, " ") != "openid groups" || s.UsernameClaim != "preferred_username" || s.GroupsClaim != "groups" || s.PublicURL != "https://vms.example" {
		t.Fatalf("defaults %+v", s)
	}
	if err := s.Validate(nil); err != nil {
		t.Fatal(err)
	}
	bad := s
	bad.Issuer = "http://idp.example"
	if bad.Validate(nil) == nil {
		t.Error("plain http issuer accepted")
	}
	bad = s
	bad.PublicURL = "javascript:alert(1)"
	if bad.Validate(nil) == nil {
		t.Error("bad public URL accepted")
	}
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{"": "/", "/": "/", "/live?cam=1": "/live?cam=1"} {
		if got, ok := directory.SafeReturnTo(in); !ok || got != want {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"//evil.example", "https://evil.example/", `/\evil.example`, "evil", "/x\r\nSet-Cookie: a", "javascript:alert(1)", "///evil"} {
		if _, ok := directory.SafeReturnTo(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestSafeRedirectURI(t *testing.T) {
	for _, in := range []string{"http://127.0.0.1:53111/callback", "http://localhost:8080/", "sentinel-operator:/oauth", "sentinel-operator://signin"} {
		if !directory.SafeRedirectURI(in) {
			t.Errorf("%q refused", in)
		}
	}
	for _, in := range []string{"http://127.0.0.1/cb", "https://127.0.0.1:443/", "http://evil.example:80/", "http://127.0.0.1:80@evil.example/",
		"http://127.0.0.1.evil.example:80/", "http://localhost:0/", "http://[::1]:8080/", "javascript:alert(1)", "http://127.0.0.1:8080/#frag", ""} {
		if directory.SafeRedirectURI(in) {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestOIDCCodesAreOneTime(t *testing.T) {
	var o directory.OIDC
	code := o.IssueCode("u1")
	if id, ok := o.RedeemCode(code); !ok || id != "u1" {
		t.Fatal("first redeem failed")
	}
	if _, ok := o.RedeemCode(code); ok {
		t.Fatal("code redeemed twice")
	}
	if _, ok := o.RedeemCode(""); ok {
		t.Fatal("empty code accepted")
	}
}
