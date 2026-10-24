// Package cloud links a Management Server to Sentinel Cloud, the optional
// hosted service that adds remote access at a stable address, push through
// the Sentinel push relay and offsite backup of alarm snapshots and clips.
//
// Nothing here is needed to run Sentinel: an unlinked server behaves exactly
// as before. The control plane is whatever SENTINEL_CLOUD_URL names
// (default https://cloud.sentvms.com); the package holds no secrets of its
// own. What it stores lives in the server's settings (section 5 of the
// design contract):
//
//	cloud_server_token     the server token, sealed with the install key
//	cloud_server_id        the relay id the control plane assigned
//	cloud_entitlement      the last entitlement (JSON) and when it came
//	cloud_entitlement_at
//	cloud_remote           on | off (default on)
//	cloud_backup           on | off (default on)
//	cloud_backup_cursor    the last alarm uploaded
//
// Pieces:
//
//	client.go  Client: link (device code, or a signed-in account in one
//	           call), entitlement refresh, unlink, status, the public
//	           sign-in configuration, and starting the relay and backup
//	           when they apply
//	relay.go   Relay: the one outbound WebSocket to the relay, carrying
//	           requests for the loopback origin (frame.go is its framing)
//	backup.go  Backup: uploads alarm.json, snapshot.jpg and clip.mp4
package cloud

import (
	"context"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultURL is the Sentinel Cloud control plane.
const DefaultURL = "https://cloud.sentvms.com"

// Settings keys.
const (
	KeyToken         = "cloud_server_token" // sealed
	KeyServerID      = "cloud_server_id"
	KeyEntitlement   = "cloud_entitlement"
	KeyEntitlementAt = "cloud_entitlement_at" // RFC 3339
	KeyRemote        = "cloud_remote"         // on | off, default on
	KeyBackup        = "cloud_backup"         // on | off, default on
	KeyBackupCursor  = "cloud_backup_cursor"  // last alarm id uploaded ("-" = from the start)
	// KeyBackupClipCursor is "<lockedAt unix>:<evidence id>" of the last
	// evidence clip uploaded (clips arrive when evidence is locked, which
	// can be long after the alarm).
	KeyBackupClipCursor = "cloud_backup_clip_cursor"
)

// Features an entitlement can carry.
const (
	FeatureRelay  = "relay"
	FeaturePush   = "push"
	FeatureBackup = "backup"
)

// Remote access states (CloudStatus.remote.state).
const (
	StateOff        = "off"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateRetrying   = "retrying"
	StateInactive   = "inactive"
)

// staleAfter: an entitlement that could not be refreshed for this long
// counts as the free plan until a refresh succeeds.
const staleAfter = 7 * 24 * time.Hour

// Store is the part of the server's settings store the package uses;
// *store.Store implements it.
type Store interface {
	Setting(ctx context.Context, key, fallback string) string
	SetSetting(ctx context.Context, key, value string) error
	Seal(plain string) (string, error)
	Unseal(sealed string) (string, error)
}

// Quotas are the plan's limits.
type Quotas struct {
	BackupBytes int64 `json:"backupBytes"`
	BackupDays  int   `json:"backupDays"`
	Servers     int   `json:"servers"`
}

// Entitlement is what the control plane says this server may use
// (contract section 2).
type Entitlement struct {
	ServerID           string   `json:"serverId"`
	Plan               string   `json:"plan"`      // free | cloud | business
	Status             string   `json:"status"`    // inactive | active | grace | canceled
	PeriodEnd          *string  `json:"periodEnd"` // null for free
	Features           []string `json:"features"`
	Quotas             Quotas   `json:"quotas"`
	RelayHost          string   `json:"relayHost"`
	RelayURL           string   `json:"relayURL"`
	PushRelayURL       string   `json:"pushRelayURL"`
	BackupURL          string   `json:"backupURL"`
	AccountEmailMasked string   `json:"accountEmailMasked"`
	IssuedAt           string   `json:"issuedAt"`
	RefreshAfter       string   `json:"refreshAfter"`
}

// Paid reports whether the site has a Sentinel Cloud subscription in good
// standing: plan cloud or business, status active or grace. Video uploads
// and phone cameras need it (WAVE23-PHONE-CAMERA.md, addenda).
func (e Entitlement) Paid() bool {
	return (e.Plan == "cloud" || e.Plan == "business") && (e.Status == "active" || e.Status == "grace")
}

// Has reports whether the entitlement includes feature.
func (e Entitlement) Has(feature string) bool {
	for _, f := range e.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// free returns e with its plan taken away: what a server whose cached
// entitlement went stale may use. The addresses stay so the server can
// keep trying to refresh.
func (e Entitlement) free() Entitlement {
	e.Plan, e.Status, e.PeriodEnd, e.Features, e.Quotas = "free", "inactive", nil, []string{}, Quotas{}
	return e
}

// Status mirrors CloudStatus (contract section 4), field for field.
type Status struct {
	Linked             bool         `json:"linked"`
	ServerID           string       `json:"serverId"`
	ServerName         string       `json:"serverName"`
	AccountEmailMasked string       `json:"accountEmailMasked"`
	Plan               string       `json:"plan"`
	Status             string       `json:"status"`
	PeriodEnd          *string      `json:"periodEnd"`
	Features           []string     `json:"features"`
	Quotas             Quotas       `json:"quotas"`
	Remote             RemoteStatus `json:"remote"`
	Push               PushStatus   `json:"push"`
	Backup             BackupStatus `json:"backup"`
	Link               *LinkInfo    `json:"link,omitempty"` // only while a link is pending
	// SignIn is the control plane's public configuration (Config): what a
	// browser or the Mac app needs to sign in to a Sentinel Cloud account
	// and subscribe; null when Sentinel Cloud can't be reached. The server
	// fills it from Client.CachedConfig; Client.Status leaves it nil.
	SignIn *Config `json:"signIn"`
}

// RemoteStatus is CloudStatus.remote.
type RemoteStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"` // off | connecting | connected | retrying | inactive
	URL     string `json:"url"`   // https://<relayHost>, "" when not linked
	Error   string `json:"error"`
}

// PushStatus is CloudStatus.push. The server fills State (it owns the push
// settings): off | cloud | direct.
type PushStatus struct {
	State string `json:"state"`
}

// BackupStatus is CloudStatus.backup.
type BackupStatus struct {
	Enabled      bool    `json:"enabled"`
	BytesUsed    int64   `json:"bytesUsed"`
	Objects      int64   `json:"objects"`
	LastUploadAt *string `json:"lastUploadAt"` // RFC 3339, null before the first upload
	Error        string  `json:"error"`
}

// LinkInfo is a pending link: the code the owner approves on the website.
type LinkInfo struct {
	UserCode  string `json:"userCode"`
	VerifyURL string `json:"verifyURL"`
	ExpiresAt string `json:"expiresAt"`
}

// Config is the control plane's public configuration, GET /v1/config
// (WAVE13-CLOUD-ONBOARDING.md): where to sign in to a Sentinel Cloud
// account (Supabase, with its publishable key), where to subscribe and
// manage the plan, and the plans on sale. Nothing in it is secret.
type Config struct {
	SupabaseURL     string `json:"supabaseUrl"`
	SupabaseAnonKey string `json:"supabaseAnonKey"`
	SiteBase        string `json:"siteBase"`
	CheckoutURL     string `json:"checkoutURL"`
	PortalURL       string `json:"portalURL"`
	Plans           []Plan `json:"plans"`
}

// Plan is a Sentinel Cloud plan as Config lists it: prices in US dollars a
// month and a year, and its limits.
type Plan struct {
	ID       string  `json:"id"` // cloud | business
	Name     string  `json:"name"`
	Month    float64 `json:"month"`
	Year     float64 `json:"year"`
	Servers  int     `json:"servers"`
	BackupGB int     `json:"backupGB"`
	Days     int     `json:"days"`
}

// BackupAccess is what signed-in phones get to read the backup directly.
type BackupAccess struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	ServerID  string `json:"serverId"`
	BaseURL   string `json:"baseURL"`
}

