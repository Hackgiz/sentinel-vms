// Package push delivers alarm notifications to paired iPhones (Sentinel
// Mobile) through Apple Push Notification service.
//
// Two senders implement the same interface:
//
//   - Relay: the default for self-hosted servers, which can't hold the
//     Sentinel developer's APNs key. The server registers itself once with
//     the Sentinel push relay (an install id + secret), tells the relay which
//     device tokens belong to it, and asks it to send structured alerts. The
//     relay builds the APNs payload itself and only delivers to tokens that
//     install registered, under per-install and per-device rate limits.
//   - APNs: direct delivery with the user's own .p8 key (for people who build
//     the open-source iOS app under their own team and bundle id). Same
//     behavior as the Mac's APNsPushService.
//
// A Queue in front of either sender keeps alarm handling non-blocking: it is
// bounded (drops when full), retries transient failures with backoff, and
// reports tokens APNs says are dead so the server can forget them.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Notification is one alert to one device.
type Notification struct {
	Token    string `json:"token"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	CameraID string `json:"cameraID,omitempty"` // tap deep-links to this camera's live view
	AlarmID  string `json:"alarmID,omitempty"`
	ThreadID string `json:"threadID,omitempty"` // groups notifications on the phone
	Severity string `json:"severity,omitempty"` // Critical | Warning | Info
}

// Sender delivers one notification. Errors wrap ErrInvalidToken (the device
// token is dead — forget it), ErrRetry (try again later) or are permanent.
type Sender interface {
	Send(ctx context.Context, n Notification) error
	Name() string
}

var (
	ErrInvalidToken = errors.New("device token is no longer valid")
	ErrRetry        = errors.New("temporary push failure")
)

// RetryError carries a server-requested delay (Retry-After).
type RetryError struct {
	Msg   string
	After int // seconds; 0 = use the queue's backoff
}

func (e *RetryError) Error() string { return e.Msg }
func (e *RetryError) Unwrap() error { return ErrRetry }

// Limits APNs and the relay both enforce.
const (
	maxTitle = 120
	maxBody  = 400
)

// Clip shortens s to n bytes on a rune boundary, adding an ellipsis.
func Clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// Payload builds the APNs JSON body. The shape matches the Mac's
// APNsPushService so the shipping iOS app handles it: alert title/body,
// default sound, time-sensitive interruption, and cameraID as a top-level
// custom key (the app opens that camera's live view on tap, otherwise the
// Alarms tab). alarmID is additive; the app ignores keys it doesn't know.
func Payload(n Notification) ([]byte, error) {
	if n.Title == "" && n.Body == "" {
		return nil, errors.New("empty notification")
	}
	level := "time-sensitive"
	if n.Severity == "Info" {
		level = "active"
	}
	aps := map[string]any{
		"alert":              map[string]string{"title": Clip(n.Title, maxTitle), "body": Clip(n.Body, maxBody)},
		"sound":              "default",
		"interruption-level": level,
	}
	if n.ThreadID != "" {
		aps["thread-id"] = n.ThreadID
	}
	body := map[string]any{"aps": aps}
	if n.CameraID != "" {
		body["cameraID"] = n.CameraID
	}
	if n.AlarmID != "" {
		body["alarmID"] = n.AlarmID
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(b) > 4096 {
		return nil, fmt.Errorf("payload too large (%d bytes)", len(b))
	}
	return b, nil
}

// validToken: APNs device tokens are hex (64 chars today; Apple says they may grow).
func validToken(t string) bool {
	if len(t) < 32 || len(t) > 200 {
		return false
	}
	for _, c := range t {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
