package directory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	// FlowTTL is how long a started sign-in waits for the identity provider.
	FlowTTL = 10 * time.Minute
	// CodeTTL is how long a native client has to exchange its one-time code.
	CodeTTL = 60 * time.Second
	// providerTTL: discovery is fetched again after this long.
	providerTTL = time.Hour
	// maxPending bounds sign-ins in progress (each start costs memory).
	maxPending  = 10000
	oidcTimeout = 15 * time.Second
)

// ErrTooManyFlows: too many sign-ins are waiting on the identity provider.
var ErrTooManyFlows = errors.New("too many sign-ins in progress — try again in a few minutes")

// Flow is a sign-in waiting for the identity provider, kept server-side
// under its state value.
type Flow struct {
	Nonce       string
	Verifier    string // PKCE code verifier
	CallbackURL string // the redirect_uri sent to the provider
	ReturnTo    string // web: a path on this server
	RedirectURI string // native: where the one-time code goes
	ClientState string // native: echoed back to RedirectURI
	expires     time.Time
}

// Native reports whether the flow ends with a code for a native client.
func (f *Flow) Native() bool { return f.RedirectURI != "" }

type issuedCode struct {
	userID  string
	expires time.Time
}

// OIDC runs authorization code + PKCE sign-ins. The zero value is ready.
type OIDC struct {
	HTTPClient *http.Client // nil = a client with a 15 s timeout

	mu       sync.Mutex
	provider *oidc.Provider
	provKey  string
	provAt   time.Time
	flows    map[string]*Flow      // by state
	codes    map[string]issuedCode // by SHA-256 of the code
}

func (o *OIDC) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: oidcTimeout}
}

// Provider returns the identity provider's discovered configuration,
// cached for an hour per issuer.
func (o *OIDC) Provider(ctx context.Context, s OIDCSettings) (*oidc.Provider, error) {
	o.mu.Lock()
	if o.provider != nil && o.provKey == s.Issuer && time.Since(o.provAt) < providerTTL {
		p := o.provider
		o.mu.Unlock()
		return p, nil
	}
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, o.client()), oidcTimeout)
	defer cancel()
	p, err := oidc.NewProvider(ctx, s.Issuer)
	if err != nil {
		return nil, &UnavailableError{fmt.Errorf("identity provider discovery: %w", err)}
	}
	o.mu.Lock()
	o.provider, o.provKey, o.provAt = p, s.Issuer, time.Now()
	o.mu.Unlock()
	return p, nil
}

func (o *OIDC) oauthConfig(p *oidc.Provider, s OIDCSettings, callbackURL string) *oauth2.Config {
	return &oauth2.Config{ClientID: s.ClientID, ClientSecret: s.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: callbackURL, Scopes: s.Scopes}
}

// Begin keeps f (with a fresh nonce and PKCE verifier) under a new state
// and returns the provider's authorization URL and that state.
func (o *OIDC) Begin(ctx context.Context, s OIDCSettings, f Flow) (authURL, state string, err error) {
	p, err := o.Provider(ctx, s)
	if err != nil {
		return "", "", err
	}
	state, f.Nonce, f.Verifier = randomToken(), randomToken(), oauth2.GenerateVerifier()
	f.expires = time.Now().Add(FlowTTL)
	o.mu.Lock()
	o.pruneLocked()
	if len(o.flows) >= maxPending {
		o.mu.Unlock()
		return "", "", ErrTooManyFlows
	}
	if o.flows == nil {
		o.flows = map[string]*Flow{}
	}
	o.flows[state] = &f
	o.mu.Unlock()
	return o.oauthConfig(p, s, f.CallbackURL).AuthCodeURL(state, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), state, nil
}

// Take removes and returns the flow for state; each state works once.
func (o *OIDC) Take(state string) (*Flow, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f, ok := o.flows[state]
	if !ok {
		return nil, false
	}
	delete(o.flows, state)
	if time.Now().After(f.expires) {
		return nil, false
	}
	return f, true
}

