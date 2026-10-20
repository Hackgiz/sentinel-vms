package push

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRelayURL is the Sentinel gateway (the same Worker that takes
// feedback and update checks). The /push/v1 routes are not deployed yet —
// see sentvms-dl-worker/src/push-relay.js.
const DefaultRelayURL = "https://dl.sentvms.com"

// Relay protocol (v1), all JSON over HTTPS:
//
//	POST /push/v1/register  {"name","version"}            → 200 {"installId","secret"}
//	PUT  /push/v1/devices   {"tokens":[hex…]}              → 200 {"ok":true,"count":n}
//	POST /push/v1/send      Notification (without secrets) → 200 {"ok":true}
//
// /devices and /send carry "Authorization: Bearer <installId>.<secret>".
// /send answers 410 for a dead token, 403 {"error":"token_not_registered"}
// for a token this install never registered, 401 for an unknown install and
// 429 (with Retry-After) when a rate limit is hit.
type Relay struct {
	BaseURL   string
	InstallID string
	Secret    string
	Client    *http.Client
	// OnUnauthorized is called when the relay no longer knows this install,
	// so the server can drop its credentials and register again.
	OnUnauthorized func()
	// Tokens, when set, lists every device token this server pushes to; the
	// set is synced to the relay before a send whenever it has changed.
	Tokens func(ctx context.Context) []string
	// RegisterAuth, when set, is sent as the Authorization header of
	// /push/v1/register: Sentinel Cloud's relay wants "Bearer <server token>".
	RegisterAuth string // WAVE12(C)

	mu     sync.Mutex
	synced string // hash of the token set the relay last accepted
}

func (r *Relay) Name() string { return "Sentinel push relay" }

func (r *Relay) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (r *Relay) base() string {
	if r.BaseURL == "" {
		return DefaultRelayURL
	}
	return strings.TrimRight(r.BaseURL, "/")
}

func (r *Relay) do(ctx context.Context, method, path string, in, out any, authed bool) (int, http.Header, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, method, r.base()+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SentinelVMS-Linux")
	if authed {
		req.Header.Set("Authorization", "Bearer "+r.InstallID+"."+r.Secret)
	} else if r.RegisterAuth != "" { // WAVE12(C)
		req.Header.Set("Authorization", r.RegisterAuth)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, resp.Header, fmt.Errorf("relay sent an unexpected reply: %w", err)
		}
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, resp.Header, &relayError{status: resp.StatusCode, code: e.Error}
	}
	return resp.StatusCode, resp.Header, nil
}

type relayError struct {
	status int
	code   string
}

func (e *relayError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("push relay %d: %s", e.status, e.code)
	}
	return fmt.Sprintf("push relay %d", e.status)
}

// Register creates this server's install on the relay. The secret is shown
// once; the caller stores it sealed.
func Register(ctx context.Context, baseURL, name, version string, client *http.Client) (installID, secret string, err error) {
	return (&Relay{BaseURL: baseURL, Client: client}).Register(ctx, name, version)
}

// Register creates this server's install on r's relay, sending RegisterAuth
// when set. WAVE12(C)
func (r *Relay) Register(ctx context.Context, name, version string) (installID, secret string, err error) {
	var out struct {
		InstallID string `json:"installId"`
		Secret    string `json:"secret"`
	}
	status, _, err := r.do(ctx, http.MethodPost, "/push/v1/register", map[string]string{"name": name, "version": version}, &out, false)
	if status == http.StatusNotFound {
		return "", "", errors.New("the Sentinel push relay isn't available at " + r.base() + " yet")
	}
	if err != nil {
		return "", "", err
	}
	if out.InstallID == "" || out.Secret == "" {
		return "", "", errors.New("relay did not return credentials")
	}
	return out.InstallID, out.Secret, nil
}

func tokenSetHash(tokens []string) string {
	t := append([]string(nil), tokens...)
	sort.Strings(t)
	h := sha256.Sum256([]byte(strings.Join(t, ",")))
	return hex.EncodeToString(h[:])
}

// SyncTokens tells the relay which device tokens belong to this install
// (replacing the previous set). Skipped when the set hasn't changed.
func (r *Relay) SyncTokens(ctx context.Context, tokens []string) error {
	h := tokenSetHash(tokens)
	r.mu.Lock()
	same := r.synced == h
	r.mu.Unlock()
	if same {
		return nil
	}
	if tokens == nil {
		tokens = []string{}
	}
	status, _, err := r.do(ctx, http.MethodPut, "/push/v1/devices", map[string]any{"tokens": tokens}, nil, true)
	if status == http.StatusUnauthorized && r.OnUnauthorized != nil {
		r.OnUnauthorized()
	}
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.synced = h
	r.mu.Unlock()
	return nil
}

func (r *Relay) Send(ctx context.Context, n Notification) error {
	if !validToken(n.Token) {
		return ErrInvalidToken
	}
	if r.Tokens != nil {
		if err := r.SyncTokens(ctx, r.Tokens(ctx)); err != nil {
			var re *relayError
			if errors.As(err, &re) && re.status < 500 && re.status != http.StatusTooManyRequests {
				return err
			}
			return &RetryError{Msg: "push relay: " + err.Error()}
		}
	}
	n.Title, n.Body = Clip(n.Title, maxTitle), Clip(n.Body, maxBody)
	status, hdr, err := r.do(ctx, http.MethodPost, "/push/v1/send", n, nil, true)
	return r.sendResult(status, hdr, err)
}

// sendResult maps the relay's answer to a send onto ErrInvalidToken /
// RetryError (shared by alerts and Live Activity pushes).
func (r *Relay) sendResult(status int, hdr http.Header, err error) error {
	if err == nil {
		return nil
	}
	var re *relayError
	if !errors.As(err, &re) {
		return &RetryError{Msg: "push relay unreachable: " + err.Error()}
	}
	switch {
	case status == http.StatusGone:
		return ErrInvalidToken
	case status == http.StatusForbidden && re.code == "token_not_registered":
		r.mu.Lock()
		r.synced = "" // resync before the retry
		r.mu.Unlock()
		return &RetryError{Msg: err.Error()}
	case status == http.StatusUnauthorized:
		if r.OnUnauthorized != nil {
			r.OnUnauthorized()
		}
		return err
	case status == http.StatusTooManyRequests || status >= 500:
		after, _ := strconv.Atoi(hdr.Get("Retry-After"))
		return &RetryError{Msg: err.Error(), After: after}
	}
	return err
}
