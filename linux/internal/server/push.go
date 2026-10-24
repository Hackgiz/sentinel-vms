package server

// Push notifications to paired iPhones. An alarm is pushed when it is first
// raised (merges into an open alarm never push — that is the 60 s burst
// coalescing the alarm grouping already does), at least one enabled rule
// with push delivery matches it, it isn't quiet hours (Critical alarms still
// go through), and the same camera hasn't pushed the same kind in the last
// minute.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/push"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

const (
	pushModeSetting      = "push_mode" // off | relay | direct
	pushRelayURLSetting  = "push_relay_url"
	pushRelayIDSetting   = "push_relay_install"
	pushRelayKeySetting  = "push_relay_secret" // sealed
	pushAPNsKeySetting   = "push_apns_key"     // sealed .p8 PEM
	pushAPNsKeyIDSetting = "push_apns_key_id"
	pushAPNsTeamSetting  = "push_apns_team_id"
	pushAPNsTopicSetting = "push_apns_topic"
	pushQuietSetting     = "push_quiet" // "" = off, else "fromMinute-toMinute"

	pushCoalesce = 60 * time.Second
)

type pushState struct {
	mu        sync.Mutex
	queue     *push.Queue
	senderKey string
	sender    push.Sender
	lastPush  map[string]time.Time // camera|kind → last push (coalescing)
	lastError string
	hooked    bool        // subscribed to new alarms
	override  push.Sender // tests
}

// RunRules starts the alarm-rules side jobs: the push queue and the
// event-bus subscription that feeds it new alarms. (The low-disk monitor
// runs on the recording server; see storage_alarm.go.) Blocks
// until ctx ends.
func (s *Server) RunRules(ctx context.Context) {
	go s.runWebhooks(ctx) // outbound webhooks (v2_webhooks.go)
	// Alarm Live Activity pushes (liveactivity.go, WAVE10(B)).
	go s.runLiveActivities(ctx)
	go s.runUploadSweep(ctx) // unfinished video uploads (v2_uploads.go, WAVE23)
	q := s.pushQueue()
	s.push.mu.Lock()
	subscribe := !s.push.hooked
	s.push.hooked = true
	s.push.mu.Unlock()
	if subscribe {
		sub := s.Bus.Subscribe("push", 0)
		bus.Handle(sub, TopicAlarmNew, s.pushAlarm)
		defer sub.Close()
	}
	q.Run(ctx)
}

func (s *Server) pushQueue() *push.Queue {
	s.push.mu.Lock()
	defer s.push.mu.Unlock()
	if s.push.queue == nil {
		s.push.queue = &push.Queue{
			Sender:         s.pushSender,
			OnInvalidToken: s.dropPushToken,
			OnResult: func(n push.Notification, err error) {
				if err != nil {
					slog.Warn("push failed", "err", err)
					s.setPushError(err.Error())
				}
			},
		}
	}
	return s.push.queue
}

func (s *Server) setPushError(msg string) {
	s.push.mu.Lock()
	s.push.lastError = msg
	s.push.mu.Unlock()
}

func (s *Server) dropPushToken(token string) {
	n, err := s.Store.ClearAPNSToken(s.Ctx, token)
	if err == nil && n > 0 {
		_ = s.Store.Audit(s.Ctx, "System", "Notifications", "Removed expired push token",
			"Apple reported a paired iPhone's push token as no longer valid; it re-registers the next time the app opens")
	}
}

func (s *Server) pushMode() string {
	return s.Store.Setting(s.ctx(), pushModeSetting, "off")
}

func (s *Server) ctx() context.Context {
	if s.Ctx != nil {
		return s.Ctx
	}
	return context.Background()
}

func (s *Server) relayURL() string {
	if u := s.Store.Setting(s.ctx(), pushRelayURLSetting, ""); u != "" {
		return u
	}
	if u := os.Getenv("SENTINEL_PUSH_RELAY_URL"); u != "" {
		return u
	}
	return push.DefaultRelayURL
}

