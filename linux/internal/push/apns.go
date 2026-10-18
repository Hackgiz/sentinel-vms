package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	ProductionHost = "https://api.push.apple.com"
	SandboxHost    = "https://api.sandbox.push.apple.com"
	// DefaultTopic is the App Store Sentinel Mobile bundle id (the Mac's default).
	DefaultTopic = "com.handoffgrid.sentinel.mobile"
	jwtLifetime  = 45 * time.Minute // Apple accepts a provider token for up to 60
)

// APNs sends directly to Apple with the user's own token-based auth key.
// Like the Mac, it tries the sandbox first for a token it hasn't seen (Xcode
// installs) and falls back to production on BadDeviceToken (TestFlight / App
// Store), remembering which one worked.
type APNs struct {
	KeyID, TeamID, Topic string
	Key                  *ecdsa.PrivateKey
	Client               *http.Client // nil = default (HTTP/2 over TLS)
	Production, Sandbox  string       // hosts; tests point these at a fake

	mu      sync.Mutex
	jwt     string
	jwtAt   time.Time
	hostFor map[string]string
}

// ParseP8 reads Apple's AuthKey_XXXXXXXXXX.p8 (PKCS#8 PEM, P-256).
func ParseP8(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("not a PEM key — paste the whole AuthKey_….p8 file, including the BEGIN/END lines")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid .p8 key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve.Params().Name != "P-256" {
		return nil, errors.New("the .p8 key must be an APNs (P-256) key")
	}
	return ek, nil
}

func (a *APNs) Name() string { return "direct APNs (key " + a.KeyID + ")" }

func (a *APNs) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// token returns a cached ES256 provider JWT, minting a new one when old.
func (a *APNs) token(force bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !force && a.jwt != "" && time.Since(a.jwtAt) < jwtLifetime {
		return a.jwt, nil
	}
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	p, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": time.Now().Unix()})
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, a.Key, sum[:])
	if err != nil {
		return "", err
	}
	// JWS wants the raw 64-byte r||s, not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.jwt, a.jwtAt = input+"."+enc.EncodeToString(sig), time.Now()
	return a.jwt, nil
}

func (a *APNs) hosts(tok string) []string {
	prod, sand := a.Production, a.Sandbox
	if prod == "" {
		prod = ProductionHost
	}
	if sand == "" {
		sand = SandboxHost
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hostFor[tok] == prod {
		return []string{prod, sand}
	}
	return []string{sand, prod}
}

func (a *APNs) remember(tok, host string) {
	a.mu.Lock()
	if a.hostFor == nil {
		a.hostFor = map[string]string{}
	}
	a.hostFor[tok] = host
	a.mu.Unlock()
}

func (a *APNs) Send(ctx context.Context, n Notification) error {
	if !validToken(n.Token) {
		return ErrInvalidToken
	}
	payload, err := Payload(n)
	if err != nil {
		return err
	}
	prio := "10"
	if n.Severity == "Info" {
		prio = "5"
	}
	req := apnsRequest{token: n.Token, topic: a.topic(), pushType: "alert", priority: prio, payload: payload}
	if n.AlarmID != "" {
		req.collapseID = Clip(n.AlarmID, 64)
	}
	return a.post(ctx, req)
}

func (a *APNs) topic() string {
	if a.Topic == "" {
		return DefaultTopic
	}
	return a.Topic
}

// apnsRequest is what differs between an alert and a Live Activity push.
type apnsRequest struct {
	token, topic, pushType, priority, collapseID string
	payload                                      []byte
}

// post delivers one push: sandbox → production fallback, provider-token
// refresh, and APNs' answers mapped onto ErrInvalidToken / RetryError.
func (a *APNs) post(ctx context.Context, p apnsRequest) error {
	refreshed := false
	badToken := 0
	hosts := a.hosts(p.token)
	for i := 0; i < len(hosts); i++ {
		host := hosts[i]
		jwt, err := a.token(false)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+p.token, bytes.NewReader(p.payload))
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "bearer "+jwt)
		req.Header.Set("apns-topic", p.topic)
		req.Header.Set("apns-push-type", p.pushType)
		req.Header.Set("apns-priority", p.priority)
		if p.collapseID != "" {
			req.Header.Set("apns-collapse-id", p.collapseID)
		}
		resp, err := a.client().Do(req)
		if err != nil {
			return &RetryError{Msg: "APNs unreachable: " + err.Error()}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			a.remember(p.token, host)
			return nil
		}
		var r struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(body, &r)
		switch {
		case resp.StatusCode == http.StatusGone || r.Reason == "Unregistered":
			return ErrInvalidToken
		case r.Reason == "BadDeviceToken" || r.Reason == "BadEnvironmentKeyInToken":
			badToken++
			continue // wrong environment → try the other host
		case r.Reason == "ExpiredProviderToken" && !refreshed:
			refreshed = true
			if _, err := a.token(true); err != nil {
				return err
			}
			i-- // same host again with a fresh token
			continue
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			return &RetryError{Msg: fmt.Sprintf("APNs %d %s", resp.StatusCode, r.Reason), After: after}
		default:
			return fmt.Errorf("APNs %d: %s", resp.StatusCode, orUnknown(r.Reason))
		}
	}
	if badToken == len(hosts) {
		return ErrInvalidToken // rejected by both environments
	}
	return errors.New("APNs rejected the device token")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
