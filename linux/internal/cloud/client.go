package cloud

// Client: linking (device code, or a signed-in account in one call), the
// entitlement cache, unlinking, status, the public sign-in configuration,
// and running the relay and the backup uploader while they apply.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	refreshEvery    = 6 * time.Hour
	refreshMinDelay = time.Minute
	refreshFailMin  = 5 * time.Minute
	refreshFailMax  = time.Hour
	defaultInterval = 5 * time.Second
	defaultLinkLife = 10 * time.Minute
	accessReuse     = 24 * time.Hour     // hand out the same backup token for a day
	accessMinLeft   = 7 * 24 * time.Hour // … unless it has less than this left
	forceRefreshMin = 10 * time.Second   // ForceRefresh: at most once per this
	configFor       = time.Hour          // /v1/config is cached this long …
	configRetry     = time.Minute        // … and not asked again sooner than this after a failure
	configTimeout   = 3 * time.Second    // one /v1/config fetch
	accountPollGap  = time.Second        // LinkWithAccount: between polls for the approved link
	accountCollect  = 30 * time.Second   // … and for at most this long
	maxAccountToken = 16 << 10           // a Supabase session JWT is ~1 KB
)

// Client is a server's Sentinel Cloud connection. Configure the exported
// fields, then call Start once; every method is safe for concurrent use.
type Client struct {
	BaseURL       string                // control plane; "" = URLFromEnv()
	Store         Store                 // the server's settings
	Version       string                // server version (link, relay)
	Platform      string                // "mac" | "linux"
	ServerName    func() string         // the name the owner sees on the website
	Origin        string                // loopback origin the relay forwards to; "" = no relay
	AllowInsecure bool                  // allow http:// and ws:// (development)
	Source        BackupSource          // what the backup uploads; nil = no backup
	HTTP          *http.Client          // control plane calls; nil = 30 s timeout
	UploadHTTP    *http.Client          // backup uploads; nil = default
	RelayDialer   *websocket.Dialer     // nil = default
	Tune          func(*Relay, *Backup) // tests: shorten waits

	// OnLinked runs after a link completes; who started it.
	OnLinked func(who string, e Entitlement)
	// OnUnlinked runs after this server stops being linked: reason is
	// "unlinked" (Unlink) or "revoked" (the relay closed with 4001).
	OnUnlinked func(reason string)
	// OnEntitlement runs whenever a new entitlement is stored.
	OnEntitlement func(e Entitlement)

	// Now is the clock (tests).
	Now func() time.Time
	// PollEvery overrides the link poll interval the control plane asks
	// for (tests).
	PollEvery time.Duration
	// RetryInactiveAfter is how long the relay stays down after Sentinel
	// Cloud says the plan is inactive (close 4003, or a HELLO without
	// "relay"); 0 = an hour, the contract's minimum.
	RetryInactiveAfter time.Duration

	mu         sync.Mutex
	ctx        context.Context
	refreshNow chan struct{}
	pending    *pendingLink
	relay      *Relay
	relayStop  context.CancelFunc
	relayKey   string
	backup     *Backup
	backupStop context.CancelFunc
	backupKey  string
	lastBackup BackupStatus // kept while the uploader is stopped
	access     *cachedAccess
	refreshErr string
	failures   int
	// inactiveUntil: no relay connection before this (plan inactive).
	inactiveUntil time.Time
	inactiveTimer *time.Timer
	lastOnDemand  time.Time // RefreshIfOlder's last attempt
	lastForced    time.Time // ForceRefresh's last attempt
	// rejected: /v1/entitlement answered 401. The link is kept (only the
	// relay's 4001 unlinks), but nothing runs until a refresh, a HELLO or a
	// new link succeeds.
	rejected atomic.Bool

	// linkMu: one LinkWithAccount at a time (a second one then finds the
	// server linked instead of linking it twice).
	linkMu sync.Mutex

	// The /v1/config cache: cfg (nil after a failure) holds until cfgUntil.
	// cfgFetch is held across a fetch, so callers of Config share one;
	// cfgMu guards the cached value and is never held across I/O, so
	// CachedConfig and PeekConfig never wait for a fetch.
	cfgFetch    sync.Mutex
	cfgMu       sync.Mutex
	cfg         *Config
	cfgUntil    time.Time
	cfgFetching bool // CachedConfig started a background fetch
}

type pendingLink struct {
	info       LinkInfo
	deviceCode string
	interval   time.Duration
	expires    time.Time
	cancel     context.CancelFunc
}

type cachedAccess struct {
	BackupAccess
	issued  time.Time
	expires time.Time
	token   string // the server token it was issued for
}

// APIError is a control plane error answer.
type APIError struct {
	Status     int
	Message    string
	UpgradeURL string // 402 from /v1/link/approve: where to upgrade the plan
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("Sentinel Cloud answered %d", e.Status)
}

// LinkError is why LinkWithAccount failed. Err is ErrSignIn, ErrServerLimit
// or ErrUnreachable (errors.Is works); Message is the sentence to show,
// Sentinel Cloud's own when it sent one.
type LinkError struct {
	Err        error
	Message    string
	UpgradeURL string // ErrServerLimit: where the owner upgrades their plan ("" if Sentinel Cloud sent none)
}

func (e *LinkError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Err.Error()
}

