package server

// Alarm rules (Mac: Notifications → Alarm Rules). See internal/rules for how
// rules combine with each camera's alarm mode; in short, the camera decides
// whether an alarm is raised, rules decide what the operator is told
// (instructions) and whether the iPhone is pushed.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

type rulesState struct {
	mu     sync.Mutex
	loaded bool
	list   []rules.Rule
	loc    *time.Location // nil = server local time; tests pin it
}

func (s *Server) registerRules(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/rules", s.authed(auth.ViewLive, s.handleListRules))
	mux.Handle("POST /api/v1/rules", s.authed(auth.ChangeSettings, s.handleSaveRule))
	mux.Handle("POST /api/v1/rules/suggested", s.authed(auth.ChangeSettings, s.handleAddSuggestedRules))
	mux.Handle("PUT /api/v1/rules/{id}", s.authed(auth.ChangeSettings, s.handleSaveRule))
	mux.Handle("DELETE /api/v1/rules/{id}", s.authed(auth.ChangeSettings, s.handleDeleteRule))
	mux.Handle("GET /api/v1/push", s.authed(auth.ChangeSettings, s.handleGetPush))
	mux.Handle("PUT /api/v1/push", s.authed(auth.ChangeSettings, s.handlePutPush))
	mux.Handle("POST /api/v1/push/test", s.authed(auth.ChangeSettings, s.handleTestPush))
}

func (s *Server) rulesLoc() *time.Location {
	if s.rules.loc != nil {
		return s.rules.loc
	}
	return time.Local
}

// alarmRules returns the cached rule list, loading it on first use.
func (s *Server) alarmRules(ctx context.Context) []rules.Rule {
	st := &s.rules
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.loaded {
		list, err := s.Store.AlarmRules(ctx)
		if err != nil {
			return nil
		}
		st.list, st.loaded = list, true
	}
	return st.list
}

func (s *Server) invalidateRules() {
	s.rules.mu.Lock()
	s.rules.loaded = false
	s.rules.mu.Unlock()
}

func ruleAlarm(a *store.Alarm) rules.Alarm {
	at := a.LastEventAt
	if at == 0 {
		at = a.CreatedAt
	}
	return rules.Alarm{CameraID: a.CameraID, Kind: a.Kind, Severity: a.Severity, At: time.Unix(at, 0)}
}

// alarmInstructions: what the operator should do, from every enabled rule
// matching the alarm (evaluated live, like the Mac, so edits apply to open
// alarms). Always a non-nil list: the phone API sends [] when there are none.
func (s *Server) alarmInstructions(ctx context.Context, a *store.Alarm) []rules.Instruction {
	return rules.Instructions(s.alarmRules(ctx), ruleAlarm(a), s.rulesLoc())
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.AlarmRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zone, _ := time.Now().In(s.rulesLoc()).Zone()
	writeJSON(w, http.StatusOK, map[string]any{
		"rules":      list,
		"kinds":      rules.Kinds,
		"severities": rules.Severities,
		"timeZone":   s.rulesLoc().String(),
		"zoneAbbrev": zone,
	})
}

func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var in rules.Rule
	if !readJSON(w, r, &in) {
		return
	}
	created, err := s.saveRule(r, r.PathValue("id"), &in)
	var bad inputError
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	} else if errors.As(err, &bad) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, in)
}

// saveRule creates (id "") or replaces a rule, filling in its id and times,
// and audits it (v1 and v2). created reports a new rule.
func (s *Server) saveRule(r *http.Request, id string, in *rules.Rule) (created bool, err error) {
	var old *rules.Rule
	if id != "" {
		if old, err = s.Store.AlarmRule(r.Context(), id); err != nil {
			return false, err
		}
		in.ID, in.CreatedAt = id, old.CreatedAt
	} else {
		in.ID = ""
	}
	in.Normalize()
	if err := in.Validate(); err != nil {
		return false, inputError{err.Error()}
	}
	if err := s.Store.SaveAlarmRule(r.Context(), in); err != nil {
		return false, err
	}
	s.invalidateRules()
	action := "Created rule"
	if old != nil {
		action = "Edited rule"
		if onlyEnabledChanged(*old, *in) {
			action = map[bool]string{true: "Enabled rule", false: "Disabled rule"}[in.Enabled]
		}
	}
	s.audit(r, "Alarm Rules", action, in.Name+" — "+describeRule(*in))
	return old == nil, nil
}

func onlyEnabledChanged(a, b rules.Rule) bool {
	if a.Enabled == b.Enabled {
		return false
	}
	a.Enabled, a.UpdatedAt, b.UpdatedAt = b.Enabled, 0, 0
	return describeRule(a) == describeRule(b) && a.Name == b.Name && a.Instructions == b.Instructions
}

func (s *Server) handleAddSuggestedRules(w http.ResponseWriter, r *http.Request) {
	var added []rules.Rule
	for _, rule := range rules.Suggested() {
		rule.Normalize()
		if err := s.Store.SaveAlarmRule(r.Context(), &rule); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		added = append(added, rule)
		s.audit(r, "Alarm Rules", "Created rule", rule.Name+" — "+describeRule(rule))
	}
	s.invalidateRules()
	writeJSON(w, http.StatusCreated, added)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteRule(r, r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deleteRule removes a rule and audits it (v1 and v2).
func (s *Server) deleteRule(r *http.Request, id string) error {
	old, err := s.Store.AlarmRule(r.Context(), id)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteAlarmRule(r.Context(), old.ID); err != nil {
		return err
	}
	s.invalidateRules()
	s.audit(r, "Alarm Rules", "Deleted rule", old.Name)
	return nil
}

// describeRule is the one-line summary written to the audit log.
func describeRule(r rules.Rule) string {
	var parts []string
	if len(r.Kinds) == 0 {
		parts = append(parts, "any alarm")
	} else {
		parts = append(parts, strings.Join(r.Kinds, "/"))
	}
	parts = append(parts, r.MinSeverity+"+")
	if len(r.CameraIDs) == 0 {
		parts = append(parts, "all cameras")
	} else {
		parts = append(parts, plural(len(r.CameraIDs), "camera"))
	}
	if r.Schedule {
		parts = append(parts, hhmm(r.FromMinute)+"–"+hhmm(r.ToMinute))
	}
	if len(r.Days) > 0 {
		names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
		var d []string
		for _, i := range r.Days {
			d = append(d, names[i])
		}
		parts = append(parts, strings.Join(d, ","))
	}
	if r.Delivery == rules.DeliveryPush {
		parts = append(parts, "push")
	} else {
		parts = append(parts, "dashboard only")
	}
	if !r.Enabled {
		parts = append(parts, "disabled")
	}
	return strings.Join(parts, ", ")
}

func hhmm(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
