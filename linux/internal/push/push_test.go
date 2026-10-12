package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const tok = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func TestPayloadMatchesMacShape(t *testing.T) {
	b, err := Payload(Notification{Token: tok, Title: "Front Door", Body: "Front Door detected a person.", CameraID: "CAM-1", AlarmID: "AL-1", ThreadID: "CAM-1", Severity: "Critical"})
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	aps := p["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "Front Door" || alert["body"] != "Front Door detected a person." {
		t.Errorf("alert = %v", alert)
	}
	if aps["sound"] != "default" || aps["interruption-level"] != "time-sensitive" || aps["thread-id"] != "CAM-1" {
		t.Errorf("aps = %v", aps)
	}
	// cameraID is a top-level sibling of aps — what the iOS app reads on tap.
	if p["cameraID"] != "CAM-1" || p["alarmID"] != "AL-1" {
		t.Errorf("custom keys = %v", p)
	}
	if _, ok := p["token"]; ok {
		t.Error("device token must not be in the payload")
	}

	b, _ = Payload(Notification{Title: "Sentinel VMS", Body: "x", Severity: "Info"})
	p = nil
	_ = json.Unmarshal(b, &p)
	if _, ok := p["cameraID"]; ok {
		t.Error("no cameraID → key omitted, so a tap opens the Alarms tab")
	}
	if p["aps"].(map[string]any)["interruption-level"] != "active" {
		t.Error("Info should not be time-sensitive")
	}
	if _, err := Payload(Notification{}); err == nil {
		t.Error("empty notification should fail")
	}
}

func TestClip(t *testing.T) {
	s := strings.Repeat("é", 300) // 2 bytes each
	c := Clip(s, 101)
	if len(c) > 101 || !strings.HasSuffix(c, "…") || !utf8Valid(c) {
		t.Errorf("clip = %q (%d)", c, len(c))
	}
	if Clip("  short ", 10) != "short" {
		t.Error("short strings pass through trimmed")
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "?") == s }

// MARK: - Direct APNs

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func verifyJWT(t *testing.T, jwt string, pub *ecdsa.PublicKey) map[string]any {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature must be raw r||s, got %d bytes", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("bad JWT signature")
	}
	var h, c map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(hb, &h)
	_ = json.Unmarshal(cb, &c)
	if h["alg"] != "ES256" || h["kid"] != "ABCDEFGHIJ" || c["iss"] != "TEAM123456" {
		t.Fatalf("jwt header %v claims %v", h, c)
	}
	return c
}

func TestAPNsSendFallsBackToProduction(t *testing.T) {
	key, pemText := testKey(t)
	parsed, err := ParseP8(pemText)
	if err != nil {
		t.Fatal(err)
	}
	var sandboxHits, prodHits atomic.Int32
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sandboxHits.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"reason":"BadDeviceToken"}`))
	}))
	defer sandbox.Close()
	prod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prodHits.Add(1)
		if r.URL.Path != "/3/device/"+tok {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("apns-topic") != "com.example.sentinel" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-priority") != "10" {
			t.Errorf("headers %v", r.Header)
		}
		verifyJWT(t, strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), &key.PublicKey)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"cameraID":"CAM-1"`) {
			t.Errorf("body %s", body)
		}
		w.WriteHeader(200)
	}))
	defer prod.Close()

	a := &APNs{KeyID: "ABCDEFGHIJ", TeamID: "TEAM123456", Topic: "com.example.sentinel", Key: parsed, Production: prod.URL, Sandbox: sandbox.URL}
	n := Notification{Token: tok, Title: "Gate", Body: "Person", CameraID: "CAM-1", Severity: "Critical"}
	if err := a.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	// Second push goes straight to the environment that worked.
	if err := a.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if sandboxHits.Load() != 1 || prodHits.Load() != 2 {
		t.Errorf("sandbox %d prod %d", sandboxHits.Load(), prodHits.Load())
	}
}