// Errors the server maps to HTTP statuses.
var (
	ErrNotLinked     = errors.New("this server is not linked to Sentinel Cloud")
	ErrAlreadyLinked = errors.New("this server is already linked to Sentinel Cloud; unlink it first")
	ErrBackupOff     = errors.New("offsite backup is off or not part of this server's plan")
	ErrUnavailable   = errors.New("Sentinel Cloud isn't available on this server")

	// Linking with a signed-in account: LinkWithAccount fails with a
	// *LinkError wrapping one of these.
	ErrSignIn      = errors.New("sign in to your Sentinel Cloud account again")
	ErrServerLimit = errors.New("your Sentinel Cloud plan allows no more servers")
	ErrUnreachable = errors.New("couldn't reach Sentinel Cloud")
)

var tokenPattern = regexp.MustCompile(`^(scs|scb)_([a-z2-9]{10})_([A-Za-z0-9_-]{43})$`)

// ParseToken splits a server token (scs_…) or backup token (scb_…) into
// its kind, relay id and secret. ok is false when it is malformed.
func ParseToken(token string) (kind, relayID, secret string, ok bool) {
	m := tokenPattern.FindStringSubmatch(token)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// URLFromEnv is the control plane: SENTINEL_CLOUD_URL, or DefaultURL.
func URLFromEnv() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("SENTINEL_CLOUD_URL")), "/"); u != "" {
		return u
	}
	return DefaultURL
}

// InsecureFromEnv reports SENTINEL_CLOUD_INSECURE=1 (allow ws:// and http://,
// for a control plane under development).
func InsecureFromEnv() bool { return os.Getenv("SENTINEL_CLOUD_INSECURE") == "1" }

// checkURL accepts an https:// (or wss://) URL, and http:// (ws://) only
// when insecure is allowed.
func checkURL(raw string, websocket, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid Sentinel Cloud address " + raw)
	}
	secure, plain := "https", "http"
	if websocket {
		secure, plain = "wss", "ws"
	}
	switch u.Scheme {
	case secure:
		return nil
	case plain:
		if insecure {
			return nil
		}
		return errors.New("Sentinel Cloud address " + raw + " is not encrypted (set SENTINEL_CLOUD_INSECURE=1 for development)")
	}
	return errors.New("Sentinel Cloud address " + raw + " has an unsupported scheme")
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}
