package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel-linux/internal/push"
	"sentinel-linux/internal/store"
)

const (
	activityTok  = "cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123cafe0123"
	activityTok2 = "beef4567beef4567beef4567beef4567beef4567beef4567beef4567beef4567"
)

func raiseTestAlarm(t *testing.T, s *Server, c *store.Camera, sev string) *store.Alarm {
	t.Helper()
	now := time.Now()
	a, _, err := s.Store.RaiseAlarm(context.Background(), store.AlarmInput{CameraID: c.ID, CameraName: c.Name, Kind: "Person",
		Title: "Person detected", Severity: sev, Detail: "At the door.", At: now, MergeSince: now.Add(time.Second), Origin: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestV2ActivityTokenRoutes(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	c := addCam(t, s)
	a := raiseTestAlarm(t, s, c, "Warning")
	tok := v2Token(t, s, "viewer1", "Viewer")
	apns := strings.Repeat("ab12", 16)
	body := func(alarm, act string) string {
		return `{"apnsToken":"` + apns + `","alarmId":"` + alarm + `","activityToken":"` + act + `"}`
	}

	if code, _ := v2Call(t, h, "PUT", "/api/v2/push/activity-token", "", body(a.ID, activityTok)); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	// No push registration for this phone yet.
	if code, m := v2Call(t, h, "PUT", "/api/v2/push/activity-token", tok, body(a.ID, activityTok)); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("unregistered phone: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/push/registration", tok, `{"apnsToken":"`+apns+`"}`); code != 204 {
		t.Fatalf("register: %d", code)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/push/activity-token", tok, body(a.ID, "nothex!")); code != 400 || errCode(m) != "bad_request" {
		t.Fatalf("bad token: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", "/api/v2/push/activity-token", tok, body("no-such-alarm", activityTok)); code != 404 {
		t.Fatalf("unknown alarm: %d", code)
	}
	if code, m := v2Call(t, h, "PUT", "/api/v2/push/activity-token", tok, body(a.ID, activityTok)); code != 204 {
		t.Fatalf("follow: %d %v", code, m)
	}
	f, err := s.Store.ActivityFollowers(ctx, a.ID)
	if err != nil || len(f) != 1 || f[0].Token != activityTok || f[0].Device.APNSToken() != apns {
		t.Fatalf("followers %+v %v", f, err)
	}
	// Someone else can't stop this phone's follow.
	other := v2Token(t, s, "viewer2", "Viewer")
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/push/activity-token?apnsToken="+apns, other, ""); code != 204 {
		t.Fatalf("foreign delete: %d", code)
	}
	if f, _ := s.Store.ActivityFollowers(ctx, a.ID); len(f) != 1 {
		t.Fatal("another user cleared the follow")
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/push/activity-token?apnsToken="+apns, tok, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if f, _ := s.Store.ActivityFollowers(ctx, a.ID); len(f) != 0 {
		t.Fatalf("still following: %+v", f)
	}
	if code, _ := v2Call(t, h, "DELETE", "/api/v2/push/activity-token?apnsToken=zz", tok, ""); code != 400 {
		t.Fatalf("bad delete: %d", code)
	}
}

func TestCompanionActivityToken(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	ctx := context.Background()
	v2SignIn(t, s, "owner", "Admin")
	c := addCam(t, s)
	a := raiseTestAlarm(t, s, c, "Warning")
	dev, phoneToken, err := s.Store.CreatePairedDevice(ctx, "iPhone", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, "POST", "/devices/activity-token", "", `{"alarmId":"`+a.ID+`","activityToken":"`+activityTok+`"}`); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/devices/activity-token", phoneToken, `{"alarmId":"`+a.ID+`","activityToken":"xyz"}`); rec.Code != 400 {
		t.Fatalf("bad token: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/devices/activity-token", phoneToken, `{"alarmId":"nope","activityToken":"`+activityTok+`"}`); rec.Code != 404 {
		t.Fatalf("unknown alarm: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/devices/activity-token", phoneToken, `{"alarmId":"`+a.ID+`","activityToken":"`+activityTok+`"}`); rec.Code != 200 {
		t.Fatalf("follow: %d %s", rec.Code, rec.Body)
	}
	if f, _ := s.Store.ActivityFollowers(ctx, a.ID); len(f) != 1 || f[0].Device.ID != dev.ID {
		t.Fatalf("followers %+v", f)
	}
	// Also served on the tunnel-only companion handler.
	if rec := do(t, s.CompanionHandler(), "POST", "/devices/activity-token", phoneToken, `{"activityToken":""}`); rec.Code != 200 {
		t.Fatalf("unfollow via tunnel: %d", rec.Code)
	}
	if f, _ := s.Store.ActivityFollowers(ctx, a.ID); len(f) != 0 {
		t.Fatalf("still following %+v", f)
	}
}

// fakeAPNs records Live Activity pushes the way Apple would receive them.
type fakeAPNs struct {
	mu     sync.Mutex
	pushes []fakeActivityPush
	gone   map[string]bool // tokens answered 410
}

type fakeActivityPush struct {
	token, pushType, topic, priority string
	aps                              struct {
		Event         string               `json:"event"`
		Timestamp     int64                `json:"timestamp"`
		DismissalDate int64                `json:"dismissal-date"`
		ContentState  push.ActivityContent `json:"content-state"`
	}
}

func (f *fakeAPNs) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tok := strings.TrimPrefix(r.URL.Path, "/3/device/")
		if !strings.HasPrefix(r.Header.Get("authorization"), "bearer ") {
			t.Errorf("no provider token")
		}
		if f.gone[tok] {
			w.WriteHeader(410)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
			return
		}
		p := fakeActivityPush{token: tok, pushType: r.Header.Get("apns-push-type"), topic: r.Header.Get("apns-topic"), priority: r.Header.Get("apns-priority")}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			APS json.RawMessage `json:"aps"`
		}
		if err := json.Unmarshal(b, &body); err != nil || json.Unmarshal(body.APS, &p.aps) != nil {
			t.Errorf("body %s", b)
		}
		f.pushes = append(f.pushes, p)
		w.WriteHeader(200)
	})
}