func TestAPNsErrors(t *testing.T) {
	_, pemText := testKey(t)
	key, _ := ParseP8(pemText)
	status, reason := 410, "Unregistered"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
	}))
	defer srv.Close()
	a := &APNs{KeyID: "ABCDEFGHIJ", TeamID: "TEAM123456", Key: key, Production: srv.URL, Sandbox: srv.URL}
	n := Notification{Token: tok, Title: "t", Body: "b"}
	if err := a.Send(context.Background(), n); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("410 → %v", err)
	}
	status, reason = 400, "BadDeviceToken" // both environments refuse it
	if err := a.Send(context.Background(), n); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("BadDeviceToken twice → %v", err)
	}
	status, reason = 503, "ServiceUnavailable"
	err := a.Send(context.Background(), n)
	var re *RetryError
	if !errors.As(err, &re) || re.After != 7 {
		t.Errorf("503 → %v", err)
	}
	status, reason = 403, "InvalidProviderToken"
	if err := a.Send(context.Background(), n); err == nil || errors.Is(err, ErrRetry) || errors.Is(err, ErrInvalidToken) {
		t.Errorf("403 should be permanent, got %v", err)
	}
	if err := a.Send(context.Background(), Notification{Token: "zz", Title: "t"}); !errors.Is(err, ErrInvalidToken) {
		t.Error("malformed token")
	}
	if _, err := ParseP8("nope"); err == nil {
		t.Error("ParseP8 accepted garbage")
	}
}

// MARK: - Relay

type fakeRelay struct {
	mu       sync.Mutex
	tokens   map[string]bool
	syncs    int
	sent     []Notification
	sendCode int
}

