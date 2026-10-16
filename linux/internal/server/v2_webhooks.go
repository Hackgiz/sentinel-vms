package server

// Wave 6: outbound webhooks. CRUD + test under /api/v2/webhooks (ChangeSettings,
// audited area "Rules"), and the bus subscriber that fires them for rule
// actions and event-kind subscriptions. Delivery itself (signing, retries,
// the bounded queue) lives in internal/webhooks.
//
// The secret is write-only: PUT/POST accept it, GET only says whether one
// is set (hasSecret). On PUT an absent secret keeps the stored one; "" clears.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/webhooks"
)

// WebhookEvents are the event-kind subscriptions a webhook may list in its
// `events` (empty = fires only for rule actions).
var WebhookEvents = []string{"alarm.new", "alarm.state", "event.new", "recorder.online"}

type webhookState struct {
	mu     sync.Mutex
	loaded bool
	list   []store.Webhook
	disp   *webhooks.Dispatcher
	hooked bool
}

func (s *Server) registerV2Webhooks(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/webhooks", auth.ChangeSettings, s.handleV2Webhooks)
	s.v2Route(mux, "POST /api/v2/webhooks", auth.ChangeSettings, s.handleV2SaveWebhook)
	s.v2Route(mux, "PUT /api/v2/webhooks/{webhookId}", auth.ChangeSettings, s.handleV2SaveWebhook)
	s.v2Route(mux, "DELETE /api/v2/webhooks/{webhookId}", auth.ChangeSettings, s.handleV2DeleteWebhook)
	s.v2Route(mux, "POST /api/v2/webhooks/{webhookId}/test", auth.ChangeSettings, s.handleV2TestWebhook)
}

type v2Webhook struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Enabled     bool     `json:"enabled"`
	HasSecret   bool     `json:"hasSecret"`
	LastStatus  string   `json:"lastStatus"`
	LastFiredAt int64    `json:"lastFiredAt"` // ms, 0 = never
	CreatedAt   int64    `json:"createdAt"`
}

func toV2Webhook(w *store.Webhook) v2Webhook {
	ev := w.Events
	if ev == nil {
		ev = []string{}
	}
	return v2Webhook{ID: w.ID, Name: w.Name, URL: w.URL, Events: ev, Enabled: w.Enabled, HasSecret: w.Secret != "",
		LastStatus: w.LastStatus, LastFiredAt: secToMs(w.LastFiredAt), CreatedAt: secToMs(w.CreatedAt)}
}

func (s *Server) handleV2Webhooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Webhooks(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Webhook, 0, len(list))
	for i := range list {
		out = append(out, toV2Webhook(&list[i]))
	}
	v2Items(w, out)
}

