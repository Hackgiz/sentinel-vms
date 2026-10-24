package server

// API v2: Sentinel Cloud (WAVE12-CLOUD.md section 4, with the additions of
// WAVE13-CLOUD-ONBOARDING.md). Management Server only; a Recording Server
// has no v2 API at all, so it answers 404.
//
//	GET    /api/v2/cloud                CloudStatus, with signIn (viewLive; link only with changeSettings)
//	PUT    /api/v2/cloud                {"remote"?, "backup"?} (changeSettings, audited)
//	POST   /api/v2/cloud/link           no body: start linking, CloudStatus with link;
//	                                    {"accessToken"}: link with a signed-in account
//	                                    now, CloudStatus linked (changeSettings, audited)
//	DELETE /api/v2/cloud/link           cancel a pending link or unlink (changeSettings, audited)
//	POST   /api/v2/cloud/refresh        refresh the entitlement now (changeSettings)
//	GET    /api/v2/cloud/remote         the address phones use from outside (viewLive)
//	GET    /api/v2/cloud/backup-access  a read-only backup token for phones (viewLive, every camera)
//
// Also here: the client's wiring (StartCloud), the BackupSource the
// uploader reads alarms through, and push mode "cloud" (push.go).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/cloud"
	"sentinel-linux/internal/push"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Cloud(mux *http.ServeMux) {
	// WAVE13: everyone who sees live video may read the status lines
	// (nothing secret: a masked email, the public address, the plan).
	s.v2Route(mux, "GET /api/v2/cloud", auth.ViewLive, s.handleV2Cloud)
	s.v2Route(mux, "PUT /api/v2/cloud", auth.ChangeSettings, s.handleV2PutCloud)
	s.v2Route(mux, "POST /api/v2/cloud/link", auth.ChangeSettings, s.handleV2CloudLink)
	s.v2Route(mux, "DELETE /api/v2/cloud/link", auth.ChangeSettings, s.handleV2CloudUnlink)
	s.v2Route(mux, "POST /api/v2/cloud/refresh", auth.ChangeSettings, s.handleV2CloudRefresh)
	s.v2Route(mux, "GET /api/v2/cloud/remote", auth.ViewLive, s.handleV2CloudRemote)
	// The backup holds every camera's alarms, so only callers who see every
	// camera get a token (scope.go).
	s.v2Route(mux, "GET /api/v2/cloud/backup-access", auth.ViewLive, s.siteWide(s.handleV2CloudBackupAccess))
}

// Error codes of POST /api/v2/cloud/link with an account token
// (WAVE13-CLOUD-ONBOARDING.md, "Server API v2 additions").
const (
	codeCloudSignIn        = "cloud_sign_in"        // 401: Sentinel Cloud didn't accept the sign-in
	codeCloudServerLimit   = "cloud_server_limit"   // 402: the plan allows no more servers (+ upgradeURL)
	codeCloudAlreadyLinked = "cloud_already_linked" // 409
	codeCloudUnreachable   = "cloud_unreachable"    // 502
)

// maxCloudLinkBody bounds POST /api/v2/cloud/link's body (a session JWT is
// about a kilobyte).
const maxCloudLinkBody = 64 << 10

// Push through Sentinel Cloud: the install the relay registered (its id is
// the relay id) and its secret, and the relay address it was registered with.
const (
	cloudPushInstallSetting = "cloud_push_install"
	cloudPushSecretSetting  = "cloud_push_secret" // sealed
	cloudPushURLSetting     = "cloud_push_url"
)

// MARK: - The client

// cloudState is the per-server Sentinel Cloud state (the Server struct is
// shared with other streams, so it lives here, as shareState does).
type cloudState struct {
	client atomic.Pointer[cloud.Client]

	pushMu      sync.Mutex
	pushRunning bool
	pushTried   time.Time
}

var cloudStates sync.Map // *Server → *cloudState

func (s *Server) cloudState() *cloudState {
	if v, ok := cloudStates.Load(s); ok {
		return v.(*cloudState)
	}
	v, _ := cloudStates.LoadOrStore(s, &cloudState{})
	return v.(*cloudState)
}