func (f *fakeRelay) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/push/v1/register" {
			_ = json.NewEncoder(w).Encode(map[string]string{"installId": "inst1", "secret": "s3cret"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer inst1.s3cret" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/push/v1/devices":
			var in struct{ Tokens []string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.tokens = map[string]bool{}
			for _, x := range in.Tokens {
				f.tokens[x] = true
			}
			f.syncs++
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/push/v1/send":
			var n Notification
			_ = json.NewDecoder(r.Body).Decode(&n)
			if !f.tokens[n.Token] {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"error":"token_not_registered"}`))
				return
			}
			if f.sendCode != 0 {
				w.WriteHeader(f.sendCode)
				return
			}
			f.sent = append(f.sent, n)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	})
}

func TestRelayRegisterSyncSend(t *testing.T) {
	f := &fakeRelay{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	id, secret, err := Register(context.Background(), srv.URL, "lab", "0.5.0", nil)
	if err != nil || id != "inst1" || secret != "s3cret" {
		t.Fatalf("register: %v %v %v", id, secret, err)
	}
	r := &Relay{BaseURL: srv.URL, InstallID: id, Secret: secret, Tokens: func(context.Context) []string { return []string{tok} }}
	n := Notification{Token: tok, Title: "Gate", Body: "Person", CameraID: "CAM-1"}
	for i := 0; i < 2; i++ {
		if err := r.Send(context.Background(), n); err != nil {
			t.Fatal(err)
		}
	}
	if f.syncs != 1 || len(f.sent) != 2 || f.sent[0].CameraID != "CAM-1" {
		t.Errorf("syncs %d sent %+v", f.syncs, f.sent)
	}
	f.sendCode = 410
	if err := r.Send(context.Background(), n); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("410 → %v", err)
	}
	f.sendCode = 429
	if err := r.Send(context.Background(), n); !errors.Is(err, ErrRetry) {
		t.Errorf("429 → %v", err)
	}
	unauthorized := false
	bad := &Relay{BaseURL: srv.URL, InstallID: "inst1", Secret: "wrong", OnUnauthorized: func() { unauthorized = true }}
	if err := bad.Send(context.Background(), n); err == nil || !unauthorized {
		t.Errorf("bad secret → %v (unauthorized=%v)", err, unauthorized)
	}
	// Relay not deployed yet → a clear message.
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if _, _, err := Register(context.Background(), gone.URL, "x", "y", nil); err == nil || !strings.Contains(err.Error(), "isn't available") {
		t.Errorf("404 register → %v", err)
	}
}

// MARK: - Queue

type fakeSender struct {
	mu    sync.Mutex
	calls map[string]int
	fail  func(n Notification, call int) error
	block chan struct{}
}

func (f *fakeSender) Name() string { return "fake" }
func (f *fakeSender) Send(ctx context.Context, n Notification) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[n.Token]++
	c := f.calls[n.Token]
	f.mu.Unlock()
	if f.fail != nil {
		return f.fail(n, c)
	}
	return nil
}

func runQueue(t *testing.T, q *Queue) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	t.Cleanup(cancel)
	return cancel
}

func waitQueue(t *testing.T, q *Queue) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Wait(ctx); err != nil {
		t.Fatal("queue did not drain")
	}
}

func TestQueueRetriesThenSucceeds(t *testing.T) {
	fs := &fakeSender{fail: func(n Notification, call int) error {
		if call < 3 {
			return &RetryError{Msg: "busy"}
		}
		return nil
	}}
	q := &Queue{Sender: func() Sender { return fs }, Backoff: func(int) time.Duration { return time.Millisecond }}
	runQueue(t, q)
	q.Enqueue(Notification{Token: "t1", Title: "x"})
	waitQueue(t, q)
	st := q.Stats()
	if st.Sent != 1 || st.Retried != 2 || fs.calls["t1"] != 3 {
		t.Errorf("stats %+v calls %v", st, fs.calls)
	}
}

func TestQueueGivesUpAfterMaxAttempts(t *testing.T) {
	fs := &fakeSender{fail: func(Notification, int) error { return &RetryError{Msg: "down"} }}
	var results []error
	var mu sync.Mutex
	q := &Queue{Sender: func() Sender { return fs }, MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Millisecond },
		OnResult: func(_ Notification, err error) { mu.Lock(); results = append(results, err); mu.Unlock() }}
	runQueue(t, q)
	q.Enqueue(Notification{Token: "t1", Title: "x"})
	waitQueue(t, q)
	if fs.calls["t1"] != 3 || q.Stats().Failed != 1 || len(results) != 1 {
		t.Errorf("calls %v stats %+v results %v", fs.calls, q.Stats(), results)
	}
}

func TestQueueReportsInvalidTokensWithoutRetry(t *testing.T) {
	fs := &fakeSender{fail: func(n Notification, _ int) error {
		if n.Token == "dead" {
			return ErrInvalidToken
		}
		return nil
	}}
	var dropped []string
	var mu sync.Mutex
	q := &Queue{Sender: func() Sender { return fs }, OnInvalidToken: func(t string) { mu.Lock(); dropped = append(dropped, t); mu.Unlock() }}
	runQueue(t, q)
	q.Enqueue(Notification{Token: "dead", Title: "x"})
	q.Enqueue(Notification{Token: "live", Title: "x"})
	waitQueue(t, q)
	if len(dropped) != 1 || dropped[0] != "dead" || fs.calls["dead"] != 1 || q.Stats().Sent != 1 {
		t.Errorf("dropped %v calls %v stats %+v", dropped, fs.calls, q.Stats())
	}
}

func TestQueuePermanentErrorNotRetried(t *testing.T) {
	fs := &fakeSender{fail: func(Notification, int) error { return errors.New("bad key") }}
	q := &Queue{Sender: func() Sender { return fs }, Backoff: func(int) time.Duration { return time.Millisecond }}
	runQueue(t, q)
	q.Enqueue(Notification{Token: "t", Title: "x"})
	waitQueue(t, q)
	if fs.calls["t"] != 1 || q.Stats().LastError != "bad key" {
		t.Errorf("calls %v stats %+v", fs.calls, q.Stats())
	}
}

func TestQueueIsBoundedAndNonBlocking(t *testing.T) {
	fs := &fakeSender{block: make(chan struct{})}
	q := &Queue{Sender: func() Sender { return fs }, Size: 2, Workers: 1}
	runQueue(t, q)
	accepted := 0
	start := time.Now()
	for i := 0; i < 10; i++ {
		if q.Enqueue(Notification{Token: "t", Title: "x"}) {
			accepted++
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("Enqueue blocked")
	}
	// One may already be in the worker's hands, so 2 or 3 fit.
	if accepted < 2 || accepted > 3 || q.Stats().Dropped != 10-accepted {
		t.Errorf("accepted %d stats %+v", accepted, q.Stats())
	}
	close(fs.block)
	waitQueue(t, q)
}

func TestQueueWithoutSenderDrops(t *testing.T) {
	q := &Queue{Sender: func() Sender { return nil }}
	runQueue(t, q)
	q.Enqueue(Notification{Token: "t", Title: "x"})
	waitQueue(t, q)
	if q.Stats().Dropped != 1 || q.Stats().Sent != 0 {
		t.Errorf("stats %+v", q.Stats())
	}
}
