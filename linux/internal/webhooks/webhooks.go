// Package webhooks delivers Sentinel's outbound webhooks: a JSON POST per
// event, signed with HMAC-SHA256 when the webhook has a secret, sent from a
// bounded asynchronous queue with a small retry budget.
//
//	POST <url>
//	Content-Type: application/json
//	X-Sentinel-Signature: sha256=<hex HMAC-SHA256(body, secret)>   (only with a secret)
//	{"event":"alarm.new","camera":"Front door","server":"site-1","ts":1700000000,"ruleId":"…"}
//
// Each attempt has a 5 s timeout and TLS certificates are always verified
// (there is deliberately no opt-out). A delivery makes up to 3 attempts with
// backoff; 2xx is success, other 4xx answers are final (except 408/429).
// When the queue is full the oldest pending delivery is dropped with a
// warning, so a dead endpoint can never grow memory or block the site.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// SignatureHeader carries "sha256=<hex>".
	SignatureHeader = "X-Sentinel-Signature"
	// Timeout bounds one attempt.
	Timeout = 5 * time.Second
	// Attempts per delivery.
	Attempts = 3
	// DefaultQueue is the bound on pending deliveries.
	DefaultQueue = 256
)

// Payload is the JSON body of every delivery.
type Payload struct {
	Event  string `json:"event"`
	Camera string `json:"camera"`
	Server string `json:"server"`
	TS     int64  `json:"ts"` // unix seconds
	RuleID string `json:"ruleId,omitempty"`
}

// Target is where and how to deliver. Secret may be empty (unsigned).
type Target struct {
	ID     string
	URL    string
	Secret string
}

// Sign returns the X-Sentinel-Signature value for body.
func Sign(body []byte, secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// ValidateURL accepts only absolute http(s) URLs with a host.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the webhook URL must be an absolute http:// or https:// address")
	}
	return nil
}

// Result is the outcome of a delivery, as shown next to the webhook.
type Result struct {
	OK     bool
	Status string // "ok (200)", "failed: HTTP 500", "failed: connection refused"
	At     time.Time
}

type job struct {
	t Target
	p Payload
}

// Dispatcher is the bounded retry queue. The zero value is usable after
// setting nothing; fields tune it for tests.
type Dispatcher struct {
	// Client defaults to a 5 s-timeout client with the system's trusted roots.
	Client *http.Client
	// Backoff[i] is the wait before attempt i+2. Defaults to 1 s, 4 s.
	Backoff []time.Duration
	// QueueSize bounds pending deliveries; 0 = DefaultQueue.
	QueueSize int
	// Workers is the number of concurrent senders; 0 = 4.
	Workers int
	// OnResult is called once per finished delivery (after the last attempt).
	OnResult func(t Target, r Result)

	mu      sync.Mutex
	queue   []job
	wake    chan struct{}
	dropped int
	once    sync.Once
}

func (d *Dispatcher) init() {
	d.once.Do(func() {
		d.wake = make(chan struct{}, 1)
		if d.Client == nil {
			d.Client = &http.Client{
				Timeout: Timeout,
				// A redirect would re-send the signed body somewhere the admin
				// never configured.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}
		}
		if d.Backoff == nil {
			d.Backoff = []time.Duration{time.Second, 4 * time.Second}
		}
	})
}

// Enqueue schedules a delivery without blocking. When the queue is full the
// oldest pending delivery is dropped.
func (d *Dispatcher) Enqueue(t Target, p Payload) {
	d.init()
	max := d.QueueSize
	if max <= 0 {
		max = DefaultQueue
	}
	d.mu.Lock()
	if len(d.queue) >= max {
		old := d.queue[0]
		d.queue = append(d.queue[:0], d.queue[1:]...)
		d.dropped++
		slog.Warn("webhook queue full, dropped the oldest delivery", "webhook", old.t.ID, "event", old.p.Event)
	}
	d.queue = append(d.queue, job{t, p})
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Dropped counts deliveries discarded because the queue was full.
func (d *Dispatcher) Dropped() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped
}

// Pending is the number of queued deliveries.
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.queue)
}

func (d *Dispatcher) pop() (job, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return job{}, false
	}
	j := d.queue[0]
	d.queue = append(d.queue[:0], d.queue[1:]...)
	if len(d.queue) > 0 { // let an idle worker take the next one
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	return j, true
}

// Run sends queued deliveries until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	d.init()
	n := d.Workers
	if n <= 0 {
		n = 4
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, ok := d.pop()
				if !ok {
					select {
					case <-ctx.Done():
						return
					case <-d.wake:
					case <-time.After(time.Second): // another worker may have taken the wake-up
					}
					continue
				}
				res := d.deliver(ctx, j)
				if ctx.Err() != nil {
					return
				}
				if d.OnResult != nil {
					d.OnResult(j.t, res)
				}
			}
		}()
	}
	wg.Wait()
}

func (d *Dispatcher) deliver(ctx context.Context, j job) Result {
	var res Result
	for attempt := 0; attempt < Attempts; attempt++ {
		if attempt > 0 {
			wait := d.Backoff[len(d.Backoff)-1]
			if attempt-1 < len(d.Backoff) {
				wait = d.Backoff[attempt-1]
			}
			select {
			case <-ctx.Done():
				return res
			case <-time.After(wait):
			}
		}
		var final bool
		res, final = d.Once(ctx, j.t, j.p)
		if res.OK || final {
			return res
		}
	}
	return res
}

// Once makes a single attempt, synchronously (the Test button uses it).
// final reports an answer retrying can't improve.
func (d *Dispatcher) Once(ctx context.Context, t Target, p Payload) (res Result, final bool) {
	d.init()
	res.At = time.Now()
	body, _ := json.Marshal(p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		res.Status = "failed: " + err.Error()
		return res, true
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Sentinel-Webhook/1")
	if t.Secret != "" {
		req.Header.Set(SignatureHeader, Sign(body, t.Secret))
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		res.Status = "failed: " + shorten(err)
		return res, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.OK, res.Status = true, fmt.Sprintf("ok (%d)", resp.StatusCode)
		return res, true
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429:
		res.Status = fmt.Sprintf("failed: HTTP %d", resp.StatusCode)
		return res, true
	}
	res.Status = fmt.Sprintf("failed: HTTP %d", resp.StatusCode)
	return res, false
}

// shorten strips the URL out of net/http errors (it may carry a token).
func shorten(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