// cloudClient is the attached client, nil when Sentinel Cloud isn't set up
// on this server (tests, API-only runs).
func (s *Server) cloudClient() *cloud.Client { return s.cloudState().client.Load() }

// cloudOrIdle is the attached client, or an idle one that only reads the
// settings (status, preferences).
func (s *Server) cloudOrIdle() *cloud.Client {
	if c := s.cloudClient(); c != nil {
		return c
	}
	return &cloud.Client{Store: s.Store, ServerName: s.cloudServerName}
}

// StartCloud creates the Sentinel Cloud client for this Management Server
// and starts it; origin is the loopback listener serving RemoteHandler
// that the relay forwards to. SENTINEL_CLOUD_URL and
// SENTINEL_CLOUD_INSECURE configure the control plane.
func (s *Server) StartCloud(ctx context.Context, origin string) *cloud.Client {
	c := &cloud.Client{
		BaseURL:       cloud.URLFromEnv(),
		Store:         s.Store,
		Version:       s.Version,
		Platform:      cloudPlatform(),
		Origin:        origin,
		AllowInsecure: cloud.InsecureFromEnv(),
	}
	s.runCloud(ctx, c)
	return c
}

func cloudPlatform() string {
	if runtime.GOOS == "darwin" {
		return "mac"
	}
	return "linux"
}

// runCloud attaches c (filling in what the server provides), feeds it new
// alarms and starts it.
func (s *Server) runCloud(ctx context.Context, c *cloud.Client) {
	if c.ServerName == nil {
		c.ServerName = s.cloudServerName
	}
	if c.Source == nil {
		c.Source = cloudSource{s}
	}
	c.OnLinked = s.cloudLinked
	c.OnUnlinked = s.cloudUnlinked
	c.OnEntitlement = func(e cloud.Entitlement) {
		// A server linked on a free plan gains push when the plan arrives
		// later (a subscription, or a manual grant): same rule as cloudLinked.
		if mode := s.pushMode(); e.Has(cloud.FeaturePush) && (mode == "off" || mode == "relay") {
			if err := s.Store.SetSetting(s.ctx(), pushModeSetting, "cloud"); err == nil {
				s.setPushError("")
			}
		}
		if e.Has(cloud.FeaturePush) && s.pushMode() == "cloud" {
			s.ensureCloudPush()
		}
	}
	s.cloudState().client.Store(c)
	sub := s.Bus.Subscribe("cloud-backup", 0)
	bus.Handle(sub, TopicAlarmNew, func(store.Alarm) { c.NotifyAlarm() })
	go func() {
		<-ctx.Done()
		sub.Close()
	}()
	c.Start(ctx)
}

// cloudServerName is the name the owner sees on sentvms.com: the site
// name, else the server name.
func (s *Server) cloudServerName() string {
	if n := strings.TrimSpace(s.Store.Setting(s.ctx(), "site.name", "")); n != "" {
		return n
	}
	return s.serverName()
}

// cloudLinked: the owner approved the link. Push moves to Sentinel Cloud
// unless the owner sends with their own APNs key.
func (s *Server) cloudLinked(who string, e cloud.Entitlement) {
	ctx := s.ctx()
	if who == "" {
		who = "System"
	}
	detail := e.ServerID + ", plan " + e.Plan
	if e.AccountEmailMasked != "" {
		detail += ", account " + e.AccountEmailMasked
	}
	if err := s.Store.Audit(ctx, who, "Settings", "Linked Sentinel Cloud", detail); err != nil {
		slog.Error("audit write failed", "err", err)
	}
	if mode := s.pushMode(); e.Has(cloud.FeaturePush) && (mode == "off" || mode == "relay") {
		if err := s.Store.SetSetting(ctx, pushModeSetting, "cloud"); err == nil {
			s.setPushError("")
		}
	}
	if e.Has(cloud.FeaturePush) && s.pushMode() == "cloud" {
		s.ensureCloudPush()
	}
}

