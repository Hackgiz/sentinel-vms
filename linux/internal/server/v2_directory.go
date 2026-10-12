package server

// Directory sign-in (PLATFORM.md, "Permissions"): LDAP/Active Directory
// names and passwords for /api/v1/login and the v2 password grant, and
// the settings for it and for OIDC single sign-on (v2_sso.go). The
// directory logic is internal/directory; this file stores the settings
// (secrets sealed, never returned), provisions directory users and maps
// sign-in failures to answers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/directory"
	"sentinel-linux/internal/store"
)

const (
	ldapSetting             = "directory.ldap"
	ldapBindPasswordSetting = "directory.ldap.bindPassword" // sealed
	oidcSetting             = "directory.oidc"
	oidcClientSecretSetting = "directory.oidc.clientSecret" // sealed
)

type dirState struct {
	mu       sync.Mutex       // serializes settings saves
	ldapDial directory.Dialer // nil = directory.DialLDAP; tests fake the directory
	oidc     directory.OIDC
}

func (s *Server) registerV2Directory(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/directory", auth.ChangeSettings, s.handleV2GetDirectory)
	s.v2Route(mux, "PUT /api/v2/directory", auth.ChangeSettings, s.handleV2PutDirectory)
	s.v2Route(mux, "POST /api/v2/directory/test", auth.ChangeSettings, s.handleV2TestDirectory)
	s.registerV2SSO(mux) // v2_sso.go
}

// MARK: - Settings

func (s *Server) ldapSettings(ctx context.Context) directory.LDAPSettings {
	var cfg directory.LDAPSettings
	_ = json.Unmarshal([]byte(s.Store.Setting(ctx, ldapSetting, "{}")), &cfg)
	if sealed := s.Store.Setting(ctx, ldapBindPasswordSetting, ""); sealed != "" {
		if pw, err := s.Store.Unseal(sealed); err == nil {
			cfg.BindPassword = pw
		}
	}
	return cfg.WithDefaults()
}

func (s *Server) oidcSettings(ctx context.Context) directory.OIDCSettings {
	var cfg directory.OIDCSettings
	_ = json.Unmarshal([]byte(s.Store.Setting(ctx, oidcSetting, "{}")), &cfg)
	if sealed := s.Store.Setting(ctx, oidcClientSecretSetting, ""); sealed != "" {
		if sec, err := s.Store.Unseal(sealed); err == nil {
			cfg.ClientSecret = sec
		}
	}
	return cfg.WithDefaults()
}

// v2LDAPSettings is LDAPSettings: the secret goes in, only whether one is
// set comes out. Absent bindPassword keeps the stored one; "" clears it.
type v2LDAPSettings struct {
	directory.LDAPSettings
	BindPassword    *string `json:"bindPassword,omitempty"`
	HasBindPassword bool    `json:"hasBindPassword"`
}

type v2OIDCSettings struct {
	directory.OIDCSettings
	ClientSecret    *string `json:"clientSecret,omitempty"`
	HasClientSecret bool    `json:"hasClientSecret"`
}

type v2DirectorySettings struct {
	LDAP *v2LDAPSettings `json:"ldap"`
	OIDC *v2OIDCSettings `json:"oidc"`
}

func (s *Server) directoryView(ctx context.Context) v2DirectorySettings {
	l, o := s.ldapSettings(ctx), s.oidcSettings(ctx)
	return v2DirectorySettings{
		LDAP: &v2LDAPSettings{LDAPSettings: l, HasBindPassword: l.BindPassword != ""},
		OIDC: &v2OIDCSettings{OIDCSettings: o, HasClientSecret: o.ClientSecret != ""},
	}
}

func (s *Server) handleV2GetDirectory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.directoryView(r.Context()))
}

func (s *Server) groupExists(ctx context.Context) func(string) bool {
	return func(id string) bool {
		_, err := s.Store.CameraGroup(ctx, id)
		return err == nil
	}
}

