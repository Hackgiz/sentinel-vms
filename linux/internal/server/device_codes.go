package server

// Device-code sign-in (WAVE10-APPLE.md, Stream C; RFC 8628 style), used by
// Sentinel TV.
//
//	POST /api/v2/device-codes          public, rate limited per IP: start
//	POST /api/v2/device-codes/token    public: the device polls with its device code
//	POST /api/v2/device-codes/lookup   signed in: what device is behind a user code
//	POST /api/v2/device-codes/approve  signed in: let that device in as me (Viewer-capped)
//	POST /api/v2/device-codes/deny     signed in: refuse it
//
// The device shows the user code and verifyUrl (the dashboard's /link
// page); a signed-in person enters the code there, sees the device's name
// and approves. The device polls no faster than every `interval` seconds;
// polling faster answers slow_down. Codes live 10 minutes, are stored
// hashed, are decided once and redeemed once. The token pair a device gets
// belongs to the approving user but is capped at the Viewer role
// (store.CapDeviceTokenUser) and keeps that user's camera scope. Issue,
// approve, deny and redeem are audited.

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	deviceCodeTTL        = 600 * time.Second
	deviceCodeInterval   = 5 // seconds between polls
	deviceCodePollSlack  = time.Second
	deviceCodeIssueLimit = 10 // codes per IP per window
	deviceCodeIssueWin   = 10 * time.Minute
	deviceCodeGuessLimit = 10 // failed lookups per user per window
	deviceCodeGuessWin   = 10 * time.Minute
	deviceCodeAuditUser  = "Device sign-in"
	deviceNameMax        = 60
)

// RFC 8628 error codes, carried as the v2 error code.
const (
	codeAuthPending  = "authorization_pending"
	codeSlowDown     = "slow_down"
	codeAccessDenied = "access_denied"
	codeExpiredToken = "expired_token"
)

// deviceCodeGuard is the per-server state of the device-code routes (the
// Server struct is shared with other streams, so it lives here).
type deviceCodeGuard struct {
	issue   *windowLimiter // issues per client IP
	guesses *windowLimiter // failed user-code lookups per user
}

// windowLimiter allows n hits per key in a sliding window (the pattern of
// the share-link guess limiter and videogw.SessionLimiter).
type windowLimiter struct {
	n      int
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	recent map[string][]time.Time
}

func newWindowLimiter(n int, window time.Duration) *windowLimiter {
	return &windowLimiter{n: n, window: window, now: time.Now, recent: map[string][]time.Time{}}
}

// prune drops key's expired hits; the caller holds l.mu.
func (l *windowLimiter) prune(key string) []time.Time {
	now := l.now()
	kept := l.recent[key][:0]
	for _, t := range l.recent[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.recent, key)
		return nil
	}
	l.recent[key] = kept
	return kept
}

// Blocked reports whether key has used up its hits.
func (l *windowLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.n
}

// Allow records a hit for key unless it is over the limit.
func (l *windowLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.prune(key)
	if len(kept) >= l.n {
		return false
	}
	l.recent[key] = append(kept, l.now())
	if len(l.recent) > 5000 { // drop idle keys
		for k := range l.recent {
			l.prune(k)
		}
	}
	return true
}

var (
	deviceCodeGuardsMu sync.Mutex
	deviceCodeGuards   = map[*Server]*deviceCodeGuard{}
)

func (s *Server) deviceCodeState() *deviceCodeGuard {
	deviceCodeGuardsMu.Lock()
	defer deviceCodeGuardsMu.Unlock()
	g := deviceCodeGuards[s]
	if g == nil {
		g = &deviceCodeGuard{
			issue:   newWindowLimiter(deviceCodeIssueLimit, deviceCodeIssueWin),
			guesses: newWindowLimiter(deviceCodeGuessLimit, deviceCodeGuessWin),
		}
		deviceCodeGuards[s] = g
	}
	return g
}

func (s *Server) registerV2DeviceCodes(mux *http.ServeMux) {
	s.v2Route(mux, "POST /api/v2/device-codes", publicRoute, s.handleV2DeviceCodeIssue)
	s.v2Route(mux, "POST /api/v2/device-codes/token", publicRoute, s.handleV2DeviceCodeToken)
	s.v2Route(mux, "POST /api/v2/device-codes/lookup", auth.ViewLive, s.handleV2DeviceCodeLookup)
	s.v2Route(mux, "POST /api/v2/device-codes/approve", auth.ViewLive, s.handleV2DeviceCodeDecide(true))
	s.v2Route(mux, "POST /api/v2/device-codes/deny", auth.ViewLive, s.handleV2DeviceCodeDecide(false))
}

