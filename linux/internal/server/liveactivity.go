package server

// Alarm Live Activities on Sentinel Mobile (WAVE10, stream B). A phone that
// follows an alarm runs an ActivityKit Live Activity and hands the server
// that activity's push token:
//
//   - paired phones (companion API, like POST /devices/register):
//     POST /devices/activity-token {alarmId, activityToken}
//   - v2 sign-ins (like PUT /api/v2/push/registration):
//     PUT /api/v2/push/activity-token {apnsToken, alarmId, activityToken}
//     DELETE /api/v2/push/activity-token?apnsToken=…
//
// The token lives in paired_devices.activity_token (store/activity.go). When
// an operator changes a followed alarm's state (TopicAlarmState), every
// follower that may still see the alarm gets an APNs `liveactivity` push
// through the configured sender (direct APNs or the relay): event "update"
// while the alarm is open, "end" once it is resolved / a false alarm, after
// which the follow is forgotten.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/push"
	"sentinel-linux/internal/store"
)

const (
	// activityLinger: how long an ended alarm activity stays on the Lock Screen.
	activityLinger  = 15 * time.Minute
	activityRetry   = 3 * time.Second
	activityTimeout = 20 * time.Second
)

func (s *Server) registerCompanionActivity(mux *http.ServeMux) {
	mux.Handle("POST /devices/activity-token", s.phoneAuthed(s.handlePhoneActivityToken))
}

func (s *Server) registerV2LiveActivity(mux *http.ServeMux) {
	s.v2Route(mux, "PUT /api/v2/push/activity-token", auth.ViewLive, s.handleV2ActivityToken)
	s.v2Route(mux, "DELETE /api/v2/push/activity-token", auth.ViewLive, s.handleV2ActivityTokenDelete)
}

// handlePhoneActivityToken: an empty activityToken stops following.
func (s *Server) handlePhoneActivityToken(w http.ResponseWriter, r *http.Request, dev *store.PairedDevice) {
	var in struct {
		AlarmID       string `json:"alarmId"`
		ActivityToken string `json:"activityToken"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if json.Unmarshal(body, &in) != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if in.ActivityToken == "" {
		if err := s.Store.SetDeviceActivity(r.Context(), dev.ID, "", ""); err != nil && !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusServiceUnavailable, "server busy, try again")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if !apnsTokenPattern.MatchString(in.ActivityToken) {
		writeError(w, http.StatusBadRequest, "bad activityToken")
		return
	}
	if _, err := s.scopedAlarm(r.Context(), in.AlarmID); err != nil {
		writeError(w, http.StatusNotFound, "Alarm not found — it may have been cleared on the server.")
		return
	}
	if err := s.Store.SetDeviceActivity(r.Context(), dev.ID, in.AlarmID, in.ActivityToken); err != nil {
		writeError(w, http.StatusServiceUnavailable, "server busy, try again")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleV2ActivityToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APNSToken     string `json:"apnsToken"`
		AlarmID       string `json:"alarmId"`
		ActivityToken string `json:"activityToken"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if !apnsTokenPattern.MatchString(in.APNSToken) || !apnsTokenPattern.MatchString(in.ActivityToken) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "apnsToken and activityToken must be hex push tokens")
		return
	}
	if _, err := s.scopedAlarm(r.Context(), in.AlarmID); err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	err := s.Store.SetActivityForPushToken(r.Context(), in.APNSToken, currentUser(r).Name, in.AlarmID, in.ActivityToken)
	if errors.Is(err, store.ErrNotFound) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "this phone has no push registration — PUT /api/v2/push/registration first")
		return
	}
	if err != nil {
		writeV2Err(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2ActivityTokenDelete(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("apnsToken")
	if !apnsTokenPattern.MatchString(token) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "apnsToken must be the device token as hex")
		return
	}
	// Only the caller's own registration; anything else already follows nothing.
	if err := s.Store.SetActivityForPushToken(r.Context(), token, currentUser(r).Name, "", ""); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeV2Err(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runLiveActivities pushes followed alarms' state changes until ctx ends.
// Started by RunRules.
func (s *Server) runLiveActivities(ctx context.Context) {
	sub := s.Bus.Subscribe("liveactivity", 0)
	defer sub.Close()
	bus.Handle(sub, TopicAlarmState, func(a store.Alarm) { s.pushAlarmActivity(ctx, &a) })
	<-ctx.Done()
}

// activityFor shapes an alarm as a Live Activity push (content-state keys
// match the phone's AlarmActivityContent).
func activityFor(a *store.Alarm, now time.Time) push.Activity {
	updated := a.UpdatedAt
	if a.LastEventAt > updated {
		updated = a.LastEventAt
	}
	act := push.Activity{
		AlarmID:   a.ID,
		Event:     push.ActivityUpdate,
		Timestamp: now.Unix(),
		Critical:  a.Severity == "Critical",
		ContentState: push.ActivityContent{State: a.State, Owner: a.Owner, Severity: a.Severity,
			UpdatedAt: secToMs(updated), EventCount: a.EventCount},
	}
	if !store.IsOpenState(a.State) {
		act.Event = push.ActivityEnd
		act.Dismissal = now.Add(activityLinger).Unix()
	}
	return act
}

// pushAlarmActivity sends one alarm's state to every phone following it.
func (s *Server) pushAlarmActivity(ctx context.Context, a *store.Alarm) {
	followers, err := s.Store.ActivityFollowers(ctx, a.ID)
	if err != nil || len(followers) == 0 {
		return
	}
	sender, ok := s.pushSender().(push.ActivitySender)
	if !ok {
		return // push off, or a sender without Live Activity support
	}
	base := activityFor(a, time.Now())
	for i := range followers {
		f := followers[i]
		// A follower who lost sight of the camera stops getting updates.
		if !s.scopeOf(ctx, s.deviceUser(ctx, &f.Device)).alarm(a) {
			_ = s.Store.ClearDeviceActivity(ctx, f.Device.ID, f.Token)
			continue
		}
		act := base
		act.Token, act.DeviceToken = f.Token, f.Device.APNSToken()
		go s.sendActivity(ctx, sender, f, act)
	}
}

// sendActivity delivers one push with a single retry on a transient
// failure; a dead token or an ended activity forgets the follow.
func (s *Server) sendActivity(ctx context.Context, sender push.ActivitySender, f store.ActivityFollow, act push.Activity) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, activityTimeout)
		err = sender.SendActivity(sctx, act)
		cancel()
		if !errors.Is(err, push.ErrRetry) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(activityRetry):
		}
	}
	switch {
	case err == nil && act.Event == push.ActivityEnd, errors.Is(err, push.ErrInvalidToken):
		_ = s.Store.ClearDeviceActivity(s.ctx(), f.Device.ID, f.Token)
	case err != nil:
		slog.Warn("live activity push failed", "device", f.Device.ID, "alarm", f.AlarmID, "err", err)
		s.setPushError(err.Error())
	}
}
