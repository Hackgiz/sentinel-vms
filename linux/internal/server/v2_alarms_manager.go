package server

// Alarm Manager endpoints (WAVE4-OPERATOR.md, Stream C): filter parsing for
// the alarm list, per-state counts, assignment, the events behind an alarm
// and AI triage. The list handler itself lives in v2_alarms.go.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/ai"
	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const maxOwnerName = 100

func (s *Server) registerV2AlarmManager(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/alarms/counts", auth.ViewLive, s.handleV2AlarmCounts)
	s.v2Route(mux, "GET /api/v2/alarms/{alarmId}/events", auth.ViewLive, s.handleV2AlarmEvents)
	s.v2Route(mux, "POST /api/v2/alarms/{alarmId}/assign", auth.AcknowledgeAlarms, s.handleV2AlarmAssign)
	s.v2Route(mux, "POST /api/v2/alarms/{alarmId}/severity", auth.AcknowledgeAlarms, s.handleV2AlarmSeverity)
	s.v2Route(mux, "POST /api/v2/alarms/{alarmId}/triage", auth.AcknowledgeAlarms, s.handleV2AlarmTriage)
}

// parseAlarmQuery reads the alarm filters from the query string. withStates
// false (the counts endpoint) ignores state/states. Cameras are returned
// unscoped; callers pass them through scopedCameraIDs.
func (s *Server) parseAlarmQuery(w http.ResponseWriter, r *http.Request, withStates bool) (store.AlarmQuery, bool) {
	v := r.URL.Query()
	q := store.AlarmQuery{Severities: csv(v.Get("severities")), CameraIDs: csv(v.Get("cameras")),
		Owner: strings.TrimSpace(v.Get("owner")), Text: strings.TrimSpace(v.Get("q"))}
	if id := v.Get("cameraId"); id != "" {
		q.CameraIDs = append(q.CameraIDs, id)
	}
	if withStates {
		switch {
		case v.Get("states") != "":
			q.States = csv(v.Get("states"))
			for _, st := range q.States {
				if !store.ValidAlarmState(st) {
					writeV2Error(w, http.StatusBadRequest, codeBadRequest, "unknown alarm state "+st)
					return q, false
				}
			}
		default:
			switch v.Get("state") {
			case "", "open":
				q.States = []string{"New", "Acknowledged", "Investigating", "Snoozed"}
			case "all":
			default:
				writeV2Error(w, http.StatusBadRequest, codeBadRequest, "state must be open or all")
				return q, false
			}
		}
	}
	from, ok := parseMs(w, v.Get("from"), "from")
	if !ok {
		return q, false
	}
	to, ok := parseMs(w, v.Get("to"), "to")
	if !ok {
		return q, false
	}
	q.From, q.To = msToSec(from), msToSec(to)
	return q, true
}

// handleV2AlarmCounts answers {counts: {state: n}} for the state chips under
// every other filter.
func (s *Server) handleV2AlarmCounts(w http.ResponseWriter, r *http.Request) {
	q, ok := s.parseAlarmQuery(w, r, false)
	if !ok {
		return
	}
	counts := map[string]int{}
	for _, st := range store.AlarmStates {
		counts[st] = 0
	}
	if ids, none := s.scopedCameraIDs(r.Context(), q.CameraIDs); !none {
		q.CameraIDs = ids
		var err error
		if counts, err = s.Store.AlarmStateCounts(r.Context(), q); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

// handleV2AlarmEvents lists the detection events behind an alarm.
func (s *Server) handleV2AlarmEvents(w http.ResponseWriter, r *http.Request) {
	a, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId"))
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	events, err := s.Store.EventsForAlarm(r.Context(), a.ID, 0)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	items := make([]v2Event, 0, len(events))
	for i := range events {
		items = append(items, toV2Event(&events[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleV2AlarmAssign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Owner string `json:"owner"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Owner = strings.TrimSpace(in.Owner)
	if len([]rune(in.Owner)) > maxOwnerName {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "owner is too long")
		return
	}
	if in.Owner != "" {
		if _, err := s.Store.UserByName(r.Context(), in.Owner); err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "no such user")
			return
		}
	}
	if _, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId")); err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	by := currentUser(r).Name
	line := "Assigned to " + in.Owner + " by " + by
	if in.Owner == "" {
		line = "Unassigned by " + by
	}
	a, err := s.Store.AssignAlarm(r.Context(), r.PathValue("alarmId"), in.Owner, line)
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	s.audit(r, "Alarms", "Assigned alarm", a.Title+" ("+a.CameraName+") → "+a.Owner)
	s.publishAlarmState(a)
	writeJSON(w, http.StatusOK, s.v2AlarmView(r.Context(), a))
}

// aiGate runs the checks every AI endpoint shares with POST /events/ask: AI
// on (409 otherwise), a unit of the daily budget (429), a client (409). It
// writes the error itself and reports ok=false.
func (s *Server) aiGate(w http.ResponseWriter) (*ai.Client, bool) {
	if !s.aiReady() {
		writeV2Error(w, http.StatusConflict, codeConflict, "AI is off — add your Anthropic API key and turn AI on in System → AI")
		return nil, false
	}
	if !s.aiTakeBudget() {
		writeV2Error(w, http.StatusTooManyRequests, codeTooMany, "the daily AI limit is reached")
		return nil, false
	}
	client, err := s.aiClient()
	if err != nil {
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
		return nil, false
	}
	return client, true
}

// aiFailed reports a failed model call the way events/ask does.
func (s *Server) aiFailed(w http.ResponseWriter, err error) {
	s.setAIError(err)
	writeV2Error(w, http.StatusBadGateway, codeUnavailable, err.Error())
}

func (s *Server) eventLine(e *store.Event, names map[string]string) ai.EventLine {
	cam := names[e.CameraID]
	if cam == "" {
		cam = "Camera"
	}
	kind := e.Kind
	if e.Label != "" {
		kind += " (" + e.Label + ")"
	}
	return ai.EventLine{ID: e.ID, Time: time.Unix(e.CreatedAt, 0), Camera: cam, Kind: kind, Description: e.Description, Tags: e.Tags, Threat: e.Threat}
}

// handleV2AlarmTriage asks the model to summarize an alarm and suggest a
// severity. It never changes the alarm: the operator applies the suggestion
// with the ordinary alarm routes.
func (s *Server) handleV2AlarmTriage(w http.ResponseWriter, r *http.Request) {
	a, err := s.scopedAlarm(r.Context(), r.PathValue("alarmId"))
	if err != nil {
		writeV2Fail(w, err, "alarm")
		return
	}
	client, ok := s.aiGate(w)
	if !ok {
		return
	}
	events, err := s.Store.EventsForAlarm(r.Context(), a.ID, ai.MaxDigestEvents)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	names := s.cameraNames(r.Context())
	facts := ai.AlarmFacts{Title: a.Title, Detail: a.Detail, Kind: a.Kind, Camera: a.CameraName, Severity: a.Severity, State: a.State, RaisedAt: time.Unix(a.CreatedAt, 0)}
	for i := range events {
		facts.Events = append(facts.Events, s.eventLine(&events[i], names))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	t, err := client.TriageAlarm(ctx, facts)
	if err != nil {
		s.aiFailed(w, err)
		return
	}
	s.setAIError(nil)
	s.audit(r, "AI", "Triaged alarm", a.Title+" ("+a.CameraName+")")
	writeJSON(w, http.StatusOK, map[string]any{"summary": t.Summary, "suggestedSeverity": t.SuggestedSeverity, "rationale": t.Rationale})
}