func (e *LinkError) Unwrap() error { return e.Err }

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return URLFromEnv()
}

func (c *Client) lifetime() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// Start begins the background work (relay, backup, entitlement refresh)
// for as long as ctx lasts.
func (c *Client) Start(ctx context.Context) {
	c.mu.Lock()
	if c.ctx != nil {
		c.mu.Unlock()
		return
	}
	c.ctx = ctx
	c.refreshNow = make(chan struct{}, 1)
	c.mu.Unlock()
	if err := checkURL(c.base(), false, c.AllowInsecure); err != nil {
		slog.Warn("sentinel cloud unavailable", "err", err)
	}
	c.apply()
	go c.refreshLoop(ctx)
}

// MARK: - Settings

type linkState struct {
	token, serverID string
	ent             Entitlement
	stale           bool
	rejected        bool
	linked          bool
}

// RejectedMessage is remote.error while Sentinel Cloud rejects the token.
const RejectedMessage = "Sentinel Cloud rejected this server's token; relink if this persists"

func (c *Client) load(ctx context.Context) linkState {
	var ls linkState
	sealed := c.Store.Setting(ctx, KeyToken, "")
	if sealed == "" {
		return ls
	}
	tok, err := c.Store.Unseal(sealed)
	if err != nil || tok == "" {
		return ls
	}
	ls.token, ls.linked = tok, true
	ls.serverID = c.Store.Setting(ctx, KeyServerID, "")
	if raw := c.Store.Setting(ctx, KeyEntitlement, ""); raw == "" || json.Unmarshal([]byte(raw), &ls.ent) != nil {
		ls.ent = Entitlement{ServerID: ls.serverID}.free()
	}
	at, ok := parseTime(c.Store.Setting(ctx, KeyEntitlementAt, ""))
	if !ok || c.now().Sub(at) > staleAfter {
		ls.ent, ls.stale = ls.ent.free(), true
	}
	if c.rejected.Load() && !ls.stale {
		// Keep the cached plan for display; nothing is active.
		ls.ent.Status, ls.ent.Features, ls.rejected = "inactive", []string{}, true
	}
	if ls.ent.Features == nil {
		ls.ent.Features = []string{}
	}
	return ls
}

func (c *Client) on(ctx context.Context, key string) bool {
	return c.Store.Setting(ctx, key, "on") != "off"
}

// Linked reports whether this server holds a server token.
func (c *Client) Linked(ctx context.Context) bool { return c.load(ctx).linked }

// Entitlement is the effective entitlement (free when unlinked or stale).
func (c *Client) Entitlement(ctx context.Context) Entitlement {
	ls := c.load(ctx)
	if !ls.linked {
		return Entitlement{Features: []string{}}.free()
	}
	return ls.ent
}

// ServerToken is the server token, "" when not linked. The server uses it
// to register with the push relay.
func (c *Client) ServerToken(ctx context.Context) string { return c.load(ctx).token }

// PushRelayURL is where push goes for this server.
func (c *Client) PushRelayURL(ctx context.Context) string {
	ls := c.load(ctx)
	if ls.ent.PushRelayURL != "" && checkURL(ls.ent.PushRelayURL, false, c.AllowInsecure) == nil {
		return strings.TrimRight(ls.ent.PushRelayURL, "/")
	}
	return c.base()
}

func (c *Client) relayURL(e Entitlement) string {
	if e.RelayURL != "" {
		return e.RelayURL
	}
	b := c.base()
	switch {
	case strings.HasPrefix(b, "https://"):
		return "wss://" + strings.TrimPrefix(b, "https://") + "/v1/relay"
	case strings.HasPrefix(b, "http://"):
		return "ws://" + strings.TrimPrefix(b, "http://") + "/v1/relay"
	}
	return b + "/v1/relay"
}

func (c *Client) backupURL(e Entitlement) string {
	if e.BackupURL != "" {
		return strings.TrimRight(e.BackupURL, "/")
	}
	return c.base() + "/v1/backup"
}

// validHost accepts a bare host name for the remote address.
func validHost(h string) bool {
	if h == "" || len(h) > 253 || strings.ContainsAny(h, "/:@?# ") {
		return false
	}
	return true
}

// RemoteURL is https://<relayHost> while remote access through Sentinel
// Cloud applies (linked, turned on, in the plan), else "". It does not wait
// for the relay to be connected, so phones keep one stable address.
func (c *Client) RemoteURL(ctx context.Context) string {
	ls := c.load(ctx)
	if !ls.linked || !c.on(ctx, KeyRemote) || !ls.ent.Has(FeatureRelay) || !validHost(ls.ent.RelayHost) {
		return ""
	}
	return "https://" + ls.ent.RelayHost
}