// Finish exchanges the authorization code (with the PKCE verifier),
// verifies the ID token (signature, issuer, audience, expiry, nonce) and
// maps its claims to a role. On ErrNoRole the identity is returned too.
func (o *OIDC) Finish(ctx context.Context, s OIDCSettings, f *Flow, code string) (*Identity, error) {
	p, err := o.Provider(ctx, s)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, o.client()), oidcTimeout)
	defer cancel()
	tok, err := o.oauthConfig(p, s, f.CallbackURL).Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, fmt.Errorf("the identity provider refused the sign-in: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("the identity provider sent no ID token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: s.ClientID}).Verify(oidc.ClientContext(ctx, o.client()), raw)
	if err != nil {
		return nil, fmt.Errorf("the ID token is not valid: %w", err)
	}
	if idt.Nonce != f.Nonce {
		return nil, errors.New("the ID token is not valid: nonce mismatch")
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	name, _ := claims[s.UsernameClaim].(string)
	if name = strings.TrimSpace(name); name == "" || len(name) > maxNameLength {
		return nil, fmt.Errorf("the ID token has no %q claim to name the user", s.UsernameClaim)
	}
	id := &Identity{ExternalID: idt.Issuer + "#" + idt.Subject, Name: name, Groups: claimStrings(claims[s.GroupsClaim])}
	m, ok := MatchRole(s.RoleMappings, id.Groups, func(a, b string) bool { return a == b })
	if !ok {
		return id, ErrNoRole
	}
	id.Mapping = m
	return id, nil
}

// claimStrings reads a groups claim: a list of strings or one string.
func claimStrings(v any) []string {
	out := []string{}
	switch v := v.(type) {
	case string:
		if v != "" {
			out = append(out, v)
		}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// IssueCode creates a one-time code a native client exchanges for tokens
// within CodeTTL.
func (o *OIDC) IssueCode(userID string) string {
	code := randomToken()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked()
	if o.codes == nil {
		o.codes = map[string]issuedCode{}
	}
	o.codes[hashCode(code)] = issuedCode{userID: userID, expires: time.Now().Add(CodeTTL)}
	return code
}

// RedeemCode returns the user a code was issued for and forgets it.
func (o *OIDC) RedeemCode(code string) (userID string, ok bool) {
	if code == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	h := hashCode(code)
	c, ok := o.codes[h]
	delete(o.codes, h)
	if !ok || time.Now().After(c.expires) {
		return "", false
	}
	return c.userID, true
}

// Reset forgets the cached provider (after the settings change).
func (o *OIDC) Reset() {
	o.mu.Lock()
	o.provider = nil
	o.mu.Unlock()
}

func (o *OIDC) pruneLocked() {
	now := time.Now()
	for k, f := range o.flows {
		if now.After(f.expires) {
			delete(o.flows, k)
		}
	}
	for k, c := range o.codes {
		if now.After(c.expires) {
			delete(o.codes, k)
		}
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// MARK: - Redirect checks

// SafeReturnTo accepts only a path on this server ("/", "/live?x=1"),
// never another origin ("//evil", "https://…", "/\evil").
func SafeReturnTo(s string) (string, bool) {
	if s == "" {
		return "/", true
	}
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", false
	}
	return s, true
}

// NativeScheme is the Sentinel Operator app's URL scheme.
const NativeScheme = "sentinel-operator"

// SafeRedirectURI accepts a native client's redirect: a loopback
// http://127.0.0.1:<port>/… or http://localhost:<port>/… URL, or a
// sentinel-operator: URL.
func SafeRedirectURI(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Fragment != "" || strings.ContainsAny(s, "\r\n\t\\") {
		return false
	}
	switch u.Scheme {
	case NativeScheme:
		return true
	case "http":
		host, port := u.Hostname(), u.Port()
		if host != "127.0.0.1" && host != "localhost" {
			return false
		}
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n < 65536
	}
	return false
}
