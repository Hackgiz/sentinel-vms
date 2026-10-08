// Package server is Sentinel's HTTP surface: the web dashboard, its JSON API
// (/api/v1, cookie sessions) and the authenticated video proxy.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tunnel"
)

const (
	sessionCookie = "sentinel_session"
	sessionTTL    = 12 * time.Hour
)

type Server struct {
	Store   *store.Store
	Media   *media.Supervisor
	WebFS   fs.FS // built dashboard (index.html + assets); nil in API-only dev runs
	Version string

	limiter *auth.Limiter
	scanMu  sync.Mutex // one network scan at a time

	// iPhone companion
	Tunnel     *tunnel.Tunnel  // nil = remote access unsupported
	Ctx        context.Context // server lifetime, for starting the tunnel
	ServerName string          // shown in the phone app; defaults to hostname
	ListenPort string          // dashboard/API port, baked into pairing QR codes
	pairing    pairingState

	// Detection
	Detect *detect.Manager // nil/unavailable = no motion detection (ffmpeg missing)
	detect detectState

	// AI (bring-your-own Anthropic key)
	AIOptions []option.RequestOption // tests point the client at a fake API
	ai        aiState

	// Feedback
	FeedbackURL   string        // "" = DefaultFeedbackURL
	RecentLog     func() string // recent server log lines, secrets stripped
	feedbackLimit feedbackLimiter
}

func New(st *store.Store, sup *media.Supervisor, web fs.FS, version string) *Server {
	return &Server{Store: st, Media: sup, WebFS: web, Version: version, limiter: auth.NewLimiter()}
}

type ctxKey int