func (f *fakeAPNs) waitFor(t *testing.T, n int) []fakeActivityPush {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		got := append([]fakeActivityPush(nil), f.pushes...)
		f.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			if len(got) != n {
				t.Fatalf("pushes = %d, want %d: %+v", len(got), n, got)
			}
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveActivityPushesFollowedAlarmStateChanges(t *testing.T) {
	s := newCompanionServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Ctx = ctx
	fake := &fakeAPNs{gone: map[string]bool{activityTok2: true}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s.push.override = &push.APNs{KeyID: "ABCDEFGHIJ", TeamID: "TEAM123456", Topic: "com.example.sentinel", Key: key,
		Production: srv.URL, Sandbox: srv.URL}
	go s.RunRules(ctx)
	time.Sleep(30 * time.Millisecond) // subscriptions registered

	h := s.Handler()
	c := addCam(t, s)
	a := raiseTestAlarm(t, s, c, "Critical")
	other := raiseTestAlarm(t, s, addCam(t, s), "Warning")
	viewer := v2Token(t, s, "viewer1", "Viewer")
	op := v2Token(t, s, "op1", "Operator")
	apns, apns2 := strings.Repeat("ab12", 16), strings.Repeat("cd34", 16)
	for _, reg := range []struct{ apns, act string }{{apns, activityTok}, {apns2, activityTok2}} {
		if code, _ := v2Call(t, h, "PUT", "/api/v2/push/registration", viewer, `{"apnsToken":"`+reg.apns+`"}`); code != 204 {
			t.Fatal("register")
		}
		if code, m := v2Call(t, h, "PUT", "/api/v2/push/activity-token", viewer,
			`{"apnsToken":"`+reg.apns+`","alarmId":"`+a.ID+`","activityToken":"`+reg.act+`"}`); code != 204 {
			t.Fatalf("follow: %d %v", code, m)
		}
	}

	// A change to an alarm nobody follows pushes nothing.
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/"+other.ID+"/state", op, `{"state":"Acknowledged"}`); code != 200 {
		t.Fatalf("ack other: %d %v", code, m)
	}
	// Acknowledge the followed alarm → one update (the second phone's token is dead).
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/"+a.ID+"/state", op, `{"state":"Acknowledged"}`); code != 200 {
		t.Fatalf("ack: %d %v", code, m)
	}
	got := fake.waitFor(t, 1)
	p := got[0]
	if p.token != activityTok || p.pushType != "liveactivity" || p.topic != "com.example.sentinel.push-type.liveactivity" || p.priority != "10" {
		t.Fatalf("push %+v", p)
	}
	if p.aps.Event != "update" || p.aps.ContentState.State != "Acknowledged" || p.aps.ContentState.Severity != "Critical" ||
		p.aps.ContentState.Owner == "" || p.aps.Timestamp == 0 || p.aps.ContentState.UpdatedAt < p.aps.Timestamp*1000-60_000 {
		t.Fatalf("aps %+v", p.aps)
	}
	// The dead activity token was forgotten; the live one is kept.
	waitFollowers := func(want int) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if f, _ := s.Store.ActivityFollowers(ctx, a.ID); len(f) == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		f, _ := s.Store.ActivityFollowers(ctx, a.ID)
		t.Fatalf("followers %+v, want %d", f, want)
	}
	waitFollowers(1)

	// Resolve → "end" with a dismissal date, then the follow is gone.
	if code, m := v2Call(t, h, "POST", "/api/v2/alarms/"+a.ID+"/state", op, `{"state":"Resolved"}`); code != 200 {
		t.Fatalf("resolve: %d %v", code, m)
	}
	got = fake.waitFor(t, 2)
	end := got[1]
	if end.aps.Event != "end" || end.aps.ContentState.State != "Resolved" || end.aps.DismissalDate <= end.aps.Timestamp {
		t.Fatalf("end push %+v", end.aps)
	}
	waitFollowers(0)
}

func TestLiveActivityContent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := &store.Alarm{ID: "a1", State: "Investigating", Owner: "alice", Severity: "Warning", UpdatedAt: 100, LastEventAt: 200, EventCount: 4}
	act := activityFor(a, now)
	if act.Event != push.ActivityUpdate || act.Critical || act.Dismissal != 0 || act.ContentState.UpdatedAt != 200_000 || act.ContentState.EventCount != 4 {
		t.Fatalf("open: %+v", act)
	}
	a.State = "False Alarm"
	act = activityFor(a, now)
	if act.Event != push.ActivityEnd || act.Dismissal != now.Add(activityLinger).Unix() {
		t.Fatalf("closed: %+v", act)
	}
}