// saveEntitlement stores e as the current entitlement and re-applies.
func (c *Client) saveEntitlement(ctx context.Context, e Entitlement) {
	if id := c.Store.Setting(ctx, KeyServerID, ""); e.ServerID != "" && id != "" && e.ServerID != id {
		slog.Warn("sentinel cloud: entitlement for another server ignored", "got", e.ServerID, "want", id)
		return
	}
	if e.Features == nil {
		e.Features = []string{}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := c.Store.SetSetting(ctx, KeyEntitlement, string(raw)); err != nil {
		slog.Error("sentinel cloud: save entitlement", "err", err)
		return
	}
	_ = c.Store.SetSetting(ctx, KeyEntitlementAt, formatTime(c.now()))
	c.apply()
	if c.OnEntitlement != nil {
		c.OnEntitlement(e)
	}
}

// MARK: - Starting and stopping the relay and the backup

// apply starts or stops the relay and the uploader to match the settings
// and the entitlement.
func (c *Client) apply() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		return
	}
	ctx := c.ctx
	ls := c.load(ctx)

	// Relay.
	relayURL := c.relayURL(ls.ent)
	wantRelay := ls.linked && c.on(ctx, KeyRemote) && ls.ent.Has(FeatureRelay) && c.Origin != "" &&
		checkURL(relayURL, true, c.AllowInsecure) == nil && !c.now().Before(c.inactiveUntil)
	key := ls.token + "\x00" + relayURL
	if c.relay != nil && (!wantRelay || c.relayKey != key) {
		c.relayStop()
		c.relay, c.relayStop, c.relayKey = nil, nil, ""
	}
	if wantRelay && c.relay == nil {
		r := &Relay{URL: relayURL, Token: ls.token, Version: c.Version, Origin: c.Origin, Dialer: c.RelayDialer}
		r.OnHello = func(e Entitlement) {
			c.rejected.Store(false) // the relay accepted the token
			if !e.Has(FeatureRelay) {
				c.markInactive() // the relay closes with 4003 next
			}
			c.saveEntitlement(c.lifetime(), e)
		}
		r.OnInactive = c.markInactive
		r.OnRevoked = func() { c.revoked() }
		r.OnUnauthorized = func() { _ = c.Refresh(c.lifetime()) }
		r.RetryInactive = func(ctx context.Context) bool {
			_ = c.Refresh(ctx)
			return ctx.Err() == nil
		}
		if c.Tune != nil {
			c.Tune(r, nil)
		}
		rctx, cancel := context.WithCancel(ctx)
		c.relay, c.relayStop, c.relayKey = r, cancel, key
		go r.Run(rctx)
	}

	// Backup.
	backupURL := c.backupURL(ls.ent)
	wantBackup := ls.linked && c.on(ctx, KeyBackup) && ls.ent.Has(FeatureBackup) && c.Source != nil &&
		checkURL(backupURL, false, c.AllowInsecure) == nil
	bkey := ls.token + "\x00" + backupURL + "\x00" + strconv.Itoa(ls.ent.Quotas.BackupDays)
	if c.backup != nil && (!wantBackup || c.backupKey != bkey) {
		c.lastBackup = c.backup.Status()
		c.backupStop()
		c.backup, c.backupStop, c.backupKey = nil, nil, ""
	}
	if wantBackup && c.backup == nil {
		b := &Backup{BaseURL: backupURL, Token: ls.token, Store: c.Store, Source: c.Source, HTTP: c.UploadHTTP,
			Days: ls.ent.Quotas.BackupDays, Now: c.Now}
		b.OnUnauthorized = func() { _ = c.Refresh(c.lifetime()) }
		b.restore(c.lastBackup)
		if c.Tune != nil {
			c.Tune(nil, b)
		}
		bctx, cancel := context.WithCancel(ctx)
		c.backup, c.backupStop, c.backupKey = b, cancel, bkey
		go b.Run(bctx)
	}
}

// markInactive keeps the relay down for RetryInactiveAfter (Sentinel Cloud
// said the plan is inactive), then lets apply try again.
func (c *Client) markInactive() {
	wait := c.RetryInactiveAfter
	if wait <= 0 {
		wait = defaultInactiveWait
	}
	c.mu.Lock()
	c.inactiveUntil = c.now().Add(wait)
	if c.inactiveTimer != nil {
		c.inactiveTimer.Stop()
	}
	c.inactiveTimer = time.AfterFunc(wait, c.apply)
	c.mu.Unlock()
	go c.apply()
}

// NotifyAlarm tells the uploader a new alarm was raised.
func (c *Client) NotifyAlarm() {
	c.mu.Lock()
	b := c.backup
	c.mu.Unlock()
	if b != nil {
		b.Notify()
	}
}

// MARK: - Status

