package push

// Live Activity pushes (WAVE10). When a Sentinel Mobile user follows an
// alarm, the phone runs an ActivityKit Live Activity and hands the server
// its push token. Alarm state changes then go to Apple with
// `apns-push-type: liveactivity` on the topic
// `<bundle>.push-type.liveactivity`; the body's aps carries `event`
// (update | end), a `timestamp` and the `content-state` the activity
// decodes (iOS: Sentinel Shared/AlarmActivityAttributes.swift — the JSON
// keys below are that contract).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	ActivityUpdate = "update"
	ActivityEnd    = "end"

	activityTopicSuffix = ".push-type.liveactivity"
)

// ActivityContent is the Live Activity's dynamic state. Keys must match
// AlarmActivityContent on the phone (ActivityKit decodes it strictly).
type ActivityContent struct {
	State      string `json:"state"`
	Owner      string `json:"owner"`
	Severity   string `json:"severity"`
	UpdatedAt  int64  `json:"updatedAt"` // Unix ms
	EventCount int    `json:"eventCount"`
}

// Activity is one Live Activity push to one phone.
type Activity struct {
	Token string `json:"token"` // the activity's push token (hex)
	// DeviceToken is the phone's regular APNs token. The relay only pushes
	// for devices the install registered, so it authorizes by this one.
	DeviceToken  string          `json:"deviceToken,omitempty"`
	AlarmID      string          `json:"alarmID,omitempty"`
	Event        string          `json:"event"` // update | end
	ContentState ActivityContent `json:"contentState"`
	Timestamp    int64           `json:"timestamp"`               // Unix seconds; Apple drops older updates
	Dismissal    int64           `json:"dismissalDate,omitempty"` // end only: Unix seconds the activity leaves the Lock Screen
	Critical     bool            `json:"critical,omitempty"`      // high priority
}

// ActivitySender delivers Live Activity pushes. APNs and Relay implement it.
type ActivitySender interface {
	SendActivity(ctx context.Context, a Activity) error
}

// ActivityTopic is the apns-topic for Live Activity pushes of an app.
func ActivityTopic(bundle string) string {
	if bundle == "" {
		bundle = DefaultTopic
	}
	return bundle + activityTopicSuffix
}

// ActivityPayload builds the APNs body for a Live Activity push.
func ActivityPayload(a Activity) ([]byte, error) {
	if a.Event != ActivityUpdate && a.Event != ActivityEnd {
		return nil, fmt.Errorf("live activity event must be update or end, not %q", a.Event)
	}
	ts := a.Timestamp
	if ts == 0 {
		ts = time.Now().Unix()
	}
	cs := a.ContentState
	cs.Owner = Clip(cs.Owner, maxTitle)
	aps := map[string]any{
		"timestamp":     ts,
		"event":         a.Event,
		"content-state": cs,
	}
	if a.Event == ActivityEnd && a.Dismissal > 0 {
		aps["dismissal-date"] = a.Dismissal
	}
	b, err := json.Marshal(map[string]any{"aps": aps})
	if err != nil {
		return nil, err
	}
	if len(b) > 4096 {
		return nil, fmt.Errorf("live activity payload too large (%d bytes)", len(b))
	}
	return b, nil
}

func activityPriority(a Activity) string {
	// Apple budgets priority-10 Live Activity updates; spend them on the
	// changes a guard must see now (Critical alarms, the end) and send the
	// rest at 5.
	if a.Critical || a.Event == ActivityEnd {
		return "10"
	}
	return "5"
}

// SendActivity sends a Live Activity push directly to Apple.
func (a *APNs) SendActivity(ctx context.Context, act Activity) error {
	if !validToken(act.Token) {
		return ErrInvalidToken
	}
	payload, err := ActivityPayload(act)
	if err != nil {
		return err
	}
	return a.post(ctx, apnsRequest{token: act.Token, topic: ActivityTopic(a.topic()), pushType: "liveactivity",
		priority: activityPriority(act), payload: payload})
}

// SendActivity asks the relay to deliver a Live Activity push:
//
//	POST /push/v1/activity  Activity → 200 {"ok":true}
//
// with the same auth and error answers as /push/v1/send. The relay checks
// deviceToken against the install's registered tokens
// (sentvms-dl-worker/src/push-relay.js). A relay without the route answers
// 404, reported below.
func (r *Relay) SendActivity(ctx context.Context, act Activity) error {
	if !validToken(act.Token) {
		return ErrInvalidToken
	}
	if act.DeviceToken != "" && !validToken(act.DeviceToken) {
		return errors.New("invalid device token")
	}
	if act.Event != ActivityUpdate && act.Event != ActivityEnd {
		return fmt.Errorf("live activity event must be update or end, not %q", act.Event)
	}
	if act.Timestamp == 0 {
		act.Timestamp = time.Now().Unix()
	}
	if r.Tokens != nil {
		if err := r.SyncTokens(ctx, r.Tokens(ctx)); err != nil {
			return &RetryError{Msg: "push relay: " + err.Error()}
		}
	}
	status, hdr, err := r.do(ctx, http.MethodPost, "/push/v1/activity", act, nil, true)
	if status == http.StatusNotFound {
		return errors.New("the push relay doesn't support Live Activity updates yet")
	}
	return r.sendResult(status, hdr, err)
}