func (s *Server) handleV2PutDirectory(w http.ResponseWriter, r *http.Request) {
	var in v2DirectorySettings
	if !readV2JSON(w, r, &in) {
		return
	}
	if in.LDAP == nil || in.OIDC == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "send both ldap and oidc")
		return
	}
	ctx := r.Context()
	s.dir.mu.Lock()
	defer s.dir.mu.Unlock()
	oldL, oldO := s.ldapSettings(ctx), s.oidcSettings(ctx)

	l := in.LDAP.LDAPSettings
	l.BindPassword = oldL.BindPassword
	if in.LDAP.BindPassword != nil {
		l.BindPassword = *in.LDAP.BindPassword
	}
	l = l.WithDefaults()
	o := in.OIDC.OIDCSettings
	o.ClientSecret = oldO.ClientSecret
	if in.OIDC.ClientSecret != nil {
		o.ClientSecret = *in.OIDC.ClientSecret
	}
	o = o.WithDefaults()
	exists := s.groupExists(ctx)
	if err := l.Validate(exists); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "LDAP: "+err.Error())
		return
	}
	if err := o.Validate(exists); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "OIDC: "+err.Error())
		return
	}

	if err := s.saveSealed(ctx, ldapSetting, l, ldapBindPasswordSetting, l.BindPassword); err != nil {
		writeV2Err(w, err)
		return
	}
	if err := s.saveSealed(ctx, oidcSetting, o, oidcClientSecretSetting, o.ClientSecret); err != nil {
		writeV2Err(w, err)
		return
	}
	s.dir.oidc.Reset()

	if prev, now := describeLDAP(oldL), describeLDAP(l); prev != now {
		s.audit(r, "Users", "Changed LDAP sign-in", prev+" → "+now)
	}
	if l.BindPassword != oldL.BindPassword {
		s.audit(r, "Users", "Changed LDAP bind password", "")
	}
	if prev, now := describeOIDC(oldO), describeOIDC(o); prev != now {
		s.audit(r, "Users", "Changed single sign-on", prev+" → "+now)
	}
	if o.ClientSecret != oldO.ClientSecret {
		s.audit(r, "Users", "Changed single sign-on client secret", "")
	}
	writeJSON(w, http.StatusOK, s.directoryView(ctx))
}

// saveSealed stores cfg as JSON (its secret field is json:"-") and the
// secret sealed under its own key.
func (s *Server) saveSealed(ctx context.Context, key string, cfg any, secretKey, secret string) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	sealed, err := s.Store.Seal(secret)
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, key, string(data)); err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, secretKey, sealed)
}

// describeLDAP / describeOIDC are audit summaries (never secrets).
func describeLDAP(c directory.LDAPSettings) string {
	if !c.Enabled {
		return "off"
	}
	return fmt.Sprintf("%s, base %s, filter %s, bind %s, %s", c.URL, c.UserBaseDN, c.UserFilter, bindDNLabel(c.BindDN), describeMappings(c.RoleMappings, c.InsecureSkipVerify))
}

func describeOIDC(c directory.OIDCSettings) string {
	if !c.Enabled {
		return "off"
	}
	return fmt.Sprintf("%s, client %s, scopes %s, claims %s/%s, %s", c.Issuer, c.ClientID, strings.Join(c.Scopes, " "), c.UsernameClaim, c.GroupsClaim, describeMappings(c.RoleMappings, false))
}

func describeMappings(ms []directory.RoleMapping, insecure bool) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		p := m.Group + " = " + m.Role
		if len(m.GroupIDs) > 0 {
			p += " (" + strings.Join(m.GroupIDs, ", ") + ")"
		}
		parts = append(parts, p)
	}
	out := "mappings: " + strings.Join(parts, "; ")
	if insecure {
		out += ", certificate checks OFF"
	}
	return out
}

func bindDNLabel(s string) string {
	if s == "" {
		return "anonymous"
	}
	return s
}

