// Package directory is directory sign-in (PLATFORM.md, "Permissions"):
// names and passwords checked against LDAP or Active Directory, and single
// sign-on with an OpenID Connect identity provider. It turns a directory
// identity into a Sentinel role and camera scope through role mappings;
// the server provisions the user and signs them in.
//
// Settings are stored by the server (settings table, secrets sealed); this
// package validates them and talks to the directory.
package directory

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-ldap/ldap/v3"

	"sentinel-linux/internal/store"
)

// Sign-in outcomes callers map to answers.
var (
	// ErrBadCredentials: unknown name or wrong password (never says which).
	ErrBadCredentials = errors.New("wrong name or password")
	// ErrNoRole: the directory accepted the user but no role mapping
	// matches their groups.
	ErrNoRole = errors.New("your directory account isn't mapped to a Sentinel role — ask an administrator")
	// ErrDisabled: the directory account is disabled.
	ErrDisabled = errors.New("your directory account is disabled")
)

// UnavailableError: the directory couldn't be reached or answered with an
// error, so the outcome is unknown.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string { return "the directory is unavailable: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error { return e.Err }

// RoleMapping gives members of a directory group a role, scoped to camera
// groups (none = all cameras). The first mapping that matches wins.
type RoleMapping struct {
	Group    string   `json:"group"`
	Role     string   `json:"role"`
	GroupIDs []string `json:"groupIds"`
}

// Identity is a user the directory vouched for.
type Identity struct {
	// ExternalID is stable across renames: objectGUID or entryUUID when the
	// directory has one, else the DN (LDAP); issuer and subject (OIDC).
	ExternalID string
	Name       string
	Groups     []string
	// Mapping is the role mapping that matched (zero when none did).
	Mapping RoleMapping
}

// MatchRole returns the first mapping whose group the user is in. eq
// compares a mapping's group with one of the user's.
func MatchRole(mappings []RoleMapping, groups []string, eq func(a, b string) bool) (RoleMapping, bool) {
	for _, m := range mappings {
		for _, g := range groups {
			if eq(m.Group, g) {
				return m, true
			}
		}
	}
	return RoleMapping{}, false
}

// MARK: - Settings

// LDAPSettings is how to reach the directory and find users. BindPassword
// is stored sealed, apart from the rest.
type LDAPSettings struct {
	Enabled            bool          `json:"enabled"`
	URL                string        `json:"url"`
	StartTLS           bool          `json:"startTls"`
	InsecureSkipVerify bool          `json:"insecureSkipVerify"`
	BindDN             string        `json:"bindDn"`
	BindPassword       string        `json:"-"`
	UserBaseDN         string        `json:"userBaseDn"`
	UserFilter         string        `json:"userFilter"`
	GroupAttribute     string        `json:"groupAttribute"`
	RoleMappings       []RoleMapping `json:"roleMappings"`
}

// DefaultUserFilter finds Active Directory and OpenLDAP accounts.
const DefaultUserFilter = "(|(sAMAccountName={name})(uid={name}))"

// WithDefaults fills empty optional fields.
func (s LDAPSettings) WithDefaults() LDAPSettings {
	s.URL = strings.TrimSpace(s.URL)
	s.BindDN = strings.TrimSpace(s.BindDN)
	s.UserBaseDN = strings.TrimSpace(s.UserBaseDN)
	s.UserFilter = strings.TrimSpace(s.UserFilter)
	if s.UserFilter == "" {
		s.UserFilter = DefaultUserFilter
	}
	s.GroupAttribute = strings.TrimSpace(s.GroupAttribute)
	if s.GroupAttribute == "" {
		s.GroupAttribute = "memberOf"
	}
	s.RoleMappings = cleanMappings(s.RoleMappings)
	return s
}

var attributeName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// Validate checks settings that are about to be saved or tried.
// groupExists reports whether a camera group id exists.
func (s LDAPSettings) Validate(groupExists func(id string) bool) error {
	if err := validateMappings(s.RoleMappings, groupExists); err != nil {
		return err
	}
	if !s.Enabled && s.URL == "" {
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" || (u.Scheme != "ldap" && u.Scheme != "ldaps") {
		return errors.New("LDAP URL must look like ldaps://dc1.example.com:636 or ldap://dc1.example.com:389")
	}
	if u.Scheme == "ldaps" && s.StartTLS {
		return errors.New("StartTLS is for ldap:// URLs; ldaps:// is already encrypted")
	}
	if u.Scheme == "ldap" && !s.StartTLS {
		return errors.New("use ldaps:// or turn on StartTLS — passwords would otherwise cross the network unencrypted")
	}
	if !s.Enabled {
		return nil
	}
	if s.UserBaseDN == "" {
		return errors.New("enter the base DN to search for users in")
	}
	if _, err := ldap.ParseDN(s.UserBaseDN); err != nil {
		return fmt.Errorf("user base DN: %v", err)
	}
	if s.BindDN != "" {
		if _, err := ldap.ParseDN(s.BindDN); err != nil {
			return fmt.Errorf("bind DN: %v", err)
		}
		if s.BindPassword == "" {
			return errors.New("enter the bind password for the bind DN")
		}
	}
	if !strings.Contains(s.UserFilter, "{name}") {
		return errors.New("the user filter must contain {name}")
	}
	if _, err := ldap.CompileFilter(userFilter(s.UserFilter, "x")); err != nil {
		return fmt.Errorf("user filter: %v", err)
	}
	if !attributeName.MatchString(s.GroupAttribute) {
		return errors.New("group attribute must be an attribute name such as memberOf")
	}
	if len(s.RoleMappings) == 0 {
		return errors.New("add at least one role mapping — users without one can't sign in")
	}
	return nil
}

