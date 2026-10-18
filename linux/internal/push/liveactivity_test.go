package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const activityTok = "cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123"

func TestActivityPayloadShape(t *testing.T) {
	b, err := ActivityPayload(Activity{Token: activityTok, Event: ActivityEnd, Timestamp: 1700000000, Dismissal: 1700000900,
		ContentState: ActivityContent{State: "Resolved", Owner: "alice", Severity: "Critical", UpdatedAt: 1700000000123, EventCount: 3}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		APS struct {
			Timestamp     int64          `json:"timestamp"`
			Event         string         `json:"event"`
			DismissalDate int64          `json:"dismissal-date"`
			ContentState  map[string]any `json:"content-state"`
		} `json:"aps"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.APS.Timestamp != 1700000000 || got.APS.Event != "end" || got.APS.DismissalDate != 1700000900 {
		t.Errorf("aps = %+v", got.APS)
	}
	// The phone's AlarmActivityContent decodes exactly these keys.
	for _, k := range []string{"state", "owner", "severity", "updatedAt", "eventCount"} {
		if _, ok := got.APS.ContentState[k]; !ok {
			t.Errorf("content-state lacks %q: %s", k, b)
		}
	}
	if len(got.APS.ContentState) != 5 {
		t.Errorf("content-state has extra keys: %s", b)
	}
	// An update carries no dismissal date; a bad event is refused.
	b, _ = ActivityPayload(Activity{Token: activityTok, Event: ActivityUpdate, Dismissal: 5})
	if strings.Contains(string(b), "dismissal-date") {
		t.Errorf("update with dismissal: %s", b)
	}
	if _, err := ActivityPayload(Activity{Event: "start"}); err == nil {
		t.Error("event start accepted")
	}
	if ActivityTopic("com.example.app") != "com.example.app.push-type.liveactivity" || ActivityTopic("") != DefaultTopic+".push-type.liveactivity" {
		t.Error("activity topic")
	}
}

// The fake APNs server checks the liveactivity headers and body.
func TestAPNsSendActivity(t *testing.T) {
	key, pemText := testKey(t)
	parsed, _ := ParseP8(pemText)
	var mu sync.Mutex
	var headers []http.Header
	var bodies []string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/3/device/"+activityTok {
			t.Errorf("path %s", r.URL.Path)
		}
		verifyJWT(t, strings.TrimPrefix(r.Header.Get("authorization"), "bearer "), &key.PublicKey)
		b, _ := io.ReadAll(r.Body)
		headers = append(headers, r.Header.Clone())
		bodies = append(bodies, string(b))
		w.WriteHeader(status)
		if status == 410 {
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
		}
	}))
	defer srv.Close()
	a := &APNs{KeyID: "ABCDEFGHIJ", TeamID: "TEAM123456", Topic: "com.example.sentinel", Key: parsed, Production: srv.URL, Sandbox: srv.URL}
	up := Activity{Token: activityTok, Event: ActivityUpdate, ContentState: ActivityContent{State: "Acknowledged", Owner: "bob", Severity: "Warning"}}
	if err := a.SendActivity(context.Background(), up); err != nil {
		t.Fatal(err)
	}
	end := Activity{Token: activityTok, Event: ActivityEnd, ContentState: ActivityContent{State: "Resolved", Severity: "Warning"}}
	if err := a.SendActivity(context.Background(), end); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(headers) != 2 {
		t.Fatalf("hits %d", len(headers))
	}
	for i, h := range headers {
		if h.Get("apns-push-type") != "liveactivity" || h.Get("apns-topic") != "com.example.sentinel.push-type.liveactivity" {
			t.Errorf("push %d headers %v", i, h)
		}
	}
	if headers[0].Get("apns-priority") != "5" || headers[1].Get("apns-priority") != "10" {
		t.Errorf("priorities %q %q", headers[0].Get("apns-priority"), headers[1].Get("apns-priority"))
	}
	if !strings.Contains(bodies[0], `"event":"update"`) || !strings.Contains(bodies[0], `"owner":"bob"`) ||
		!strings.Contains(bodies[1], `"event":"end"`) {
		t.Errorf("bodies %v", bodies)
	}
	status = 410
	mu.Unlock()
	if err := a.SendActivity(context.Background(), up); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("410 → %v", err)
	}
	if err := a.SendActivity(context.Background(), Activity{Token: "nothex", Event: ActivityUpdate}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("bad token → %v", err)
	}
}

func TestRelaySendActivity(t *testing.T) {
	var got Activity
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/push/v1/devices":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/push/v1/activity":
			if r.Header.Get("Authorization") != "Bearer inst1.s3cret" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r := &Relay{BaseURL: srv.URL, InstallID: "inst1", Secret: "s3cret", Tokens: func(context.Context) []string { return []string{tok} }}
	act := Activity{Token: activityTok, DeviceToken: tok, AlarmID: "a1", Event: ActivityUpdate,
		ContentState: ActivityContent{State: "Investigating", Severity: "Critical"}}
	if err := r.SendActivity(context.Background(), act); err != nil {
		t.Fatal(err)
	}
	if got.Token != activityTok || got.DeviceToken != tok || got.Event != "update" || got.ContentState.State != "Investigating" || got.Timestamp == 0 {
		t.Errorf("relay got %+v", got)
	}
	code = 410
	if err := r.SendActivity(context.Background(), act); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("410 → %v", err)
	}
	// A relay without the route says so instead of failing silently.
	old := &Relay{BaseURL: srv.URL + "/nope", InstallID: "inst1", Secret: "s3cret"}
	if err := old.SendActivity(context.Background(), act); err == nil || !strings.Contains(err.Error(), "Live Activity") {
		t.Errorf("404 → %v", err)
	}
}
