package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSignAndDelivery(t *testing.T) {
	var gotBody []byte
	var gotSig, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig, gotCT = r.Header.Get(SignatureHeader), r.Header.Get("Content-Type")
	}))
	defer srv.Close()
	d := &Dispatcher{}
	res, _ := d.Once(context.Background(), Target{ID: "w", URL: srv.URL, Secret: "s3cret"}, Payload{Event: "alarm.new", Camera: "Door", Server: "srv", TS: 42, RuleID: "r1"})
	if !res.OK || res.Status != "ok (200)" {
		t.Fatalf("%+v", res)
	}
	if gotCT != "application/json" || gotSig != Sign(gotBody, "s3cret") || gotSig[:7] != "sha256=" {
		t.Fatalf("headers ct=%q sig=%q", gotCT, gotSig)
	}
	var p Payload
	if err := json.Unmarshal(gotBody, &p); err != nil || p != (Payload{Event: "alarm.new", Camera: "Door", Server: "srv", TS: 42, RuleID: "r1"}) {
		t.Fatalf("body %s (%v)", gotBody, err)
	}
	// Independent check of the signature a receiver would compute.
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write(gotBody)
	if gotSig != "sha256="+hex.EncodeToString(m.Sum(nil)) {
		t.Fatal("signature does not verify")
	}
}

func TestNoSignatureWithoutSecret(t *testing.T) {
	var has atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := r.Header[SignatureHeader]
		has.Store(ok)
	}))
	defer srv.Close()
	(&Dispatcher{}).Once(context.Background(), Target{URL: srv.URL}, Payload{Event: "x"})
	if has.Load() {
		t.Fatal("unsigned webhook must not send a signature")
	}
}

func TestRetryThenSucceed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	results := make(chan Result, 1)
	d := &Dispatcher{Backoff: []time.Duration{time.Millisecond, time.Millisecond}, OnResult: func(_ Target, r Result) { results <- r }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue(Target{ID: "w", URL: srv.URL}, Payload{Event: "alarm.new"})
	select {
	case r := <-results:
		if !r.OK || calls.Load() != 3 {
			t.Fatalf("result %+v after %d calls", r, calls.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no result")
	}
}

func TestGivesUpAfterThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	results := make(chan Result, 1)
	d := &Dispatcher{Backoff: []time.Duration{time.Millisecond}, OnResult: func(_ Target, r Result) { results <- r }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue(Target{URL: srv.URL}, Payload{})
	r := <-results
	if r.OK || r.Status != "failed: HTTP 500" || calls.Load() != 3 {
		t.Fatalf("%+v after %d calls", r, calls.Load())
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	d := &Dispatcher{Backoff: []time.Duration{time.Millisecond}}
	res := d.deliver(context.Background(), job{Target{URL: srv.URL}, Payload{}})
	if res.OK || calls.Load() != 1 {
		t.Fatalf("%+v after %d calls", res, calls.Load())
	}
}

func TestQueueDropsOldest(t *testing.T) {
	d := &Dispatcher{QueueSize: 3}
	for i := 1; i <= 5; i++ {
		d.Enqueue(Target{ID: "w"}, Payload{TS: int64(i)})
	}
	if d.Pending() != 3 || d.Dropped() != 2 {
		t.Fatalf("pending %d dropped %d", d.Pending(), d.Dropped())
	}
	j, _ := d.pop()
	if j.p.TS != 3 {
		t.Fatalf("oldest surviving delivery is %d, want 3", j.p.TS)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits sync.Map
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Store("other", true) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	res, _ := (&Dispatcher{}).Once(context.Background(), Target{URL: srv.URL, Secret: "s"}, Payload{})
	if res.OK {
		t.Fatalf("%+v", res)
	}
	if _, followed := hits.Load("other"); followed {
		t.Fatal("redirect was followed")
	}
}

func TestSelfSignedTLSIsRejected(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	res, _ := (&Dispatcher{}).Once(context.Background(), Target{URL: srv.URL}, Payload{})
	if res.OK {
		t.Fatal("an untrusted certificate must fail; there is no verify opt-out")
	}
}

func TestValidateURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x/y", "http://", "/relative", "javascript:alert(1)"} {
		if ValidateURL(u) == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if ValidateURL("https://hooks.example.com/a?b=c") != nil || ValidateURL("http://10.0.0.5:8080/x") != nil {
		t.Error("valid URL rejected")
	}
}