// pushSender builds (and caches) the sender for the current settings; nil
// when push is off or not set up.
func (s *Server) pushSender() push.Sender {
	s.push.mu.Lock()
	o := s.push.override
	s.push.mu.Unlock()
	if o != nil {
		return o
	}
	ctx := s.ctx()
	mode := s.pushMode()
	var parts []string
	switch mode {
	case "relay":
		parts = []string{mode, s.relayURL(), s.Store.Setting(ctx, pushRelayIDSetting, ""), s.Store.Setting(ctx, pushRelayKeySetting, "")}
	case "direct":
		parts = []string{mode, s.Store.Setting(ctx, pushAPNsKeySetting, ""), s.Store.Setting(ctx, pushAPNsKeyIDSetting, ""),
			s.Store.Setting(ctx, pushAPNsTeamSetting, ""), s.Store.Setting(ctx, pushAPNsTopicSetting, "")}
	case "cloud": // WAVE12(C): Sentinel Cloud's push relay (v2_cloud.go)
		parts = s.cloudPushParts(ctx)
	default:
		return nil
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	key := hex.EncodeToString(sum[:])
	s.push.mu.Lock()
	if s.push.senderKey == key {
		defer s.push.mu.Unlock()
		return s.push.sender
	}
	s.push.mu.Unlock()

	var sender push.Sender
	switch mode {
	case "relay":
		secret, err := s.Store.Unseal(parts[3])
		if parts[2] == "" || err != nil || secret == "" {
			return nil
		}
		sender = &push.Relay{BaseURL: parts[1], InstallID: parts[2], Secret: secret,
			Tokens: func(ctx context.Context) []string {
				toks, _ := s.Store.PushTokens(ctx)
				out := make([]string, 0, len(toks))
				for _, t := range toks {
					out = append(out, t)
				}
				return out
			},
			OnUnauthorized: func() {
				// Forget the dead registration so choosing the relay again re-registers.
				_ = s.Store.SetSetting(s.ctx(), pushRelayIDSetting, "")
				_ = s.Store.SetSetting(s.ctx(), pushRelayKeySetting, "")
				s.setPushError("The push relay no longer recognizes this server. Choose the Sentinel push relay again to re-register.")
			}}
	case "direct":
		pemText, err := s.Store.Unseal(parts[1])
		if err != nil || pemText == "" {
			return nil
		}
		k, err := push.ParseP8(pemText)
		if err != nil {
			return nil
		}
		sender = &push.APNs{Key: k, KeyID: parts[2], TeamID: parts[3], Topic: parts[4]}
	case "cloud": // WAVE12(C)
		if sender = s.cloudPushSender(parts); sender == nil {
			return nil
		}
	}
	s.push.mu.Lock()
	s.push.senderKey, s.push.sender = key, sender
	s.push.mu.Unlock()
	return sender
}

func quietWindow(v string) (from, to int, ok bool) {
	a, b, found := strings.Cut(v, "-")
	if !found {
		return 0, 0, false
	}
	f, err1 := strconv.Atoi(a)
	t, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || f == t || f < 0 || t < 0 || f >= 1440 || t >= 1440 {
		return 0, 0, false
	}
	return f, t, true
}

// pushDecision says whether a new alarm should be pushed, and why not.
func (s *Server) pushDecision(ctx context.Context, a *store.Alarm, now time.Time) (bool, string) {
	if s.pushSender() == nil {
		return false, "push is off or not set up"
	}
	if len(rules.PushRules(s.alarmRules(ctx), ruleAlarm(a), s.rulesLoc())) == 0 {
		return false, "no push rule matches"
	}
	if from, to, ok := quietWindow(s.Store.Setting(ctx, pushQuietSetting, "")); ok && a.Severity != "Critical" {
		local := now.In(s.rulesLoc())
		if rules.InWindow(local.Hour()*60+local.Minute(), from, to) {
			return false, "quiet hours"
		}
	}
	key := a.CameraID + "|" + a.Kind
	s.push.mu.Lock()
	defer s.push.mu.Unlock()
	if s.push.lastPush == nil {
		s.push.lastPush = map[string]time.Time{}
	}
	if last, ok := s.push.lastPush[key]; ok && now.Sub(last) < pushCoalesce {
		return false, "coalesced"
	}
	s.push.lastPush[key] = now
	for k, t := range s.push.lastPush { // keep the map small
		if now.Sub(t) > time.Hour {
			delete(s.push.lastPush, k)
		}
	}
	return true, ""
}

// pushAlarm runs for every newly raised alarm.
func (s *Server) pushAlarm(a store.Alarm) {
	ctx := s.ctx()
	ok, _ := s.pushDecision(ctx, &a, time.Now())
	if !ok {
		return
	}
	tokens, err := s.pushTokensFor(ctx, &a)
	if err != nil || len(tokens) == 0 {
		return
	}
	q := s.pushQueue()
	for _, t := range tokens {
		n := alarmNotification(&a)
		n.Token = t
		q.Enqueue(n)
	}
}

// alarmNotification shapes an alarm like the Mac's AI-alert push: the camera
// name is the title. Detection alarms carry cameraID so a tap opens that
// camera's live view; health alarms leave it out so the app opens Alarms.
func alarmNotification(a *store.Alarm) push.Notification {
	n := push.Notification{Title: a.CameraName, Body: a.Detail, AlarmID: a.ID, Severity: a.Severity, ThreadID: a.CameraID}
	if n.Title == "" {
		n.Title = "Sentinel VMS"
	}
	if n.Body == "" {
		n.Body = a.Title
	}
	if a.CameraID != "" && a.Kind != "Camera Offline" && a.Kind != "Storage" {
		n.CameraID = a.CameraID
	}
	if n.ThreadID == "" {
		n.ThreadID = "sentinel-system"
	}
	return n
}

// MARK: - Dashboard API

type pushStatus struct {
	Mode            string `json:"mode"`
	RelayURL        string `json:"relayURL"`
	RelayRegistered bool   `json:"relayRegistered"`
	APNsKeyID       string `json:"apnsKeyID"`
	APNsTeamID      string `json:"apnsTeamID"`
	APNsTopic       string `json:"apnsTopic"`
	APNsHasKey      bool   `json:"apnsHasKey"`
	Ready           bool   `json:"ready"`
	QuietEnabled    bool   `json:"quietEnabled"`
	QuietFrom       int    `json:"quietFrom"`
	QuietTo         int    `json:"quietTo"`
	Devices         int    `json:"devices"`
	PushDevices     int    `json:"pushDevices"`
	Sent            int    `json:"sent"`
	Failed          int    `json:"failed"`
	Dropped         int    `json:"dropped"`
	LastSentAt      int64  `json:"lastSentAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	CloudReady      bool   `json:"cloudReady"` // WAVE12(C): mode "cloud" can be chosen (linked, plan includes push)
}

func (s *Server) pushStatus(ctx context.Context) pushStatus {
	st := pushStatus{
		Mode:            s.pushMode(),
		RelayURL:        s.relayURL(),
		RelayRegistered: s.Store.Setting(ctx, pushRelayIDSetting, "") != "",
		APNsKeyID:       s.Store.Setting(ctx, pushAPNsKeyIDSetting, ""),
		APNsTeamID:      s.Store.Setting(ctx, pushAPNsTeamSetting, ""),
		APNsTopic:       s.Store.Setting(ctx, pushAPNsTopicSetting, push.DefaultTopic),
		APNsHasKey:      s.Store.Setting(ctx, pushAPNsKeySetting, "") != "",
		QuietFrom:       22 * 60,
		QuietTo:         7 * 60,
	}
	if f, t, ok := quietWindow(s.Store.Setting(ctx, pushQuietSetting, "")); ok {
		st.QuietEnabled, st.QuietFrom, st.QuietTo = true, f, t
	}
	st.Ready = s.pushSender() != nil
	st.CloudReady = s.cloudPushReady(ctx) // WAVE12(C)
	if devs, err := s.Store.PairedDevices(ctx); err == nil {
		st.Devices = len(devs)
		for _, d := range devs {
			if d.HasPush {
				st.PushDevices++
			}
		}
	}
	q := s.pushQueue().Stats()
	st.Sent, st.Failed, st.Dropped = q.Sent, q.Failed, q.Dropped
	if !q.LastSentAt.IsZero() {
		st.LastSentAt = q.LastSentAt.Unix()
	}
	s.push.mu.Lock()
	st.LastError = s.push.lastError
	s.push.mu.Unlock()
	return st
}

func (s *Server) handleGetPush(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.pushStatus(r.Context()))
}

func (s *Server) handlePutPush(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode         *string `json:"mode"`
		RelayURL     *string `json:"relayURL"`
		APNsKey      *string `json:"apnsKey"`
		APNsKeyID    *string `json:"apnsKeyID"`
		APNsTeamID   *string `json:"apnsTeamID"`
		APNsTopic    *string `json:"apnsTopic"`
		QuietEnabled *bool   `json:"quietEnabled"`
		QuietFrom    *int    `json:"quietFrom"`
		QuietTo      *int    `json:"quietTo"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	var changes []string
	set := func(key, value, label string) bool {
		if err := s.Store.SetSetting(ctx, key, value); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if label != "" {
			changes = append(changes, label)
		}
		return true
	}

	if in.RelayURL != nil {
		u := strings.TrimRight(strings.TrimSpace(*in.RelayURL), "/")
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://localhost") && !strings.HasPrefix(u, "http://127.0.0.1") {
			writeError(w, http.StatusBadRequest, "the relay address must start with https://")
			return
		}
		if u != s.Store.Setting(ctx, pushRelayURLSetting, "") {
			// A different relay means a different install registration.
			if !set(pushRelayURLSetting, u, "relay address") || !set(pushRelayIDSetting, "", "") || !set(pushRelayKeySetting, "", "") {
				return
			}
		}
	}
	if in.APNsKey != nil && strings.TrimSpace(*in.APNsKey) != "" {
		if _, err := push.ParseP8(strings.TrimSpace(*in.APNsKey)); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sealed, err := s.Store.Seal(strings.TrimSpace(*in.APNsKey))
		if err != nil || !set(pushAPNsKeySetting, sealed, "APNs key") {
			return
		}
	}
	for _, f := range []struct {
		v     *string
		key   string
		label string
		n     int
	}{{in.APNsKeyID, pushAPNsKeyIDSetting, "key ID", 10}, {in.APNsTeamID, pushAPNsTeamSetting, "team ID", 10}, {in.APNsTopic, pushAPNsTopicSetting, "bundle ID", 0}} {
		if f.v == nil {
			continue
		}
		v := strings.TrimSpace(*f.v)
		if v != "" && f.n > 0 && (len(v) != f.n || strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "") {
			writeError(w, http.StatusBadRequest, "the Apple "+f.label+" is 10 letters and digits")
			return
		}
		if v != s.Store.Setting(ctx, f.key, "") && !set(f.key, v, f.label) {
			return
		}
	}
	if in.QuietEnabled != nil || in.QuietFrom != nil || in.QuietTo != nil {
		cur := s.pushStatus(ctx)
		on, from, to := cur.QuietEnabled, cur.QuietFrom, cur.QuietTo
		if in.QuietEnabled != nil {
			on = *in.QuietEnabled
		}
		if in.QuietFrom != nil {
			from = *in.QuietFrom
		}
		if in.QuietTo != nil {
			to = *in.QuietTo
		}
		v := ""
		if on {
			v = strconv.Itoa(from) + "-" + strconv.Itoa(to)
			if _, _, ok := quietWindow(v); !ok {
				writeError(w, http.StatusBadRequest, "quiet hours need different start and end times")
				return
			}
		}
		if v != s.Store.Setting(ctx, pushQuietSetting, "") && !set(pushQuietSetting, v, "quiet hours") {
			return
		}
	}
	if in.Mode != nil {
		mode := *in.Mode
		switch mode {
		case "off":
		case "relay":
			if s.Store.Setting(ctx, pushRelayIDSetting, "") == "" {
				rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				id, secret, err := push.Register(rctx, s.relayURL(), s.serverName(), s.Version, nil)
				cancel()
				if err != nil {
					writeError(w, http.StatusBadGateway, "Couldn't register with the push relay: "+err.Error())
					return
				}
				sealed, err := s.Store.Seal(secret)
				if err != nil || !set(pushRelayIDSetting, id, "") || !set(pushRelayKeySetting, sealed, "") {
					return
				}
				changes = append(changes, "registered with relay")
			}
		case "cloud": // WAVE12(C): needs a Sentinel Cloud link whose plan includes push (v2_cloud.go)
			if !s.cloudPushReady(ctx) {
				writeError(w, http.StatusBadRequest, "push through Sentinel Cloud needs this server linked to a plan that includes push")
				return
			}
		case "direct":
			if s.Store.Setting(ctx, pushAPNsKeySetting, "") == "" || s.Store.Setting(ctx, pushAPNsKeyIDSetting, "") == "" || s.Store.Setting(ctx, pushAPNsTeamSetting, "") == "" {
				writeError(w, http.StatusBadRequest, "direct APNs needs your .p8 key, its key ID and your team ID")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "mode must be off, relay or direct")
			return
		}
		if mode != s.pushMode() && !set(pushModeSetting, mode, "mode "+mode) {
			return
		}
	}
	s.setPushError("")
	if len(changes) > 0 {
		s.audit(r, "Notifications", "Changed push settings", strings.Join(changes, ", "))
	}
	writeJSON(w, http.StatusOK, s.pushStatus(ctx))
}