// Status is CloudStatus, except Push.State, which the server fills.
func (c *Client) Status(ctx context.Context) Status {
	ls := c.load(ctx)
	remoteOn, backupOn := c.on(ctx, KeyRemote), c.on(ctx, KeyBackup)
	st := Status{
		Plan: "free", Status: "inactive", Features: []string{},
		Remote: RemoteStatus{Enabled: remoteOn, State: StateOff},
		Push:   PushStatus{State: "off"},
		Backup: BackupStatus{Enabled: backupOn},
	}
	c.mu.Lock()
	relay, backup, last, refreshErr, inactiveUntil := c.relay, c.backup, c.lastBackup, c.refreshErr, c.inactiveUntil
	var link *LinkInfo
	if p := c.pending; p != nil && c.now().Before(p.expires) {
		info := p.info
		link = &info
	}
	c.mu.Unlock()
	if !ls.linked {
		st.Link = link // only while not linked: the poller clears it right after linking
		return st
	}
	e := ls.ent
	st.Linked, st.ServerID = true, ls.serverID
	if c.ServerName != nil {
		st.ServerName = c.ServerName()
	}
	st.AccountEmailMasked, st.Plan, st.Status, st.PeriodEnd, st.Features, st.Quotas = e.AccountEmailMasked, e.Plan, e.Status, e.PeriodEnd, e.Features, e.Quotas
	if st.Plan == "" {
		st.Plan = "free"
	}
	if validHost(e.RelayHost) {
		st.Remote.URL = "https://" + e.RelayHost
	}
	switch {
	case !remoteOn:
	case ls.rejected:
		st.Remote.State, st.Remote.Error = StateInactive, RejectedMessage
	case relay != nil:
		st.Remote.State, st.Remote.Error = relay.State()
	case ls.stale:
		st.Remote.State, st.Remote.Error = StateInactive, "Sentinel Cloud hasn't been reachable for a week"
		if refreshErr != "" {
			st.Remote.Error += ": " + refreshErr
		}
	case !e.Has(FeatureRelay):
		st.Remote.State = StateInactive
		st.Remote.Error = "Remote access isn't active on this server's Sentinel Cloud plan"
	case c.now().Before(inactiveUntil):
		st.Remote.State = StateInactive
		st.Remote.Error = "Sentinel Cloud said remote access isn't active for this server; trying again at " + inactiveUntil.Local().Format("15:04")
	case c.Origin == "":
		st.Remote.Error = "Remote access isn't available on this server"
	default:
		st.Remote.Error = "The Sentinel Cloud relay address is not usable"
	}
	b := last
	if backup != nil {
		b = backup.Status()
	}
	st.Backup.BytesUsed, st.Backup.Objects, st.Backup.LastUploadAt = b.BytesUsed, b.Objects, b.LastUploadAt
	switch {
	case !backupOn:
	case ls.rejected:
		st.Backup.Error = "Offsite backup is paused: " + RejectedMessage
	case backup != nil:
		st.Backup.Error = b.Error
	case !e.Has(FeatureBackup):
		st.Backup.Error = "Offsite backup isn't active on this server's Sentinel Cloud plan"
	case c.Source == nil:
		st.Backup.Error = "Offsite backup isn't available on this server"
	}
	return st
}

// SetRemote turns remote access through Sentinel Cloud on or off.
func (c *Client) SetRemote(ctx context.Context, on bool) error {
	if err := c.Store.SetSetting(ctx, KeyRemote, onOff(on)); err != nil {
		return err
	}
	c.apply()
	return nil
}