// cloudUnlinked: the link is gone (unlinked here, or revoked).
func (s *Server) cloudUnlinked(reason string) {
	ctx := s.ctx()
	if s.pushMode() == "cloud" {
		_ = s.Store.SetSetting(ctx, pushModeSetting, "off")
	}
	for _, k := range []string{cloudPushInstallSetting, cloudPushSecretSetting, cloudPushURLSetting} {
		_ = s.Store.SetSetting(ctx, k, "")
	}
	if reason == "revoked" {
		_ = s.Store.Audit(ctx, "System", "Settings", "Unlinked Sentinel Cloud", "the link was removed on sentvms.com or the subscription's server was deleted")
	}
}

// cloudRemoteURL is Sentinel Cloud's address for this server while remote
// access through it applies, else "".
func (s *Server) cloudRemoteURL() string {
	if c := s.cloudClient(); c != nil {
		return c.RemoteURL(s.ctx())
	}
	return ""
}

// supabaseConnectSrc is always in the dashboard's connect-src: Sentinel
// Cloud's accounts and billing live on a Supabase project.
const supabaseConnectSrc = "https://*.supabase.co"

// cspHost is a host[:port] that can go in a Content-Security-Policy as is.
var cspHost = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

// cloudConnectSrc is what the dashboard may call besides itself (the main
// handler's connect-src; WAVE13(B)): the Sentinel Cloud card signs in to
// the account, and starts checkout and the billing portal, at Supabase
// straight from the browser. Always https://*.supabase.co, plus the
// origins of signIn's supabaseUrl, checkoutURL and portalURL when the
// cached configuration names others. It never fetches or waits.
func (s *Server) cloudConnectSrc() string {
	src := supabaseConnectSrc
	c := s.cloudClient()
	if c == nil {
		return src
	}
	cfg := c.PeekConfig()
	if cfg == nil {
		return src
	}
	seen := map[string]bool{}
	for _, raw := range []string{cfg.SupabaseURL, cfg.CheckoutURL, cfg.PortalURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !cspHost.MatchString(u.Host) {
			continue
		}
		if u.Scheme == "https" && strings.HasSuffix(strings.ToLower(u.Host), ".supabase.co") {
			continue // the wildcard covers it
		}
		if o := u.Scheme + "://" + u.Host; !seen[o] {
			seen[o] = true
			src += " " + o
		}
	}
	return src
}

// MARK: - Handlers

// cloudStatus is CloudStatus with the push state and signIn filled in.
// signIn comes from the hour-long cache and never waits for Sentinel
// Cloud: while nothing is cached it is null and fetched in the background.
// It stays null without a client: an idle one has no control plane of its
// own to ask.
func (s *Server) cloudStatus(ctx context.Context) cloud.Status {
	c := s.cloudOrIdle()
	st := c.Status(ctx)
	if attached := s.cloudClient(); attached != nil {
		st.SignIn = attached.CachedConfig()
	}
	if s.cloudClient() == nil && st.Remote.Enabled && st.Linked {
		st.Remote.Error = cloud.ErrUnavailable.Error()
	}
	if st.PeriodEnd != nil && *st.PeriodEnd == "" {
		st.PeriodEnd = nil
	}
	switch s.pushMode() {
	case "direct":
		st.Push.State = "direct"
	case "cloud":
		if st.Linked && c.Entitlement(ctx).Has(cloud.FeaturePush) {
			st.Push.State = "cloud"
		}
	}
	return st
}

func (s *Server) handleV2Cloud(w http.ResponseWriter, r *http.Request) {
	if c := s.cloudClient(); c != nil {
		// Reopening the page after subscribing shows the new plan: a cached
		// entitlement older than 5 minutes is refreshed first (at most once
		// per 5 minutes, briefly).
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		c.RefreshIfOlder(ctx, 5*time.Minute)
		cancel()
	}
	st := s.cloudStatus(r.Context())
	if u := currentUser(r); u == nil || !s.can(u.Role, auth.ChangeSettings) {
		// Viewers see the status lines. A pending link code is withheld: with
		// it, anyone could approve this server into their own account.
		st.Link = nil
	}
	// WAVE23: what the subscription unlocks, for clients to check first.
	paid := s.cloudPaid(r.Context())
	writeJSON(w, http.StatusOK, struct {
		cloud.Status
		UploadsAvailable      bool `json:"uploadsAvailable"`
		PhoneCamerasAvailable bool `json:"phoneCamerasAvailable"`
	}{st, paid, paid})
}