// handleTestPush sends a test alert to every phone right away (not queued),
// so the result can be shown.
func (s *Server) handleTestPush(w http.ResponseWriter, r *http.Request) {
	sender := s.pushSender()
	if sender == nil {
		writeError(w, http.StatusConflict, "Push isn't set up yet.")
		return
	}
	tokens, err := s.Store.PushTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(tokens) == 0 {
		writeError(w, http.StatusConflict, "No paired iPhone has allowed notifications yet. Open Sentinel Mobile and allow notifications.")
		return
	}
	sent, failed := 0, []string{}
	for _, t := range tokens {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		err := sender.Send(ctx, push.Notification{Token: t, Title: "Sentinel VMS", Body: "Test alert — push notifications are working.", ThreadID: "sentinel-system", Severity: "Warning"})
		cancel()
		switch {
		case err == nil:
			sent++
		case errors.Is(err, push.ErrInvalidToken):
			s.dropPushToken(t)
			failed = append(failed, "a device token was no longer valid and was removed")
		default:
			failed = append(failed, err.Error())
		}
	}
	s.audit(r, "Notifications", "Sent test push", strconv.Itoa(sent)+" delivered via "+sender.Name())
	if sent == 0 {
		s.setPushError(strings.Join(failed, "; "))
		writeError(w, http.StatusBadGateway, "Test push failed: "+strings.Join(failed, "; "))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent, "failed": failed})
}
