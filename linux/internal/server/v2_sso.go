package server

// Single sign-on with an OpenID Connect identity provider (authorization
// code + PKCE). The flow state (state, nonce, PKCE verifier, where to go
// afterwards) stays on the server; the browser only carries the state, in
// the query and in a short-lived cookie that binds the flow to the browser
// that started it.
//
//	web:    /auth/oidc/start?returnTo=/path → provider → /auth/oidc/callback
//	        → session cookie, 302 to returnTo (a path on this server)
//	native: /auth/oidc/start?redirectUri=…&state=… → provider → callback
//	        → 302 to redirectUri?code=…&state=…; the client exchanges the
//	        one-time code (60 s) with grantType oidc_code (v2_auth.go)

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"sentinel-linux/internal/directory"
	"sentinel-linux/internal/store"
)

const (
	ssoCookie      = "sentinel_sso"
	ssoCookiePath  = "/api/v2/auth/oidc/"
	maxClientState = 512
)

func (s *Server) registerV2SSO(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/auth/providers", publicRoute, s.handleV2AuthProviders)
	s.v2Route(mux, "GET /api/v2/auth/oidc/start", publicRoute, s.handleV2OIDCStart)
	s.v2Route(mux, "GET /api/v2/auth/oidc/callback", publicRoute, s.handleV2OIDCCallback)
}

func (s *Server) handleV2AuthProviders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o := map[string]any{"enabled": false}
	if cfg := s.oidcSettings(ctx); cfg.Enabled {
		o["enabled"] = true
		if cfg.DisplayName != "" {
			o["displayName"] = cfg.DisplayName
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"local": true, "ldap": s.ldapSettings(ctx).Enabled, "oidc": o})
}

// oidcCallbackURL is publicUrl + the callback path, or this request's
// origin when publicUrl isn't set.
func oidcCallbackURL(r *http.Request, cfg directory.OIDCSettings) string {
	const path = "/api/v2/auth/oidc/callback"
	if cfg.PublicURL != "" {
		return cfg.PublicURL + path
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

func ssoCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: ssoCookie, Value: value, Path: ssoCookiePath, MaxAge: maxAge, HttpOnly: true,
		// Lax: the identity provider's redirect back is a cross-site
		// top-level GET, which Strict would strip the cookie from.
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	}
}

func (s *Server) handleV2OIDCStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := s.oidcSettings(ctx)
	if !cfg.Enabled {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "single sign-on is off")
		return
	}
	q := r.URL.Query()
	f := directory.Flow{CallbackURL: oidcCallbackURL(r, cfg)}
	if ru := q.Get("redirectUri"); ru != "" {
		if !directory.SafeRedirectURI(ru) {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "redirectUri must be http://127.0.0.1:<port>/…, http://localhost:<port>/… or a "+directory.NativeScheme+": URL")
			return
		}
		if len(q.Get("state")) > maxClientState {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "state is too long")
			return
		}
		f.RedirectURI, f.ClientState = ru, q.Get("state")
	} else {
		rt, ok := directory.SafeReturnTo(q.Get("returnTo"))
		if !ok {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "returnTo must be a path on this server")
			return
		}
		f.ReturnTo = rt
	}
	authURL, state, err := s.dir.oidc.Begin(ctx, cfg, f)
	var unavailable *directory.UnavailableError
	switch {
	case errors.Is(err, directory.ErrTooManyFlows):
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, err.Error())
		return
	case errors.As(err, &unavailable):
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, "the identity provider can't be reached")
		return
	case err != nil:
		writeV2Err(w, err)
		return
	}
	http.SetCookie(w, ssoCookieFor(r, state, int(directory.FlowTTL.Seconds())))
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleV2OIDCCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	f, ok := s.dir.oidc.Take(state)
	if !ok {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this sign-in expired or was already used — start again")
		return
	}
	// The flow must finish in the browser that started it (no login CSRF).
	c, err := r.Cookie(ssoCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this sign-in was started in a different browser — start again")
		return
	}
	http.SetCookie(w, ssoCookieFor(r, "", -1))

	fail := func(msg, why string) {
		_ = s.Store.Audit(ctx, "System", "Users", "Failed sign-in", "single sign-on: "+why+", from "+clientIP(r))
		if f.Native() {
			http.Redirect(w, r, ssoURL(f.RedirectURI, "error", "access_denied", "error_description", msg, "state", f.ClientState), http.StatusFound)
			return
		}
		http.Redirect(w, r, ssoURL("/", "ssoError", msg), http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		fail("the identity provider didn't sign you in ("+ssoClip(e, 100)+")", "provider error "+ssoClip(e, 100))
		return
	}
	cfg := s.oidcSettings(ctx)
	if !cfg.Enabled {
		fail("single sign-on is off", "single sign-on is off")
		return
	}
	id, err := s.dir.oidc.Finish(ctx, cfg, f, q.Get("code"))
	if err != nil {
		who := ""
		if id != nil {
			who = id.Name + ": "
		}
		msg := "single sign-on failed — try again, or ask an administrator"
		if errors.Is(err, directory.ErrNoRole) {
			msg = err.Error()
		}
		fail(msg, who+err.Error())
		return
	}
	u, err := s.provisionDirectoryUser(ctx, store.SourceOIDC, id)
	if err != nil {
		var taken errNameTaken
		msg := "single sign-on failed"
		if errors.As(err, &taken) {
			msg = err.Error()
		}
		fail(msg, id.Name+": "+err.Error())
		return
	}
	if f.Native() {
		_ = s.Store.Audit(ctx, u.Name, "Users", "Single sign-on", "code for a native client, from "+clientIP(r))
		http.Redirect(w, r, ssoURL(f.RedirectURI, "code", s.dir.oidc.IssueCode(u.ID), "state", f.ClientState), http.StatusFound)
		return
	}
	if err := s.setSessionCookie(w, r, u); err != nil {
		fail("single sign-on failed", err.Error())
		return
	}
	_ = s.Store.Audit(ctx, u.Name, "Users", "Signed in", "single sign-on, from "+clientIP(r))
	http.Redirect(w, r, f.ReturnTo, http.StatusFound)
}

// handleOIDCCodeGrant exchanges a native client's one-time code for a
// token pair (grantType oidc_code).
func (s *Server) handleOIDCCodeGrant(w http.ResponseWriter, r *http.Request, code, client string) {
	ipKey := "ip:" + clientIP(r)
	if locked, until := s.limiter.Locked(ipKey); locked {
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "too many failed sign-ins — try again after "+until.Format("15:04"))
		return
	}
	userID, ok := s.dir.oidc.RedeemCode(code)
	var u *store.User
	var err error
	if ok {
		u, err = s.Store.UserByID(r.Context(), userID)
	}
	if !ok || err != nil {
		s.limiter.Fail(ipKey)
		_ = s.Store.Audit(r.Context(), "System", "Users", "Failed sign-in", "single sign-on code invalid, expired or used, from "+clientIP(r))
		writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, "the sign-in code is invalid or expired — sign in again")
		return
	}
	s.issueTokens(w, r, u, client, "single sign-on, ")
}

// ssoURL adds key/value pairs ("" values skipped) to a URL's query.
func ssoURL(raw string, kv ...string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	q := u.Query()
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			q.Set(kv[i], kv[i+1])
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func ssoClip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