func (s *Server) handleV2PutCloud(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Remote *bool `json:"remote"`
		Backup *bool `json:"backup"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	c := s.cloudOrIdle()
	cur := c.Status(ctx)
	if in.Remote != nil && *in.Remote != cur.Remote.Enabled {
		if err := c.SetRemote(ctx, *in.Remote); err != nil {
			writeV2Err(w, err)
			return
		}
		s.audit(r, "Settings", "Turned "+onOffWord(*in.Remote)+" remote access", "Sentinel Cloud")
	}
	if in.Backup != nil && *in.Backup != cur.Backup.Enabled {
		if err := c.SetBackup(ctx, *in.Backup); err != nil {
			writeV2Err(w, err)
			return
		}
		s.audit(r, "Settings", "Turned "+onOffWord(*in.Backup)+" offsite backup", "Sentinel Cloud")
	}
	writeJSON(w, http.StatusOK, s.cloudStatus(ctx))
}

// handleV2CloudLink: with no body, the code flow of wave 12 (unchanged);
// with {"accessToken"}, the session token of a signed-in Sentinel Cloud
// account, the server links by itself and answers once linked (audited
// "Linked Sentinel Cloud" by cloudLinked). The token is never stored.
func (s *Server) handleV2CloudLink(w http.ResponseWriter, r *http.Request) {
	token, withAccount, ok := readCloudLinkBody(w, r)
	if !ok {
		return
	}
	c := s.cloudClient()
	if c == nil {
		writeV2Error(w, http.StatusServiceUnavailable, codeUnavailable, cloud.ErrUnavailable.Error())
		return
	}
	if withAccount {
		if err := c.LinkWithAccount(r.Context(), token, currentUser(r).Name); err != nil {
			writeCloudLinkErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.cloudStatus(r.Context()))
		return
	}
	if _, err := c.Link(r.Context(), currentUser(r).Name); err != nil {
		writeCloudErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cloudStatus(r.Context()))
}

// readCloudLinkBody reads POST /api/v2/cloud/link's optional body: none
// (or only white space) for the code flow, else {"accessToken":"…"}.
// It answers 400 itself, without echoing the body.
func readCloudLinkBody(w http.ResponseWriter, r *http.Request) (token string, withAccount, ok bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCloudLinkBody))
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid request: the body is too large or unreadable")
		return "", false, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, true
	}
	var in struct {
		AccessToken *string `json:"accessToken"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid request: "+err.Error())
		return "", false, false
	}
	if in.AccessToken == nil || strings.TrimSpace(*in.AccessToken) == "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest,
			"invalid request: accessToken is required (sign in to the Sentinel Cloud account first, or send no body to link with a code)")
		return "", false, false
	}
	return strings.TrimSpace(*in.AccessToken), true, true
}

// writeCloudLinkErr maps LinkWithAccount's errors to the codes of
// WAVE13-CLOUD-ONBOARDING.md. 402 adds upgradeURL to the error object:
// {"error":{"code":"cloud_server_limit","message":"…","upgradeURL":"…"}}.
func writeCloudLinkErr(w http.ResponseWriter, err error) {
	var le *cloud.LinkError
	var api *cloud.APIError
	switch {
	case errors.Is(err, cloud.ErrAlreadyLinked):
		writeV2Error(w, http.StatusConflict, codeCloudAlreadyLinked, err.Error())
	case errors.Is(err, cloud.ErrSignIn):
		writeV2Error(w, http.StatusUnauthorized, codeCloudSignIn, err.Error())
	case errors.Is(err, cloud.ErrServerLimit):
		upgrade := ""
		if errors.As(err, &le) {
			upgrade = le.UpgradeURL
		}
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": map[string]string{
			"code": codeCloudServerLimit, "message": err.Error(), "upgradeURL": upgrade}})
	case errors.Is(err, cloud.ErrUnreachable):
		writeV2Error(w, http.StatusBadGateway, codeCloudUnreachable, err.Error())
	case errors.As(err, &api) && api.Status == http.StatusTooManyRequests:
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "Sentinel Cloud: "+api.Error())
	default: // saving the link failed here
		writeV2Error(w, http.StatusInternalServerError, codeInternal, err.Error())
	}
}