// SetBackup turns offsite backup on or off. Turning it off forgets the
// upload position, so turning it back on starts from new alarms instead
// of uploading everything raised in between.
func (c *Client) SetBackup(ctx context.Context, on bool) error {
	if err := c.Store.SetSetting(ctx, KeyBackup, onOff(on)); err != nil {
		return err
	}
	if !on {
		_ = c.Store.SetSetting(ctx, KeyBackupCursor, "")
		_ = c.Store.SetSetting(ctx, KeyBackupClipCursor, "")
	}
	c.apply()
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// MARK: - Control plane HTTP

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call sends a JSON request; out is filled from a 2xx answer. Non-2xx
// answers return an *APIError (with the status) as well as the status.
func (c *Client) call(ctx context.Context, method, url, token string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SentinelVMS/"+c.Version+" ("+c.Platform+")")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("couldn't reach Sentinel Cloud: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error      string `json:"error"`
			UpgradeURL string `json:"upgradeURL"`
		}
		_ = json.Unmarshal(raw, &e)
		return resp.StatusCode, &APIError{Status: resp.StatusCode, Message: e.Error, UpgradeURL: e.UpgradeURL}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("Sentinel Cloud sent an unexpected answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// MARK: - Link

type linkStart struct {
	DeviceCode string `json:"deviceCode"`
	UserCode   string `json:"userCode"`
	VerifyURL  string `json:"verifyURL"`
	ExpiresAt  string `json:"expiresAt"`
	Interval   int    `json:"interval"`
}

type linkResult struct {
	ServerToken string      `json:"serverToken"`
	ServerID    string      `json:"serverId"`
	Entitlement Entitlement `json:"entitlement"`
}

// Link asks the control plane for a link code and polls in the background
// until the owner approves it on the website or it expires. While a code
// is pending, Link returns it again. who is recorded for OnLinked.
func (c *Client) Link(ctx context.Context, who string) (LinkInfo, error) {
	if err := checkURL(c.base(), false, c.AllowInsecure); err != nil {
		return LinkInfo{}, err
	}
	if c.load(ctx).linked {
		return LinkInfo{}, ErrAlreadyLinked
	}
	c.mu.Lock()
	if p := c.pending; p != nil && c.now().Before(p.expires) {
		info := p.info
		c.mu.Unlock()
		return info, nil
	}
	c.mu.Unlock()

	name := ""
	if c.ServerName != nil {
		name = c.ServerName()
	}
	var start linkStart
	if _, err := c.call(ctx, http.MethodPost, c.base()+"/v1/link/start", "",
		map[string]string{"serverName": name, "version": c.Version, "platform": c.Platform}, &start); err != nil {
		return LinkInfo{}, err
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		return LinkInfo{}, errors.New("Sentinel Cloud sent an incomplete link code")
	}
	expires, ok := parseTime(start.ExpiresAt)
	if !ok {
		expires = c.now().Add(defaultLinkLife)
	}
	interval := time.Duration(start.Interval) * time.Second
	if interval < 2*time.Second || interval > time.Minute {
		interval = defaultInterval
	}
	if c.PollEvery > 0 {
		interval = c.PollEvery
	}
	info := LinkInfo{UserCode: start.UserCode, VerifyURL: start.VerifyURL, ExpiresAt: formatTime(expires)}
	pctx, cancel := context.WithDeadline(c.lifetime(), expires)
	p := &pendingLink{info: info, deviceCode: start.DeviceCode, interval: interval, expires: expires, cancel: cancel}
	c.mu.Lock()
	if old := c.pending; old != nil {
		old.cancel()
	}
	c.pending = p
	c.mu.Unlock()
	go c.poll(pctx, p, who)
	return info, nil
}

// CancelLink drops a pending link code; false when none was pending.
func (c *Client) CancelLink() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pending
	if p == nil {
		return false
	}
	p.cancel()
	c.pending = nil
	return true
}

func (c *Client) clearPending(p *pendingLink) {
	c.mu.Lock()
	if c.pending == p {
		c.pending = nil
	}
	c.mu.Unlock()
	p.cancel()
}

// poll waits for the owner to approve the link.
func (c *Client) poll(ctx context.Context, p *pendingLink, who string) {
	defer c.clearPending(p)
	interval := p.interval
	for {
		if !sleep(ctx, interval) {
			return
		}
		var res linkResult
		status, err := c.call(ctx, http.MethodPost, c.base()+"/v1/link/poll", "", map[string]string{"deviceCode": p.deviceCode}, &res)
		switch {
		case status == http.StatusAccepted:
			continue
		case status == http.StatusOK && err == nil:
			if err := c.completeLink(ctx, res); err != nil {
				slog.Warn("sentinel cloud: link failed", "err", err)
				return
			}
			slog.Info("sentinel cloud: linked", "server", res.ServerID)
			if c.OnLinked != nil {
				c.OnLinked(who, c.Entitlement(ctx))
			}
			return
		case status == http.StatusNotFound:
			slog.Info("sentinel cloud: link code expired")
			return
		case status == http.StatusTooManyRequests:
			interval += defaultInterval
		case err != nil:
			slog.Debug("sentinel cloud: link poll", "err", err)
		}
	}
}

// completeLink stores the token (sealed), the server id and the
// entitlement, then starts whatever now applies.
func (c *Client) completeLink(ctx context.Context, res linkResult) error {
	kind, relayID, _, ok := ParseToken(res.ServerToken)
	if !ok || kind != "scs" || relayID != res.ServerID {
		return errMalformedToken
	}
	sealed, err := c.Store.Seal(res.ServerToken)
	if err != nil {
		return err
	}
	e := res.Entitlement
	if e.ServerID == "" {
		e.ServerID = res.ServerID
	}
	if e.Features == nil {
		e.Features = []string{}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.inactiveUntil = time.Time{} // a new link starts fresh
	c.mu.Unlock()
	c.rejected.Store(false)
	// The token goes last: once it is there the server counts as linked,
	// with its entitlement already in place.
	for _, kv := range [][2]string{{KeyEntitlement, string(raw)}, {KeyEntitlementAt, formatTime(c.now())}, {KeyServerID, res.ServerID},
		{KeyBackupCursor, ""}, {KeyBackupClipCursor, ""}, {KeyToken, sealed}} {
		if err := c.Store.SetSetting(ctx, kv[0], kv[1]); err != nil {
			return err
		}
	}
	c.apply()
	if c.OnEntitlement != nil {
		c.OnEntitlement(e)
	}
	return nil
}

var errMalformedToken = errors.New("Sentinel Cloud sent a malformed server token")

// MARK: - Link with a signed-in account

// LinkWithAccount links this server to the Sentinel Cloud account that jwt
// (the session token a browser or the Mac app got by signing in to the
// account) belongs to, in one call and without showing a code: it starts a
// link code, approves it as the account, collects the server token and
// saves the link exactly as the code flow does; OnLinked runs with who.
// The token is sent once, to approve the code, and never stored or logged.
// A pending code from Link is dropped.
//
// Errors: ErrAlreadyLinked; a *LinkError wrapping ErrSignIn (Sentinel Cloud
// didn't accept the sign-in), ErrServerLimit (the plan allows no more
// servers; UpgradeURL says where to upgrade) or ErrUnreachable; an
// *APIError with Status 429 when Sentinel Cloud asks to slow down.
func (c *Client) LinkWithAccount(ctx context.Context, jwt, who string) error {
	if err := checkURL(c.base(), false, c.AllowInsecure); err != nil {
		return &LinkError{Err: ErrUnreachable, Message: err.Error()}
	}
	if !validAccountToken(jwt) {
		return &LinkError{Err: ErrSignIn}
	}
	c.linkMu.Lock()
	defer c.linkMu.Unlock()
	if c.load(ctx).linked {
		return ErrAlreadyLinked
	}
	c.CancelLink() // the owner signed in instead of entering the code

	name := ""
	if c.ServerName != nil {
		name = c.ServerName()
	}
	var start linkStart
	status, err := c.call(ctx, http.MethodPost, c.base()+"/v1/link/start", "",
		map[string]string{"serverName": name, "version": c.Version, "platform": c.Platform}, &start)
	if err != nil {
		return c.accountLinkErr(status, err)
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		return &LinkError{Err: ErrUnreachable, Message: "Sentinel Cloud sent an incomplete link code"}
	}
	status, err = c.call(ctx, http.MethodPost, c.base()+"/v1/link/approve", jwt, map[string]string{"userCode": start.UserCode}, nil)
	if err != nil {
		return c.accountLinkErr(status, err)
	}

	// Approved: finish even if the caller goes away now, so the account
	// doesn't end up with a server that this one doesn't know it is.
	cctx, cancel := context.WithTimeout(c.lifetime(), accountCollect)
	defer cancel()
	res, err := c.collect(cctx, start.DeviceCode)
	if err != nil {
		return err
	}
	if err := c.completeLink(cctx, res); err != nil {
		if errors.Is(err, errMalformedToken) {
			return &LinkError{Err: ErrUnreachable, Message: err.Error()}
		}
		return err
	}
	c.CancelLink() // a code started meanwhile must not link over this
	slog.Info("sentinel cloud: linked with an account", "server", res.ServerID)
	if c.OnLinked != nil {
		c.OnLinked(who, c.Entitlement(cctx))
	}
	return nil
}

// collect polls for the server token of a link code that is already
// approved: right away, then every second (PollEvery in tests) for at most
// as long as ctx lasts.
func (c *Client) collect(ctx context.Context, deviceCode string) (linkResult, error) {
	gap := accountPollGap
	if c.PollEvery > 0 {
		gap = c.PollEvery
	}
	var wait time.Duration
	var last error
	for {
		if wait > 0 && !sleep(ctx, wait) {
			break
		}
		var res linkResult
		status, err := c.call(ctx, http.MethodPost, c.base()+"/v1/link/poll", "", map[string]string{"deviceCode": deviceCode}, &res)
		if status == http.StatusOK {
			if err != nil { // the token goes out once: an answer that can't be read is lost
				return linkResult{}, &LinkError{Err: ErrUnreachable, Message: err.Error()}
			}
			return res, nil
		}
		if ctx.Err() != nil {
			break
		}
		switch {
		case status == http.StatusAccepted:
			wait, last = gap, nil
		case status == http.StatusTooManyRequests:
			wait, last = gap+defaultInterval, err
		case status == 0 || status >= 500: // no answer, or a hiccup: try again
			wait, last = gap, err
		default: // 404 (the code expired or was collected) or another refusal
			why := fmt.Sprintf("Sentinel Cloud answered %d", status)
			if err != nil {
				why = err.Error()
			}
			return linkResult{}, &LinkError{Err: ErrUnreachable, Message: "Sentinel Cloud couldn't finish linking (" + why + "); try again"}
		}
	}
	msg := "Sentinel Cloud didn't finish linking in time; try again"
	if last != nil {
		msg = "Sentinel Cloud didn't finish linking (" + last.Error() + "); try again"
	}
	return linkResult{}, &LinkError{Err: ErrUnreachable, Message: msg}
}

// accountLinkErr maps a failed /v1/link/start or /v1/link/approve.
func (c *Client) accountLinkErr(status int, err error) error {
	var api *APIError
	if !errors.As(err, &api) { // no answer, or one that can't be read
		return &LinkError{Err: ErrUnreachable, Message: err.Error()}
	}
	switch status {
	case http.StatusUnauthorized:
		return &LinkError{Err: ErrSignIn, Message: api.Message}
	case http.StatusForbidden: // the account's email address isn't confirmed yet
		return &LinkError{Err: ErrSignIn, Message: "Confirm your Sentinel Cloud account's email address first, then sign in again"}
	case http.StatusPaymentRequired:
		upgrade := api.UpgradeURL
		if upgrade != "" && checkURL(upgrade, false, c.AllowInsecure) != nil {
			upgrade = ""
		}
		return &LinkError{Err: ErrServerLimit, Message: api.Message, UpgradeURL: upgrade}
	case http.StatusTooManyRequests:
		return api
	}
	return &LinkError{Err: ErrUnreachable, Message: "Sentinel Cloud: " + api.Error()}
}

// validAccountToken: a plausible bearer token (a JWT is three base64url
// parts). Anything else could never be accepted and mustn't reach a header.
func validAccountToken(t string) bool {
	if t == "" || len(t) > maxAccountToken {
		return false
	}
	for _, r := range t {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', strings.ContainsRune("-._~+/=", r):
		default:
			return false
		}
	}
	return true
}

// MARK: - Refresh and unlink

// Refresh fetches the entitlement now. A 401 marks the token rejected:
// the link is kept, nothing runs, and the refresh is retried hourly.
func (c *Client) Refresh(ctx context.Context) error {
	ls := c.load(ctx)
	if !ls.linked {
		return ErrNotLinked
	}
	var e Entitlement
	status, err := c.call(ctx, http.MethodGet, c.base()+"/v1/entitlement", ls.token, nil, &e)
	c.mu.Lock()
	if err != nil {
		c.refreshErr = err.Error()
		c.failures++
	} else {
		c.refreshErr, c.failures = "", 0
	}
	c.mu.Unlock()
	if status == http.StatusUnauthorized {
		// Not an unlink: a control plane bug must not unlink every server.
		// Only the relay's 4001 close (an explicit unlink or revoke) does.
		if !c.rejected.Swap(true) {
			slog.Warn("sentinel cloud: the control plane rejected this server's token; keeping the link, retrying hourly")
		}
		c.apply()
		return err
	}
	if err != nil {
		return err
	}
	c.rejected.Store(false)
	if e.Has(FeatureRelay) { // the control plane says the plan is active again: don't wait out the hour
		c.mu.Lock()
		c.inactiveUntil = time.Time{}
		if c.inactiveTimer != nil {
			c.inactiveTimer.Stop()
		}
		c.mu.Unlock()
	}
	c.saveEntitlement(ctx, e)
	return nil
}

// RefreshIfOlder refreshes the entitlement when the cached one is older
// than maxAge, trying at most once per maxAge (Sentinel Admin and the Mac
// pane reading the status, so a new subscription shows up on reopening).
func (c *Client) RefreshIfOlder(ctx context.Context, maxAge time.Duration) {
	ls := c.load(ctx)
	if !ls.linked {
		return
	}
	now := c.now()
	if at, ok := parseTime(c.Store.Setting(ctx, KeyEntitlementAt, "")); ok && now.Sub(at) < maxAge {
		return
	}
	c.mu.Lock()
	if now.Sub(c.lastOnDemand) < maxAge {
		c.mu.Unlock()
		return
	}
	c.lastOnDemand = now
	c.mu.Unlock()
	if err := c.Refresh(ctx); err != nil {
		slog.Debug("sentinel cloud: on-demand refresh", "err", err)
	}
}

// ForceRefresh fetches the entitlement now whatever its age, bypassing
// RefreshIfOlder's throttle (the owner just subscribed or changed plan),
// but at most once per 10 seconds: a call sooner than that does nothing
// and returns nil, leaving the cached entitlement. ErrNotLinked when not
// linked.
func (c *Client) ForceRefresh(ctx context.Context) error {
	if !c.load(ctx).linked {
		return ErrNotLinked
	}
	now := c.now()
	c.mu.Lock()
	if !c.lastForced.IsZero() && now.Sub(c.lastForced) < forceRefreshMin {
		c.mu.Unlock()
		return nil
	}
	c.lastForced = now
	c.mu.Unlock()
	return c.Refresh(ctx)
}

// MARK: - Public configuration

// Config is the control plane's public configuration (GET /v1/config):
// what a browser or the Mac app needs to sign in to a Sentinel Cloud
// account and to subscribe. It is cached for an hour. nil when it can't be
// fetched within 3 seconds (asked again no sooner than a minute later) or
// isn't usable; the caller gets its own copy.
func (c *Client) Config(ctx context.Context) *Config {
	c.cfgFetch.Lock()
	defer c.cfgFetch.Unlock()
	c.cfgMu.Lock()
	cfg, fresh := c.cfg, c.now().Before(c.cfgUntil)
	c.cfgMu.Unlock()
	if fresh {
		return cfg.clone()
	}
	cfg, err := c.fetchConfig(ctx)
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	if err != nil {
		if ctx.Err() == nil { // the caller giving up isn't Sentinel Cloud's failure
			c.cfg, c.cfgUntil = nil, c.now().Add(configRetry)
		}
		slog.Debug("sentinel cloud: configuration", "err", err)
		return nil
	}
	c.cfg, c.cfgUntil = cfg, c.now().Add(configFor)
	return cfg.clone()
}

// CachedConfig is Config without waiting, for status reads: the cached
// configuration, or nil when there is none (yet, or Sentinel Cloud
// couldn't be reached). When there is none, or it is over an hour old, it
// fetches one in the background (Config's limits apply) for the next
// caller, and meanwhile still returns the old one.
func (c *Client) CachedConfig() *Config {
	c.cfgMu.Lock()
	out := c.cfg.clone()
	start := !c.now().Before(c.cfgUntil) && !c.cfgFetching
	if start {
		c.cfgFetching = true
	}
	c.cfgMu.Unlock()
	if start {
		go func() {
			defer func() {
				c.cfgMu.Lock()
				c.cfgFetching = false
				c.cfgMu.Unlock()
			}()
			// The client's lifetime, not a caller's: only Config's own
			// time limit ends it, and that counts as a failure.
			c.Config(c.lifetime())
		}()
	}
	return out
}

// PeekConfig is the configuration last fetched (even over an hour ago),
// nil if none; it never fetches or waits.
func (c *Client) PeekConfig() *Config {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	return c.cfg.clone()
}

func (c *Client) fetchConfig(ctx context.Context) (*Config, error) {
	if err := checkURL(c.base(), false, c.AllowInsecure); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	var cfg Config
	if _, err := c.call(ctx, http.MethodGet, c.base()+"/v1/config", "", nil, &cfg); err != nil {
		return nil, err
	}
	// Browsers send the account password to supabaseUrl: only over HTTPS
	// (or plain HTTP while developing with AllowInsecure).
	if cfg.SupabaseURL == "" || cfg.SupabaseAnonKey == "" {
		return nil, errors.New("Sentinel Cloud's configuration has no sign-in service")
	}
	for _, u := range []string{cfg.SupabaseURL, cfg.SiteBase, cfg.CheckoutURL, cfg.PortalURL} {
		if u == "" {
			continue
		}
		if err := checkURL(u, false, c.AllowInsecure); err != nil {
			return nil, err
		}
	}
	if cfg.Plans == nil {
		cfg.Plans = []Plan{}
	}
	return &cfg, nil
}

func (cfg *Config) clone() *Config {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Plans = append([]Plan{}, cfg.Plans...)
	return &out
}

// RefreshSoon asks the refresh loop for an early refresh.
func (c *Client) RefreshSoon() {
	c.mu.Lock()
	ch := c.refreshNow
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (c *Client) refreshLoop(ctx context.Context) {
	for {
		t := time.NewTimer(c.nextRefresh(ctx))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-c.refreshNow:
			t.Stop()
		}
		if c.load(ctx).linked {
			if err := c.Refresh(ctx); err != nil && !errors.Is(err, ErrNotLinked) {
				slog.Warn("sentinel cloud: entitlement refresh failed", "err", err)
			}
		}
	}
}

// nextRefresh: every 6 h or at the entitlement's refreshAfter if sooner;
// after failures, 5 min doubling up to an hour.
func (c *Client) nextRefresh(ctx context.Context) time.Duration {
	c.mu.Lock()
	failures := c.failures
	c.mu.Unlock()
	if c.rejected.Load() {
		return refreshFailMax // token rejected: try again hourly
	}
	if failures > 0 {
		d := refreshFailMin << min(failures-1, 4)
		return min(d, refreshFailMax)
	}
	ls := c.load(ctx)
	if !ls.linked {
		return refreshEvery
	}
	if ls.stale {
		return refreshFailMin
	}
	d := refreshEvery
	if t, ok := parseTime(ls.ent.RefreshAfter); ok {
		d = min(d, t.Sub(c.now()))
	}
	return max(d, refreshMinDelay)
}

// Unlink tells the control plane to forget this server and forgets the
// link locally either way. remoteErr is set when the control plane could
// not be told (the server is then unlinked here only).
func (c *Client) Unlink(ctx context.Context) (remoteErr error, err error) {
	c.CancelLink()
	ls := c.load(ctx)
	if !ls.linked {
		return nil, ErrNotLinked
	}
	status, rerr := c.call(ctx, http.MethodDelete, c.base()+"/v1/servers/self", ls.token, nil, nil)
	if rerr != nil && status != http.StatusUnauthorized && status != http.StatusNotFound {
		remoteErr = rerr
	}
	if err := c.forget(ctx); err != nil {
		return remoteErr, err
	}
	if c.OnUnlinked != nil {
		c.OnUnlinked("unlinked")
	}
	return remoteErr, nil
}

// revoked: the relay closed with 4001 (the link was removed or revoked);
// the only automatic unlink.
func (c *Client) revoked() {
	ctx := c.lifetime()
	if !c.load(ctx).linked {
		return
	}
	slog.Warn("sentinel cloud: this server's link was revoked; unlinked locally")
	if err := c.forget(ctx); err != nil {
		slog.Error("sentinel cloud: forget link", "err", err)
		return
	}
	if c.OnUnlinked != nil {
		c.OnUnlinked("revoked")
	}
}

// forget clears the link settings and stops the relay and the uploader.
func (c *Client) forget(ctx context.Context) error {
	for _, k := range []string{KeyToken, KeyServerID, KeyEntitlement, KeyEntitlementAt, KeyBackupCursor, KeyBackupClipCursor} {
		if err := c.Store.SetSetting(ctx, k, ""); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.access, c.lastBackup, c.refreshErr, c.failures = nil, BackupStatus{}, "", 0
	c.mu.Unlock()
	c.rejected.Store(false)
	c.apply()
	return nil
}

// MARK: - Backup access for phones

// BackupAccess returns a read-only backup token for signed-in phones,
// reusing one for a day. ErrBackupOff when backup is off, not linked or
// not in the plan.
func (c *Client) BackupAccess(ctx context.Context) (BackupAccess, error) {
	ls := c.load(ctx)
	if !ls.linked || !c.on(ctx, KeyBackup) || !ls.ent.Has(FeatureBackup) {
		return BackupAccess{}, ErrBackupOff
	}
	now := c.now()
	c.mu.Lock()
	if a := c.access; a != nil && a.token == ls.token && now.Sub(a.issued) < accessReuse && a.expires.Sub(now) > accessMinLeft {
		out := a.BackupAccess
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	base := c.backupURL(ls.ent)
	if err := checkURL(base, false, c.AllowInsecure); err != nil {
		return BackupAccess{}, err
	}
	var res struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if _, err := c.call(ctx, http.MethodPost, base+"/tokens", ls.token, struct{}{}, &res); err != nil {
		return BackupAccess{}, err
	}
	kind, relayID, _, ok := ParseToken(res.Token)
	if !ok || kind != "scb" || (ls.serverID != "" && relayID != ls.serverID) {
		return BackupAccess{}, errors.New("Sentinel Cloud sent a malformed backup token")
	}
	exp, ok := parseTime(res.ExpiresAt)
	if !ok {
		exp = now.Add(30 * 24 * time.Hour)
	}
	out := BackupAccess{Token: res.Token, ExpiresAt: formatTime(exp), ServerID: ls.serverID, BaseURL: base}
	c.mu.Lock()
	c.access = &cachedAccess{BackupAccess: out, issued: now, expires: exp, token: ls.token}
	c.mu.Unlock()
	return out, nil
}
