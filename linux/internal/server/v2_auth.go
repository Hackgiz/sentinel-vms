package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 30 * 24 * time.Hour
)

func (s *Server) registerV2Auth(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/auth/token", publicRoute, s.handleV2Token)
	s.v2Route(mux, "POST /api/v2/auth/revoke", publicRoute, s.handleV2Revoke)
	s.v2Route(mux, "GET /api/v2/me", auth.ViewLive, s.handleV2Me)
	s.registerV2Directory(mux) // v2_directory.go, v2_sso.go
}

func (s *Server) handleV2Token(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GrantType    string `json:"grantType"`
		Name         string `json:"name"`
		Password     string `json:"password"`
		Client       string `json:"client"`
		RefreshToken string `json:"refreshToken"`
		Code         string `json:"code"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	switch in.GrantType {
	case "password":
		nameKey, ipKey := "user:"+strings.ToLower(strings.TrimSpace(in.Name)), "ip:"+clientIP(r)
		if locked, until := s.limiter.Locked(nameKey, ipKey); locked {
			writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "too many failed sign-ins — try again after "+until.Format("15:04"))
			return
		}
		// Local users first, then the directory (v2_directory.go).
		u, via, err := s.passwordSignIn(r.Context(), in.Name, in.Password)
		if err != nil {
			f := signInFailed(err)
			if f.counts {
				s.limiter.Fail(nameKey, ipKey)
			}
			_ = s.Store.Audit(r.Context(), strings.TrimSpace(in.Name), "Users", "Failed sign-in", f.audit+"API, from "+clientIP(r))
			writeV2Error(w, f.status, f.code, f.msg)
			return
		}
		s.limiter.Reset(nameKey)
		s.issueTokens(w, r, u, in.Client, via)
	case "oidc_code":
		s.handleOIDCCodeGrant(w, r, in.Code, in.Client) // v2_sso.go
	case "refresh":
		p, u, err := s.Store.RefreshTokenPair(r.Context(), in.RefreshToken, accessTokenTTL, refreshTokenTTL)
		switch {
		case errors.Is(err, store.ErrTokenReuse):
			_ = s.Store.Audit(r.Context(), "System", "Users", "Revoked API sign-in", "a refresh token was used twice (from "+clientIP(r)+")")
			writeV2Error(w, http.StatusUnauthorized, codeTokenReused, err.Error())
		case err != nil:
			writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, "sign in again")
		default:
			// Directory users are checked against the directory again
			// (v2_directory.go).
			if u, err = s.recheckDirectoryUser(r, u); err != nil {
				writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, err.Error())
				return
			}
			s.writeTokenResponse(w, r, p, u)
		}
	default:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, `grantType must be "password", "refresh" or "oidc_code"`)
	}
}

// issueTokens answers a successful sign-in with a new token pair. via
// says how the user signed in, for the audit log ("" or "LDAP, ").
func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, u *store.User, client, via string) {
	client = strings.TrimSpace(client)
	if len(client) > 100 {
		client = client[:100]
	}
	p, err := s.Store.CreateTokenPair(r.Context(), u.ID, client, accessTokenTTL, refreshTokenTTL)
	if err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	detail := via + "API, from " + clientIP(r)
	if client != "" {
		detail = via + client + ", from " + clientIP(r)
	}
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed in", detail)
	s.writeTokenResponse(w, r, p, u)
}

func (s *Server) writeTokenResponse(w http.ResponseWriter, r *http.Request, p store.TokenPair, u *store.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tokenType":        "Bearer",
		"accessToken":      p.AccessToken,
		"expiresAt":        p.AccessExpiresAt.UnixMilli(),
		"refreshToken":     p.RefreshToken,
		"refreshExpiresAt": p.RefreshExpiresAt.UnixMilli(),
		"user":             s.v2UserView(r.Context(), u),
	})
}

func (s *Server) handleV2Revoke(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if err := s.Store.RevokeRefreshToken(r.Context(), in.RefreshToken); err != nil {
		writeV2Error(w, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2Me(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	writeJSON(w, http.StatusOK, v2Me{v2User: s.v2UserView(r.Context(), u), Permissions: v2Permissions(u.Role)})
}