// handleV2CloudRefresh refreshes the entitlement now (the owner just
// subscribed or changed plan), bypassing the 5-minute throttle of the
// status read but at most once per 10 seconds, and answers the status.
// A refresh that fails leaves the cached entitlement, so this answers
// 200 whatever happened; not linked, there is nothing to refresh.
func (s *Server) handleV2CloudRefresh(w http.ResponseWriter, r *http.Request) {
	if c := s.cloudClient(); c != nil && c.Linked(r.Context()) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		if err := c.ForceRefresh(ctx); err != nil {
			slog.Debug("sentinel cloud: refresh on request", "err", err)
		}
		cancel()
	}
	writeJSON(w, http.StatusOK, s.cloudStatus(r.Context()))
}

func (s *Server) handleV2CloudUnlink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := s.cloudClient()
	if c == nil {
		writeJSON(w, http.StatusOK, s.cloudStatus(ctx))
		return
	}
	c.CancelLink()
	if !c.Linked(ctx) { // only a pending code (or nothing): no server token to revoke
		writeJSON(w, http.StatusOK, s.cloudStatus(ctx))
		return
	}
	serverID := c.Status(ctx).ServerID
	remoteErr, err := c.Unlink(ctx)
	if err != nil && !errors.Is(err, cloud.ErrNotLinked) {
		writeV2Err(w, err)
		return
	}
	detail := serverID
	if remoteErr != nil {
		detail += "; Sentinel Cloud couldn't be told (" + remoteErr.Error() + "), so the server is unlinked here only"
	}
	s.audit(r, "Settings", "Unlinked Sentinel Cloud", detail)
	writeJSON(w, http.StatusOK, s.cloudStatus(ctx))
}

// writeCloudErr maps a client error: already linked 409, the control
// plane's own answer or no answer 502.
func writeCloudErr(w http.ResponseWriter, err error) {
	var api *cloud.APIError
	switch {
	case errors.Is(err, cloud.ErrAlreadyLinked):
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
	case errors.Is(err, cloud.ErrBackupOff), errors.Is(err, cloud.ErrNotLinked):
		writeV2Error(w, http.StatusNotFound, codeNotFound, err.Error())
	case errors.As(err, &api):
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, "Sentinel Cloud: "+api.Error())
	default:
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, err.Error())
	}
}