const userKey ctxKey = 0

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)

	// Signed in
	mux.Handle("GET /api/v1/me", s.authed(auth.ViewLive, s.handleMe))
	mux.Handle("GET /api/v1/cameras", s.authed(auth.ViewLive, s.handleListCameras))
	mux.Handle("POST /api/v1/cameras", s.authed(auth.ManageCameras, s.handleSaveCamera))
	mux.Handle("PUT /api/v1/cameras/{id}", s.authed(auth.ManageCameras, s.handleSaveCamera))
	mux.Handle("DELETE /api/v1/cameras/{id}", s.authed(auth.ManageCameras, s.handleDeleteCamera))
	mux.Handle("POST /api/v1/discover", s.authed(auth.ManageCameras, s.handleDiscover))
	mux.Handle("POST /api/v1/discover/probe", s.authed(auth.ManageCameras, s.handleDiscoverProbe))
	mux.Handle("GET /api/v1/cameras/{id}/recordings", s.authed(auth.ViewLive, s.handleListRecordings))
	mux.Handle("GET /api/v1/users", s.authed(auth.ManageUsers, s.handleListUsers))
	mux.Handle("POST /api/v1/users", s.authed(auth.ManageUsers, s.handleCreateUser))
	mux.Handle("DELETE /api/v1/users/{id}", s.authed(auth.ManageUsers, s.handleDeleteUser))
	mux.Handle("GET /api/v1/settings", s.authed(auth.ViewLive, s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings", s.authed(auth.ChangeSettings, s.handlePutSettings))
	mux.Handle("GET /api/v1/system", s.authed(auth.ViewLive, s.handleSystem))
	mux.Handle("GET /api/v1/audit", s.authed(auth.ViewAudit, s.handleAudit))
	mux.Handle("GET /api/v1/audit/verify", s.authed(auth.ViewAudit, s.handleAuditVerify))

	mux.Handle("GET /api/v1/alarms", s.authed(auth.ViewLive, s.handleListAlarms))
	mux.Handle("POST /api/v1/alarms/{id}/state", s.authed(auth.AcknowledgeAlarms, s.handleSetAlarmState))
	mux.Handle("POST /api/v1/alarms/{id}/evidence", s.authed(auth.ExportEvidence, s.handleLockEvidence))
	mux.Handle("GET /api/v1/events", s.authed(auth.ViewLive, s.handleListEvents))
	mux.Handle("GET /api/v1/events/{id}/thumbnail.jpg", s.authed(auth.ViewLive, func(w http.ResponseWriter, r *http.Request) {
		s.serveEventThumbnail(w, r, r.PathValue("id"))
	}))
	mux.Handle("GET /api/v1/evidence", s.authed(auth.ExportEvidence, s.handleListEvidence))
	mux.Handle("GET /api/v1/evidence/{id}/file", s.authed(auth.ExportEvidence, s.handleEvidenceFile))
	mux.Handle("POST /api/v1/feedback", s.authed(auth.ViewLive, s.handleFeedback))
	mux.Handle("GET /api/v1/ai", s.authed(auth.ViewLive, s.handleGetAI))
	mux.Handle("PUT /api/v1/ai", s.authed(auth.ChangeSettings, s.handlePutAI))
	mux.Handle("POST /api/v1/ai/search", s.authed(auth.AcknowledgeAlarms, s.handleAISearch))
	mux.Handle("POST /api/v1/ai/digest", s.authed(auth.AcknowledgeAlarms, s.handleAIDigest))
	mux.Handle("GET /api/v1/pairing", s.authed(auth.ManageUsers, s.handleGetPairing))
	mux.Handle("POST /api/v1/pairing", s.authed(auth.ManageUsers, s.handleStartPairing))
	mux.Handle("DELETE /api/v1/pairing", s.authed(auth.ManageUsers, s.handleCancelPairing))
	mux.Handle("GET /api/v1/devices", s.authed(auth.ManageUsers, s.handleListDevices))
	mux.Handle("DELETE /api/v1/devices/{id}", s.authed(auth.ManageUsers, s.handleRevokeDevice))
	mux.Handle("GET /api/v1/remote", s.authed(auth.ManageUsers, s.handleGetRemote))
	mux.Handle("PUT /api/v1/remote", s.authed(auth.ChangeSettings, s.handlePutRemote))

	// iPhone companion API (Sentinel Mobile) — bearer-token auth, Mac-compatible paths
	s.registerCompanion(mux)

	// Video (cookie-authenticated so <video> and hls.js just work)
	mux.Handle("GET /live/{id}/{file}", s.authed(auth.ViewLive, s.handleLiveProxy))
	mux.Handle("GET /recordings/{id}/{file}", s.authed(auth.ViewLive, s.handleRecordingFile))

	// Dashboard
	mux.Handle("/", s.spa())

	return s.securityHeaders(s.csrfGuard(mux))
}

// MARK: - Middleware

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard: state-changing API calls must be JSON (a cross-site form can't
// send that without a CORS preflight we never grant) and, when the browser
// says where it came from, come from this same host.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "cross-site request refused")
					return
				}
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "send JSON")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(p auth.Permission, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		u, err := s.Store.SessionUser(r.Context(), c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session expired — sign in again")
			return
		}
		if !auth.Can(u.Role, p) {
			writeError(w, http.StatusForbidden, "your role ("+u.Role+") can't do this")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// MARK: - Helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(r *http.Request, area, action, detail string) {
	user := "System"
	if u := currentUser(r); u != nil {
		user = u.Name
	}
	if err := s.Store.Audit(r.Context(), user, area, action, detail); err != nil {
		slog.Error("audit write failed", "err", err)
	}
}

// MARK: - Session handlers

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UserCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{"setupRequired": n == 0, "version": s.Version}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			resp["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// handleSetup creates the first Admin. Only works while no users exist.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	if n, _ := s.Store.UserCount(r.Context()); n > 0 {
		writeError(w, http.StatusConflict, "setup is already complete")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "enter a name")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.Store.CreateUser(r.Context(), name, "Admin", hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Created first administrator", u.Name)
	s.startSession(w, r, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	nameKey, ipKey := "user:"+strings.ToLower(strings.TrimSpace(in.Name)), "ip:"+clientIP(r)
	if locked, until := s.limiter.Locked(nameKey, ipKey); locked {
		writeError(w, http.StatusTooManyRequests, "too many failed sign-ins — try again after "+until.Format("15:04"))
		return
	}
	u, err := s.Store.UserByName(r.Context(), in.Name)
	if err != nil {
		auth.EqualizeTiming(in.Password)
	}
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		s.limiter.Fail(nameKey, ipKey)
		_ = s.Store.Audit(r.Context(), strings.TrimSpace(in.Name), "Users", "Failed sign-in", "from "+clientIP(r))
		writeError(w, http.StatusUnauthorized, "wrong name or password")
		return
	}
	s.limiter.Reset(nameKey)
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed in", "from "+clientIP(r))
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := s.Store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Behind the Cloudflare tunnel the browser sees https even though this
		// hop is plain http.
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed out", "")
		}
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

// MARK: - Users

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || !store.ValidRole(in.Role) {
		writeError(w, http.StatusBadRequest, "enter a name and a valid role")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.Store.CreateUser(r.Context(), in.Name, in.Role, hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "Users", "Created user", u.Name+" ("+u.Role+")")
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := s.Store.UserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if target.ID == currentUser(r).ID {
		writeError(w, http.StatusBadRequest, "you can't delete yourself")
		return
	}
	if target.Role == "Admin" {
		if n, _ := s.Store.AdminCount(r.Context()); n <= 1 {
			writeError(w, http.StatusBadRequest, "can't delete the last administrator")
			return
		}
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "Users", "Deleted user", target.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// MARK: - Audit

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.AuditLog(r.Context(), 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	checked, broken, err := s.Store.VerifyAudit(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checked": checked, "intact": broken == "", "brokenEntryID": broken})
}

// MARK: - Dashboard (single-page app)

func (s *Server) spa() http.Handler {
	if s.WebFS != nil {
		if _, err := fs.Stat(s.WebFS, "index.html"); err != nil {
			s.WebFS = nil // fresh checkout: only the placeholder is embedded
		}
	}
	if s.WebFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard not built — run scripts/build.sh", http.StatusNotFound)
		})
	}
	files := http.FileServer(http.FS(s.WebFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := s.WebFS.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					// Vite fingerprints asset names, so they can be cached forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			} else if !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Client-side routes all load index.html.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