// cleanDeviceName keeps a device's self-reported name printable and short.
func cleanDeviceName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if r := []rune(name); len(r) > deviceNameMax {
		name = string(r[:deviceNameMax])
	}
	if name == "" {
		name = "Unnamed device"
	}
	return name
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleV2DeviceCodeIssue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceName string `json:"deviceName"`
	}
	if r.ContentLength != 0 && !readV2JSON(w, r, &in) {
		return
	}
	ip := clientIP(r)
	if !s.deviceCodeState().issue.Allow(ip) {
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "too many sign-in codes from this address; wait a few minutes")
		return
	}
	name := cleanDeviceName(in.DeviceName)
	userCode, deviceCode, dc, err := s.Store.CreateDeviceCode(r.Context(), name, deviceCodeTTL)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	_ = s.Store.Audit(r.Context(), deviceCodeAuditUser, "Users", "Issued device sign-in code", name+", from "+ip)
	verify := requestOrigin(r) + "/link"
	writeJSON(w, http.StatusOK, map[string]any{
		"userCode":          store.FormatUserCode(userCode),
		"deviceCode":        deviceCode,
		"verifyUrl":         verify,
		"verifyUrlComplete": verify + "?code=" + store.FormatUserCode(userCode),
		"expiresIn":         int(deviceCodeTTL / time.Second),
		"expiresAt":         secToMs(dc.ExpiresAt),
		"interval":          deviceCodeInterval,
	})
}

func (s *Server) handleV2DeviceCodeToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"deviceCode"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	d, err := s.Store.PollDeviceCode(r.Context(), strings.TrimSpace(in.DeviceCode))
	if errors.Is(err, store.ErrNotFound) {
		writeV2Error(w, http.StatusBadRequest, codeExpiredToken, "unknown or expired device code; start again")
		return
	}
	if err != nil {
		writeV2Err(w, err)
		return
	}
	now := time.Now()
	switch {
	case d.Expired(now) || d.Status == store.DeviceCodeRedeemed:
		writeV2Error(w, http.StatusBadRequest, codeExpiredToken, "the code expired or was already used; start again")
		return
	case d.Status == store.DeviceCodeDenied:
		writeV2Error(w, http.StatusBadRequest, codeAccessDenied, "the sign-in was refused")
		return
	case d.LastPollAt > 0 && now.Sub(time.UnixMilli(d.LastPollAt)) < deviceCodeInterval*time.Second-deviceCodePollSlack:
		writeV2Error(w, http.StatusBadRequest, codeSlowDown, "polling too fast; wait at least the interval between polls")
		return
	case d.Status == store.DeviceCodePending:
		writeV2Error(w, http.StatusBadRequest, codeAuthPending, "waiting for someone to approve the code")
		return
	}
	// Approved. The approver must still exist and still be allowed to
	// watch (the cap only ever lowers what their role allows).
	approver, err := s.Store.UserByID(r.Context(), d.ApprovedBy)
	if err != nil || !s.can(approver.Role, auth.ViewLive) {
		writeV2Error(w, http.StatusBadRequest, codeAccessDenied, "the approving account can no longer sign this device in")
		return
	}
	p, u, err := s.Store.RedeemDeviceCode(r.Context(), d.ID, d.DeviceName, accessTokenTTL, refreshTokenTTL)
	if errors.Is(err, store.ErrDeviceCodeExpired) {
		writeV2Error(w, http.StatusBadRequest, codeExpiredToken, "the code expired or was already used; start again")
		return
	}
	if err != nil {
		writeV2Err(w, err)
		return
	}
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed in", d.DeviceName+" (device code, viewer only), from "+clientIP(r))
	s.writeTokenResponse(w, r, p, u)
}

// lookupUserCode resolves a typed user code for the signed-in caller,
// counting misses against them.
func (s *Server) lookupUserCode(w http.ResponseWriter, r *http.Request, typed string) (string, *store.DeviceCode, bool) {
	u := currentUser(r)
	g := s.deviceCodeState().guesses
	key := "user:" + u.ID
	if g.Blocked(key) {
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "too many wrong codes; wait a few minutes")
		return "", nil, false
	}
	code := store.NormalizeUserCode(typed)
	d, err := s.Store.PendingDeviceCode(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		g.Allow(key) // count the miss
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no device is waiting for that code; check it, or start again on the device")
		return "", nil, false
	}
	if err != nil {
		writeV2Err(w, err)
		return "", nil, false
	}
	return code, d, true
}

func deviceCodeView(d *store.DeviceCode) map[string]any {
	return map[string]any{
		"deviceName": d.DeviceName,
		"createdAt":  secToMs(d.CreatedAt),
		"expiresAt":  secToMs(d.ExpiresAt),
		"role":       store.DeviceCappedRole,
	}
}

func (s *Server) handleV2DeviceCodeLookup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserCode string `json:"userCode"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if _, d, ok := s.lookupUserCode(w, r, in.UserCode); ok {
		writeJSON(w, http.StatusOK, deviceCodeView(d))
	}
}

func (s *Server) handleV2DeviceCodeDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			UserCode string `json:"userCode"`
		}
		if !readV2JSON(w, r, &in) {
			return
		}
		code, _, ok := s.lookupUserCode(w, r, in.UserCode)
		if !ok {
			return
		}
		u := currentUser(r)
		d, err := s.Store.DecideDeviceCode(r.Context(), code, u.ID, approve)
		if errors.Is(err, store.ErrNotFound) {
			writeV2Error(w, http.StatusNotFound, codeNotFound, "no device is waiting for that code any more")
			return
		}
		if err != nil {
			writeV2Err(w, err)
			return
		}
		action := "Denied device sign-in"
		if approve {
			action = "Approved device sign-in"
		}
		s.audit(r, "Users", action, d.DeviceName+" (viewer only)")
		writeJSON(w, http.StatusOK, deviceCodeView(d))
	}
}