// handleV2CloudRemote: phones read this after signing in and keep the
// answer as their outside address.
func (s *Server) handleV2CloudRemote(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"remoteURL": nil, "via": nil}
	if u := s.cloudRemoteURL(); u != "" {
		out["remoteURL"], out["via"] = u, "cloud"
	} else if s.Tunnel != nil {
		if u := s.Tunnel.URL(); u != "" {
			out["remoteURL"], out["via"] = u, "tunnel"
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleV2CloudBackupAccess(w http.ResponseWriter, r *http.Request) {
	c := s.cloudClient()
	if c == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, cloud.ErrBackupOff.Error())
		return
	}
	a, err := c.BackupAccess(r.Context())
	if err != nil {
		writeCloudErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// MARK: - Push mode "cloud" (push.go)

// cloudPushReady reports whether push mode "cloud" can be chosen.
func (s *Server) cloudPushReady(ctx context.Context) bool {
	c := s.cloudClient()
	return c != nil && c.Entitlement(ctx).Has(cloud.FeaturePush)
}

// cloudPushParts is the sender key for mode "cloud": mode, relay address,
// install id, sealed secret. While not registered it starts a
// registration and returns parts that build no sender.
func (s *Server) cloudPushParts(ctx context.Context) []string {
	c := s.cloudClient()
	if c == nil || !c.Entitlement(ctx).Has(cloud.FeaturePush) {
		return []string{"cloud"}
	}
	base := c.PushRelayURL(ctx)
	install, secret := s.Store.Setting(ctx, cloudPushInstallSetting, ""), s.Store.Setting(ctx, cloudPushSecretSetting, "")
	if install == "" || secret == "" || s.Store.Setting(ctx, cloudPushURLSetting, "") != base {
		s.ensureCloudPush()
		return []string{"cloud", base}
	}
	return []string{"cloud", base, install, secret}
}

// cloudPushSender builds the relay sender from cloudPushParts; nil while
// not registered.
func (s *Server) cloudPushSender(parts []string) push.Sender {
	if len(parts) < 4 {
		return nil
	}
	secret, err := s.Store.Unseal(parts[3])
	if parts[2] == "" || err != nil || secret == "" {
		return nil
	}
	return &push.Relay{BaseURL: parts[1], InstallID: parts[2], Secret: secret,
		Tokens: func(ctx context.Context) []string {
			toks, _ := s.Store.PushTokens(ctx)
			out := make([]string, 0, len(toks))
			for _, t := range toks {
				out = append(out, t)
			}
			return out
		},
		OnUnauthorized: func() {
			_ = s.Store.SetSetting(s.ctx(), cloudPushInstallSetting, "")
			_ = s.Store.SetSetting(s.ctx(), cloudPushSecretSetting, "")
			s.setPushError("Sentinel Cloud's push relay no longer recognized this server; registering again.")
		}}
}

// ensureCloudPush registers with Sentinel Cloud's push relay in the
// background: one attempt at a time, at most once a minute.
func (s *Server) ensureCloudPush() {
	cs := s.cloudState()
	cs.pushMu.Lock()
	if cs.pushRunning || time.Since(cs.pushTried) < time.Minute {
		cs.pushMu.Unlock()
		return
	}
	cs.pushRunning, cs.pushTried = true, time.Now()
	cs.pushMu.Unlock()
	go func() {
		defer func() {
			cs.pushMu.Lock()
			cs.pushRunning = false
			cs.pushMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(s.ctx(), 30*time.Second)
		defer cancel()
		if err := s.registerCloudPush(ctx); err != nil {
			slog.Warn("sentinel cloud push registration failed", "err", err)
			s.setPushError("Couldn't register with Sentinel Cloud's push relay: " + err.Error())
		}
	}()
}

// registerCloudPush registers this server with the push relay using the
// server token (contract section 1: POST /push/v1/register).
func (s *Server) registerCloudPush(ctx context.Context) error {
	c := s.cloudClient()
	if c == nil {
		return cloud.ErrUnavailable
	}
	tok := c.ServerToken(ctx)
	if tok == "" {
		return cloud.ErrNotLinked
	}
	base := c.PushRelayURL(ctx)
	id, secret, err := (&push.Relay{BaseURL: base, RegisterAuth: "Bearer " + tok}).Register(ctx, s.cloudServerName(), s.Version)
	if err != nil {
		return err
	}
	sealed, err := s.Store.Seal(secret)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{cloudPushInstallSetting: id, cloudPushSecretSetting: sealed, cloudPushURLSetting: base} {
		if err := s.Store.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	s.setPushError("")
	return nil
}

// MARK: - Backup source

// cloudSource is what the uploader reads (cloud.BackupSource).
type cloudSource struct{ s *Server }

func (src cloudSource) LatestAlarmID(ctx context.Context) (string, error) {
	var id string
	err := src.s.Store.DB.QueryRowContext(ctx, `SELECT id FROM alarms ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (src cloudSource) AlarmsAfter(ctx context.Context, afterID string, since time.Time, limit int) ([]cloud.BackupAlarm, error) {
	db := src.s.Store.DB
	var created int64 = -1
	if afterID != "-" {
		if err := db.QueryRowContext(ctx, `SELECT created_at FROM alarms WHERE id = ?`, afterID).Scan(&created); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM alarms WHERE created_at >= ? AND (created_at > ? OR (created_at = ? AND id > ?))
		ORDER BY created_at, id LIMIT ?`, since.Unix(), created, created, afterID, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]cloud.BackupAlarm, 0, len(ids))
	for _, id := range ids {
		a, err := src.Alarm(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (src cloudSource) Alarm(ctx context.Context, id string) (cloud.BackupAlarm, error) {
	a, err := src.s.Store.Alarm(ctx, id)
	if err != nil {
		return cloud.BackupAlarm{}, err
	}
	rec, err := json.Marshal(src.s.v2AlarmView(ctx, a))
	if err != nil {
		return cloud.BackupAlarm{}, err
	}
	meta := map[string]any{"kind": a.Kind, "severity": a.Severity, "title": a.Title}
	if a.CameraID != "" {
		meta["cameraId"], meta["cameraName"] = a.CameraID, a.CameraName
	}
	return cloud.BackupAlarm{ID: a.ID, CreatedAt: time.Unix(a.CreatedAt, 0), Record: rec, Meta: meta}, nil
}

// maxSnapshot bounds one snapshot JPEG.
const maxSnapshot = 16 << 20

func (src cloudSource) Snapshot(ctx context.Context, alarmID string) ([]byte, error) {
	a, err := src.s.Store.Alarm(ctx, alarmID)
	if err != nil {
		return nil, err
	}
	if a.EventID == "" || a.CameraID == "" {
		return nil, cloud.ErrNoSnapshot // health alarms have no picture
	}
	e, err := src.s.Store.Event(ctx, a.EventID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, cloud.ErrNoSnapshot
	} else if err != nil {
		return nil, err
	}
	if e.Thumbnail == "" {
		return nil, cloud.ErrSnapshotPending
	}
	body, ok := src.s.eventThumbnail(ctx, e, maxSnapshot)
	if !ok || len(body) == 0 {
		return nil, errors.New("the recording server didn't send the snapshot")
	}
	return body, nil
}

func (src cloudSource) ClipsAfter(ctx context.Context, lockedAt time.Time, afterID string, limit int) ([]cloud.BackupClip, error) {
	rows, err := src.s.Store.DB.QueryContext(ctx, `SELECT id, alarm_id, locked_at, size FROM evidence
		WHERE alarm_id != '' AND source != 'upload' AND (locked_at > ? OR (locked_at = ? AND id > ?)) ORDER BY locked_at, id LIMIT ?`,
		lockedAt.Unix(), lockedAt.Unix(), afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cloud.BackupClip
	for rows.Next() {
		var c cloud.BackupClip
		var at int64
		if err := rows.Scan(&c.EvidenceID, &c.AlarmID, &at, &c.Size); err != nil {
			return nil, err
		}
		c.LockedAt = time.Unix(at, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// OpenClip streams the evidence file from the recording server that holds
// it, without buffering it.
func (src cloudSource) OpenClip(ctx context.Context, c cloud.BackupClip) (io.ReadCloser, error) {
	ev, err := src.s.Store.EvidenceByID(ctx, c.EvidenceID)
	if err != nil {
		return nil, err
	}
	rid, err := src.s.Store.EvidenceRecorderID(ctx, ev.ID)
	if err != nil {
		return nil, err
	}
	if !src.s.recorderConnected(rid) {
		return nil, errors.New("the recording server with this clip isn't connected")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/rs/evidence/"+filepath.Base(ev.File), nil)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	w := &pipeResponse{h: http.Header{}, pw: pw, status: make(chan int, 1)}
	go func() {
		src.s.recByID(rid).Video().ServeHTTP(w, req)
		w.finish()
	}()
	select {
	case code := <-w.status:
		if code != http.StatusOK {
			pr.Close()
			return nil, errors.New("the recording server answered " + http.StatusText(code) + " for the clip")
		}
	case <-ctx.Done():
		pr.Close()
		return nil, ctx.Err()
	}
	return pr, nil
}

// pipeResponse is a ResponseWriter that streams the body into a pipe and
// reports the status once.
type pipeResponse struct {
	h      http.Header
	pw     *io.PipeWriter
	status chan int
	once   sync.Once
	wrote  bool
}

func (p *pipeResponse) Header() http.Header { return p.h }

func (p *pipeResponse) WriteHeader(code int) {
	p.once.Do(func() { p.status <- code })
}

func (p *pipeResponse) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	p.wrote = true
	return p.pw.Write(b)
}

// finish ends the body; a handler that wrote nothing counts as a failure.
func (p *pipeResponse) finish() {
	if !p.wrote {
		p.WriteHeader(http.StatusInternalServerError)
	}
	p.pw.Close()
}