// handleV2TestDirectory tries LDAP settings (not saved) with a name and
// password and reports what sign-in would do.
func (s *Server) handleV2TestDirectory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Settings v2LDAPSettings `json:"settings"`
		Name     string         `json:"name"`
		Password string         `json:"password"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	l := in.Settings.LDAPSettings
	l.BindPassword = s.ldapSettings(ctx).BindPassword
	if in.Settings.BindPassword != nil {
		l.BindPassword = *in.Settings.BindPassword
	}
	l = l.WithDefaults()
	l.Enabled = true // test as if it were on
	out := map[string]any{"ok": false}
	if err := l.Validate(s.groupExists(ctx)); err != nil {
		out["message"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	id, err := (&directory.LDAP{Settings: l, Dial: s.dir.ldapDial}).Authenticate(ctx, in.Name, in.Password)
	if id != nil {
		out["groups"] = id.Groups
	}
	switch {
	case err == nil:
		out["ok"], out["role"] = true, id.Mapping.Role
		out["message"] = fmt.Sprintf("%s would sign in as %s", strings.TrimSpace(in.Name), id.Mapping.Role)
	default:
		out["message"] = directory.Describe(err)
	}
	s.audit(r, "Users", "Tested LDAP sign-in", strings.TrimSpace(in.Name)+": "+out["message"].(string))
	writeJSON(w, http.StatusOK, out)
}

// MARK: - Sign-in

// passwordSignIn checks a name and password: a local user with that name
// first; otherwise, when LDAP is on, the directory (provisioning the user
// on first sign-in). via is "" or "LDAP, " for the audit log.
func (s *Server) passwordSignIn(ctx context.Context, name, password string) (u *store.User, via string, err error) {
	u, err = s.Store.UserByName(ctx, name)
	switch {
	case err == nil:
		src, err := s.Store.UserSource(ctx, u.ID)
		if err != nil {
			return nil, "", err
		}
		switch src {
		case store.SourceLocal:
			if !auth.CheckPassword(u.PasswordHash, password) {
				return nil, "", directory.ErrBadCredentials
			}
			return u, "", nil
		case store.SourceLDAP:
			// checked against the directory below
		default: // single sign-on users have no password here
			auth.EqualizeTiming(password)
			return nil, "", directory.ErrBadCredentials
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, "", err
	}
	l := s.ldapSettings(ctx)
	if !l.Enabled {
		auth.EqualizeTiming(password)
		return nil, "", directory.ErrBadCredentials
	}
	id, err := (&directory.LDAP{Settings: l, Dial: s.dir.ldapDial}).Authenticate(ctx, name, password)
	if err != nil {
		return nil, "", err
	}
	u, err = s.provisionDirectoryUser(ctx, store.SourceLDAP, id)
	return u, "LDAP, ", err
}

// provisionDirectoryUser creates or updates the user a directory vouched
// for, with the role and scope of their mapping. The last Admin keeps
// that role even if the directory says otherwise, so the site never ends
// up without one.
func (s *Server) provisionDirectoryUser(ctx context.Context, source string, id *directory.Identity) (*store.User, error) {
	role, scope := id.Mapping.Role, id.Mapping.Scope()
	prev, err := s.Store.DirectoryUser(ctx, source, id.ExternalID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if prev != nil && prev.Role == "Admin" && role != "Admin" {
		if n, _ := s.Store.AdminCount(ctx); n <= 1 {
			slog.Warn("directory sign-in: kept the last administrator's role", "user", prev.Name, "mapped", role)
			role, scope = "Admin", store.UserScope{AllCameras: true, GroupIDs: []string{}}
		}
	}
	u, created, err := s.Store.ProvisionDirectoryUser(ctx, source, id.ExternalID, id.Name, role, scope)
	if errors.Is(err, store.ErrNameTaken) {
		return nil, errNameTaken{id.Name}
	}
	if err != nil {
		return nil, err
	}
	switch {
	case created:
		_ = s.Store.Audit(ctx, "System", "Users", "Added directory user", fmt.Sprintf("%s (%s, %s)", u.Name, strings.ToUpper(source), role))
	case prev != nil && prev.Role != role:
		_ = s.Store.Audit(ctx, "System", "Users", "Changed user role", fmt.Sprintf("%s: %s → %s (directory groups)", u.Name, prev.Role, role))
	}
	return u, nil
}

// errNameTaken: the directory user's name belongs to another Sentinel user.
type errNameTaken struct{ name string }

func (e errNameTaken) Error() string {
	return "another Sentinel user is already named " + e.name + " — ask an administrator"
}

// signInFailure is how a failed sign-in is answered.
type signInFailure struct {
	status int
	code   string // v2 error code
	msg    string
	audit  string // prefix for the audit detail
	counts bool   // counts towards the lockout
}

func signInFailed(err error) signInFailure {
	var unavailable *directory.UnavailableError
	var taken errNameTaken
	switch {
	case errors.Is(err, directory.ErrBadCredentials):
		return signInFailure{http.StatusUnauthorized, codeUnauthorized, "wrong name or password", "", true}
	case errors.Is(err, directory.ErrDisabled):
		// Same answer as a wrong password: don't confirm the account exists.
		return signInFailure{http.StatusUnauthorized, codeUnauthorized, "wrong name or password", "disabled directory account, ", true}
	case errors.Is(err, directory.ErrNoRole):
		return signInFailure{http.StatusUnauthorized, codeUnauthorized, err.Error(), "no role mapping matches, ", true}
	case errors.As(err, &taken):
		return signInFailure{http.StatusUnauthorized, codeUnauthorized, err.Error(), "name taken by another user, ", true}
	case errors.As(err, &unavailable):
		slog.Warn("directory sign-in", "err", err)
		return signInFailure{http.StatusServiceUnavailable, codeUnavailable, "the directory can't be reached — try again, or ask an administrator", "directory unavailable, ", false}
	default:
		slog.Error("sign-in", "err", err)
		return signInFailure{http.StatusInternalServerError, codeInternal, "sign-in failed", "", false}
	}
}

// recheckDirectoryUser runs when an LDAP user refreshes an API sign-in:
// without a password, the service account looks them up again, so people
// removed from the directory or from every mapped group lose access
// within one access-token lifetime. If the directory can't be reached the
// refresh goes ahead. Other users pass through.
func (s *Server) recheckDirectoryUser(r *http.Request, u *store.User) (*store.User, error) {
	ctx := r.Context()
	src, err := s.Store.UserSource(ctx, u.ID)
	if err != nil || src != store.SourceLDAP {
		return u, err
	}
	refuse := func(why string) (*store.User, error) {
		_ = s.Store.DeleteUserTokens(ctx, u.ID)
		_ = s.Store.Audit(ctx, "System", "Users", "Revoked API sign-in", u.Name+": "+why)
		return nil, errors.New("sign in again")
	}
	l := s.ldapSettings(ctx)
	if !l.Enabled {
		return refuse("LDAP sign-in is off")
	}
	id, err := (&directory.LDAP{Settings: l, Dial: s.dir.ldapDial}).Lookup(ctx, u.Name)
	var unavailable *directory.UnavailableError
	switch {
	case errors.As(err, &unavailable):
		slog.Warn("directory recheck skipped", "user", u.Name, "err", err)
		return u, nil
	case err != nil:
		return refuse(directory.Describe(err))
	}
	if prev, _ := s.Store.DirectoryUser(ctx, store.SourceLDAP, id.ExternalID); prev == nil || prev.ID != u.ID {
		return refuse("the directory now has a different account with this name")
	}
	return s.provisionDirectoryUser(ctx, store.SourceLDAP, id)
}

// isDirectoryUser: the user signs in through LDAP or OIDC, so their
// password lives in the directory (v2 PATCH /users answers 409).
func (s *Server) isDirectoryUser(ctx context.Context, userID string) bool {
	src, err := s.Store.UserSource(ctx, userID)
	return err == nil && src != store.SourceLocal
}