// OIDCSettings is the identity provider for single sign-on. ClientSecret
// is stored sealed, apart from the rest.
type OIDCSettings struct {
	Enabled       bool          `json:"enabled"`
	DisplayName   string        `json:"displayName"`
	Issuer        string        `json:"issuer"`
	ClientID      string        `json:"clientId"`
	ClientSecret  string        `json:"-"`
	Scopes        []string      `json:"scopes"`
	UsernameClaim string        `json:"usernameClaim"`
	GroupsClaim   string        `json:"groupsClaim"`
	RoleMappings  []RoleMapping `json:"roleMappings"`
	PublicURL     string        `json:"publicUrl"`
}

func (s OIDCSettings) WithDefaults() OIDCSettings {
	s.DisplayName = strings.TrimSpace(s.DisplayName)
	s.Issuer = strings.TrimSpace(s.Issuer)
	s.ClientID = strings.TrimSpace(s.ClientID)
	var scopes []string
	hasOpenID := false
	for _, sc := range s.Scopes {
		if sc = strings.TrimSpace(sc); sc != "" {
			scopes = append(scopes, sc)
			hasOpenID = hasOpenID || sc == "openid"
		}
	}
	switch {
	case len(scopes) == 0:
		scopes = []string{"openid", "profile", "email"}
	case !hasOpenID:
		scopes = append([]string{"openid"}, scopes...)
	}
	s.Scopes = scopes
	if s.UsernameClaim = strings.TrimSpace(s.UsernameClaim); s.UsernameClaim == "" {
		s.UsernameClaim = "preferred_username"
	}
	if s.GroupsClaim = strings.TrimSpace(s.GroupsClaim); s.GroupsClaim == "" {
		s.GroupsClaim = "groups"
	}
	s.PublicURL = strings.TrimRight(strings.TrimSpace(s.PublicURL), "/")
	s.RoleMappings = cleanMappings(s.RoleMappings)
	return s
}

func (s OIDCSettings) Validate(groupExists func(id string) bool) error {
	if err := validateMappings(s.RoleMappings, groupExists); err != nil {
		return err
	}
	if s.PublicURL != "" {
		u, err := url.Parse(s.PublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("public URL must look like https://sentinel.example.com")
		}
	}
	if !s.Enabled {
		return nil
	}
	u, err := url.Parse(s.Issuer)
	if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return errors.New("issuer must be an https:// URL")
	}
	if s.ClientID == "" {
		return errors.New("enter the client ID registered with the identity provider")
	}
	if len(s.RoleMappings) == 0 {
		return errors.New("add at least one role mapping — users without one can't sign in")
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func cleanMappings(in []RoleMapping) []RoleMapping {
	out := make([]RoleMapping, 0, len(in))
	for _, m := range in {
		m.Group = strings.TrimSpace(m.Group)
		if m.GroupIDs == nil {
			m.GroupIDs = []string{}
		}
		out = append(out, m)
	}
	return out
}

func validateMappings(ms []RoleMapping, groupExists func(id string) bool) error {
	for i, m := range ms {
		if m.Group == "" {
			return fmt.Errorf("role mapping %d: enter a group", i+1)
		}
		if !store.ValidRole(m.Role) {
			return fmt.Errorf("role mapping %d: role must be Admin, Supervisor, Operator or Viewer", i+1)
		}
		for _, id := range m.GroupIDs {
			if groupExists != nil && !groupExists(id) {
				return fmt.Errorf("role mapping %d: no camera group %s", i+1, id)
			}
		}
	}
	return nil
}

// Scope is the camera scope a mapping grants: its camera groups, or every
// camera when it names none (and always for Admins).
func (m RoleMapping) Scope() store.UserScope {
	if len(m.GroupIDs) == 0 || m.Role == "Admin" {
		return store.UserScope{AllCameras: true, GroupIDs: []string{}}
	}
	return store.UserScope{GroupIDs: m.GroupIDs}
}