func (s *Server) handleV2SaveWebhook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		URL     string   `json:"url"`
		Secret  *string  `json:"secret"`
		Events  []string `json:"events"`
		Enabled *bool    `json:"enabled"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	bad := func(msg string) { writeV2Error(w, http.StatusBadRequest, codeBadRequest, msg) }
	name, rawURL := strings.TrimSpace(in.Name), strings.TrimSpace(in.URL)
	if name == "" || len([]rune(name)) > 100 {
		bad("enter a webhook name of up to 100 characters")
		return
	}
	if err := webhooks.ValidateURL(rawURL); err != nil || len(rawURL) > 2000 {
		bad("the webhook URL must be an absolute http:// or https:// address")
		return
	}
	if in.Secret != nil && len(*in.Secret) > 256 {
		bad("the secret is too long (256 characters max)")
		return
	}
	events := []string{}
	for _, e := range in.Events {
		e = strings.TrimSpace(e)
		if !containsString(WebhookEvents, e) {
			bad("unknown event " + strconv.Quote(e) + "; choose from " + strings.Join(WebhookEvents, ", "))
			return
		}
		if !containsString(events, e) {
			events = append(events, e)
		}
	}
	wh := &store.Webhook{ID: r.PathValue("webhookId"), Name: name, URL: rawURL, Events: events, Enabled: in.Enabled == nil || *in.Enabled}
	creating := wh.ID == ""
	if !creating {
		old, err := s.Store.Webhook(r.Context(), wh.ID)
		if err != nil {
			writeV2Fail(w, err, "webhook")
			return
		}
		wh.Secret = old.Secret
		wh.LastStatus, wh.LastFiredAt = old.LastStatus, old.LastFiredAt
	}
	if in.Secret != nil {
		wh.Secret = *in.Secret
	}
	if err := s.Store.SaveWebhook(r.Context(), wh); err != nil {
		writeV2Fail(w, err, "webhook")
		return
	}
	s.invalidateWebhooks()
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
		s.audit(r, "Rules", "Added webhook", name)
	} else {
		s.audit(r, "Rules", "Edited webhook", name)
	}
	saved, err := s.Store.Webhook(r.Context(), wh.ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, status, toV2Webhook(saved))
}

func (s *Server) handleV2DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("webhookId")
	wh, err := s.Store.Webhook(r.Context(), id)
	if err != nil {
		writeV2Fail(w, err, "webhook")
		return
	}
	if used, err := s.Store.RulesUsingWebhook(r.Context(), id); err != nil {
		writeV2Err(w, err)
		return
	} else if len(used) > 0 {
		writeV2Error(w, http.StatusConflict, codeConflict, "this webhook is used by "+plural(len(used), "rule")+" ("+strings.Join(used, ", ")+"); remove it from them first")
		return
	}
	if err := s.Store.DeleteWebhook(r.Context(), id); err != nil {
		writeV2Fail(w, err, "webhook")
		return
	}
	s.invalidateWebhooks()
	s.audit(r, "Rules", "Deleted webhook", wh.Name)
	w.WriteHeader(http.StatusNoContent)
}

// handleV2TestWebhook sends one signed {"event":"webhook.test"} delivery,
// synchronously and without retries, and reports how it went. The endpoint
// failing is a 200 with ok:false: the request itself worked.
func (s *Server) handleV2TestWebhook(w http.ResponseWriter, r *http.Request) {
	wh, err := s.Store.Webhook(r.Context(), r.PathValue("webhookId"))
	if err != nil {
		writeV2Fail(w, err, "webhook")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*webhooks.Timeout)
	defer cancel()
	res, _ := s.webhookDispatcher().Once(ctx, webhooks.Target{ID: wh.ID, URL: wh.URL, Secret: wh.Secret},
		webhooks.Payload{Event: "webhook.test", Camera: "", Server: s.serverName(), TS: time.Now().Unix()})
	_ = s.Store.SetWebhookResult(r.Context(), wh.ID, res.Status, res.At.Unix())
	s.audit(r, "Rules", "Tested webhook", wh.Name+" — "+res.Status)
	writeJSON(w, http.StatusOK, map[string]any{"ok": res.OK, "status": res.Status})
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// MARK: - Dispatch

func (s *Server) webhookDispatcher() *webhooks.Dispatcher {
	s.hooks.mu.Lock()
	defer s.hooks.mu.Unlock()
	if s.hooks.disp == nil {
		s.hooks.disp = &webhooks.Dispatcher{OnResult: func(t webhooks.Target, res webhooks.Result) {
			if err := s.Store.SetWebhookResult(s.ctx(), t.ID, res.Status, res.At.Unix()); err != nil {
				return
			}
			if !res.OK {
				slog.Warn("webhook delivery failed", "webhook", t.ID, "status", res.Status)
			}
		}}
	}
	return s.hooks.disp
}

func (s *Server) invalidateWebhooks() {
	s.hooks.mu.Lock()
	s.hooks.loaded = false
	s.hooks.mu.Unlock()
}

func (s *Server) webhookList(ctx context.Context) []store.Webhook {
	s.hooks.mu.Lock()
	defer s.hooks.mu.Unlock()
	if !s.hooks.loaded {
		list, err := s.Store.Webhooks(ctx)
		if err != nil {
			return nil
		}
		s.hooks.list, s.hooks.loaded = list, true
	}
	return s.hooks.list
}

// runWebhooks subscribes to the event bus and runs the delivery queue until
// ctx ends. Started by RunRules.
func (s *Server) runWebhooks(ctx context.Context) {
	d := s.webhookDispatcher()
	s.hooks.mu.Lock()
	subscribe := !s.hooks.hooked
	s.hooks.hooked = true
	s.hooks.mu.Unlock()
	if subscribe {
		sub := s.Bus.Subscribe("webhooks", 0)
		bus.Handle(sub, TopicAlarmNew, func(a store.Alarm) { s.fireAlarm("alarm.new", &a, true) })
		bus.Handle(sub, TopicAlarmState, func(a store.Alarm) { s.fireAlarm("alarm.state", &a, false) })
		bus.Handle(sub, TopicEventNew, func(m EventMessage) {
			cam := ""
			if m.Camera != nil {
				cam = m.Camera.Name
			}
			s.fireSubscribed("event.new", cam, m.Event.CreatedAt)
		})
		bus.Handle(sub, TopicRecorderOnline, func(m RecorderOnline) { s.fireSubscribed("recorder.online", "", 0) })
		defer sub.Close()
	}
	d.Run(ctx)
}

func (s *Server) fireSubscribed(event, camera string, ts int64) {
	if ts == 0 {
		ts = time.Now().Unix()
	}
	ctx := s.ctx()
	d := s.webhookDispatcher()
	for _, w := range s.webhookList(ctx) {
		if w.Enabled && containsString(w.Events, event) {
			d.Enqueue(webhooks.Target{ID: w.ID, URL: w.URL, Secret: w.Secret}, webhooks.Payload{Event: event, Camera: camera, Server: s.serverName(), TS: ts})
		}
	}
}

// fireAlarm sends an alarm to the webhooks subscribed to event and, for new
// alarms, to the webhook actions of every rule the alarm matches (the rule's
// time profile included, since Matches applies it).
func (s *Server) fireAlarm(event string, a *store.Alarm, ruleActions bool) {
	ctx := s.ctx()
	d := s.webhookDispatcher()
	byID := map[string]store.Webhook{}
	list := s.webhookList(ctx)
	for _, w := range list {
		byID[w.ID] = w
	}
	ts := a.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	send := func(w store.Webhook, ruleID string) {
		d.Enqueue(webhooks.Target{ID: w.ID, URL: w.URL, Secret: w.Secret},
			webhooks.Payload{Event: event, Camera: a.CameraName, Server: s.serverName(), TS: ts, RuleID: ruleID})
	}
	for _, w := range list {
		if w.Enabled && containsString(w.Events, event) {
			send(w, "")
		}
	}
	if !ruleActions {
		return
	}
	alarm := ruleAlarm(a)
	rs := s.alarmRules(ctx)
	for i := range rs {
		if len(rs[i].Actions) == 0 || !rs[i].Matches(alarm, s.rulesLoc()) {
			continue
		}
		for _, act := range rs[i].Actions {
			if w, ok := byID[act.WebhookID]; ok && act.Type == rules.ActionWebhook && w.Enabled {
				send(w, rs[i].ID)
			}
		}
	}
}

// validateRuleRefs checks that a rule's time profile and webhook actions exist.
func (s *Server) validateRuleRefs(ctx context.Context, rule *rules.Rule) error {
	if rule.TimeProfileID != "" {
		if _, err := s.Store.TimeProfile(ctx, rule.TimeProfileID); errors.Is(err, store.ErrNotFound) {
			return inputError{"that time profile no longer exists"}
		} else if err != nil {
			return err
		}
	}
	for _, a := range rule.Actions {
		if _, err := s.Store.Webhook(ctx, a.WebhookID); errors.Is(err, store.ErrNotFound) {
			return inputError{"a webhook action points at a webhook that doesn't exist"}
		} else if err != nil {
			return err
		}
	}
	return nil
}
