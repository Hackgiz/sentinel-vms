package server

// Incidents (WAVE4-OPERATOR.md, Stream C): case files that tie alarms,
// events, evidence and notes together, with an AI-drafted report.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/ai"
	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

const (
	maxIncidentTitle   = 200
	maxIncidentSummary = 20000
	maxIncidentNote    = 2000
)

func (s *Server) registerV2Incidents(mux *http.ServeMux) {
	s.registerV2AlarmManager(mux) // v2_alarms_manager.go
	s.v2Route(mux, "GET /api/v2/incidents", auth.ViewLive, s.handleV2Incidents)
	s.v2Route(mux, "POST /api/v2/incidents", auth.AcknowledgeAlarms, s.handleV2IncidentCreate)
	s.v2Route(mux, "GET /api/v2/incidents/{id}", auth.ViewLive, s.handleV2Incident)
	s.v2Route(mux, "PATCH /api/v2/incidents/{id}", auth.AcknowledgeAlarms, s.handleV2IncidentPatch)
	s.v2Route(mux, "POST /api/v2/incidents/{id}/items", auth.AcknowledgeAlarms, s.handleV2IncidentAddItem)
	s.v2Route(mux, "DELETE /api/v2/incidents/{id}/items/{itemId}", auth.AcknowledgeAlarms, s.handleV2IncidentRemoveItem)
	s.v2Route(mux, "POST /api/v2/incidents/{id}/report", auth.AcknowledgeAlarms, s.handleV2IncidentReport)
}

type v2Incident struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Severity  string `json:"severity"`
	Summary   string `json:"summary"`
	CreatedBy string `json:"createdBy"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	ItemCount int    `json:"itemCount"`
}

func toV2Incident(i *store.Incident) v2Incident {
	return v2Incident{ID: i.ID, Title: i.Title, Status: i.Status, Severity: i.Severity, Summary: i.Summary, CreatedBy: i.CreatedBy,
		CreatedAt: secToMs(i.CreatedAt), UpdatedAt: secToMs(i.UpdatedAt), ItemCount: i.ItemCount}
}

// v2IncidentItem is an item resolved to its live object. Restricted: the
// object exists but lies outside the caller's camera scope, so only the
// item's own bookkeeping is shown. Missing: the object is gone.
type v2IncidentItem struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	RefID      string      `json:"refId,omitempty"`
	Note       string      `json:"note,omitempty"`
	AddedBy    string      `json:"addedBy"`
	AddedAt    int64       `json:"addedAt"`
	At         int64       `json:"at"` // when it happened, for the chronology
	Restricted bool        `json:"restricted,omitempty"`
	Missing    bool        `json:"missing,omitempty"`
	Alarm      *v2Alarm    `json:"alarm,omitempty"`
	Event      *v2Event    `json:"event,omitempty"`
	Evidence   *v2Evidence `json:"evidence,omitempty"`
}

// resolveIncidentItems turns stored items into live objects within the
// caller's scope, ordered by when the things happened.
func (s *Server) resolveIncidentItems(ctx context.Context, items []store.IncidentItem) []v2IncidentItem {
	sc := s.cameraScope(ctx)
	out := make([]v2IncidentItem, 0, len(items))
	for _, it := range items {
		v := v2IncidentItem{ID: it.ID, Kind: it.Kind, RefID: it.RefID, Note: it.Note, AddedBy: it.AddedBy, AddedAt: secToMs(it.AddedAt), At: secToMs(it.AddedAt)}
		switch it.Kind {
		case "alarm":
			a, err := s.Store.Alarm(ctx, it.RefID)
			switch {
			case err != nil:
				v.Missing = true
			case !sc.alarm(a):
				v.Restricted = true
			default:
				av := s.v2AlarmView(ctx, a)
				v.Alarm, v.At = &av, secToMs(a.CreatedAt)
			}
		case "event":
			e, err := s.Store.Event(ctx, it.RefID)
			switch {
			case err != nil:
				v.Missing = true
			case !sc.camera(e.CameraID):
				v.Restricted = true
			default:
				ev := toV2Event(e)
				v.Event, v.At = &ev, secToMs(e.CreatedAt)
			}
		case "evidence":
			e, err := s.Store.EvidenceByID(ctx, it.RefID)
			switch {
			case err != nil:
				v.Missing = true
			case !sc.camera(e.CameraID):
				v.Restricted = true
			default:
				ev := toV2Evidence(e)
				v.Evidence, v.At = &ev, secToMs(e.LockedAt)
			}
		}
		if v.Restricted {
			v.RefID = "" // don't hand out ids of things the caller can't open
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func (s *Server) incidentDetail(ctx context.Context, i *store.Incident) (map[string]any, error) {
	items, err := s.Store.IncidentItems(ctx, i.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"incident": toV2Incident(i), "items": s.resolveIncidentItems(ctx, items)}, nil
}

func (s *Server) handleV2Incidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.IncidentFilter{Status: q.Get("status"), Text: strings.TrimSpace(q.Get("q")), Before: q.Get("before")}
	if f.Status != "" && !store.ValidIncidentStatus(f.Status) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "status must be open, investigating or closed")
		return
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageLimit {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 500")
			return
		}
		f.Limit = n
	}
	list, next, err := s.Store.SearchIncidents(r.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
		return
	}
	if err != nil {
		writeV2Err(w, err)
		return
	}
	items := make([]v2Incident, 0, len(list))
	for i := range list {
		items = append(items, toV2Incident(&list[i]))
	}
	resp := map[string]any{"items": items}
	if next != "" {
		resp["nextCursor"] = next
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleV2IncidentCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title    string `json:"title"`
		Severity string `json:"severity"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Severity == "" {
		in.Severity = "medium"
	}
	switch {
	case in.Title == "" || len([]rune(in.Title)) > maxIncidentTitle:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "title is required (200 characters max)")
		return
	case !store.ValidIncidentSeverity(in.Severity):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "severity must be low, medium, high or critical")
		return
	}
	i, err := s.Store.CreateIncident(r.Context(), in.Title, in.Severity, currentUser(r).Name)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Alarms", "Opened incident", i.Title)
	writeJSON(w, http.StatusCreated, toV2Incident(i))
}

func (s *Server) handleV2Incident(w http.ResponseWriter, r *http.Request) {
	i, err := s.Store.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	d, err := s.incidentDetail(r.Context(), i)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleV2IncidentPatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title    *string `json:"title"`
		Status   *string `json:"status"`
		Severity *string `json:"severity"`
		Summary  *string `json:"summary"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		in.Title = &t
	}
	switch {
	case in.Title != nil && (*in.Title == "" || len([]rune(*in.Title)) > maxIncidentTitle):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "title is required (200 characters max)")
		return
	case in.Status != nil && !store.ValidIncidentStatus(*in.Status):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "status must be open, investigating or closed")
		return
	case in.Severity != nil && !store.ValidIncidentSeverity(*in.Severity):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "severity must be low, medium, high or critical")
		return
	case in.Summary != nil && len([]rune(*in.Summary)) > maxIncidentSummary:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "summary is too long")
		return
	}
	before, err := s.Store.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	i, err := s.Store.UpdateIncident(r.Context(), before.ID, store.IncidentPatch{Title: in.Title, Status: in.Status, Severity: in.Severity, Summary: in.Summary})
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	if in.Status != nil && *in.Status != before.Status {
		s.audit(r, "Alarms", "Incident "+*in.Status, i.Title+" ("+before.Status+" → "+*in.Status+")")
	}
	if in.Severity != nil && *in.Severity != before.Severity {
		s.audit(r, "Alarms", "Changed incident severity", i.Title+" ("+before.Severity+" → "+*in.Severity+")")
	}
	if in.Title != nil || in.Summary != nil {
		s.audit(r, "Alarms", "Edited incident", i.Title)
	}
	writeJSON(w, http.StatusOK, toV2Incident(i))
}

func (s *Server) handleV2IncidentAddItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind  string `json:"kind"`
		RefID string `json:"refId"`
		Note  string `json:"note"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case !store.ValidIncidentItemKind(in.Kind):
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "kind must be alarm, event, evidence or note")
		return
	case in.Kind == "note" && in.Note == "":
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "a note needs text")
		return
	case in.Kind != "note" && in.RefID == "":
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "refId is required")
		return
	case len([]rune(in.Note)) > maxIncidentNote:
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "note is too long (2000 characters max)")
		return
	}
	inc, err := s.Store.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	// What is added must exist and be visible to the caller (404 otherwise).
	switch in.Kind {
	case "alarm":
		_, err = s.scopedAlarm(r.Context(), in.RefID)
	case "event":
		_, err = s.scopedEvent(r.Context(), in.RefID)
	case "evidence":
		var e *store.Evidence
		if e, err = s.Store.EvidenceByID(r.Context(), in.RefID); err == nil && !s.cameraScope(r.Context()).camera(e.CameraID) {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		writeV2Fail(w, err, in.Kind)
		return
	}
	it, err := s.Store.AddIncidentItem(r.Context(), inc.ID, in.Kind, in.RefID, in.Note, currentUser(r).Name)
	if errors.Is(err, store.ErrDuplicateItem) {
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
		return
	}
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	s.audit(r, "Alarms", "Added "+in.Kind+" to incident", inc.Title)
	writeJSON(w, http.StatusCreated, s.resolveIncidentItems(r.Context(), []store.IncidentItem{*it})[0])
}

func (s *Server) handleV2IncidentRemoveItem(w http.ResponseWriter, r *http.Request) {
	inc, err := s.Store.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	if err := s.Store.RemoveIncidentItem(r.Context(), inc.ID, r.PathValue("itemId")); err != nil {
		writeV2Fail(w, err, "item")
		return
	}
	s.audit(r, "Alarms", "Removed item from incident", inc.Title)
	w.WriteHeader(http.StatusNoContent)
}

// incidentLine renders a resolved item as one chronology line for the
// report drafter. Restricted and missing items are left out, so the model
// never sees what the caller can't.
func (s *Server) incidentLine(v v2IncidentItem, names map[string]string) string {
	at := time.UnixMilli(v.At).Format("Mon Jan 2 3:04 PM")
	switch {
	case v.Restricted || v.Missing:
		return ""
	case v.Alarm != nil:
		a := v.Alarm
		return fmt.Sprintf("%s · Alarm (%s, %s) %s on %s: %s [state %s]", at, a.Severity, a.Kind, a.Title, a.CameraName, a.Detail, a.State)
	case v.Event != nil:
		e := v.Event
		cam := names[e.CameraID]
		return fmt.Sprintf("%s · Detection %s %s on %s: %s [threat %s]", at, e.Kind, e.Label, cam, e.Description, e.Threat)
	case v.Evidence != nil:
		e := v.Evidence
		return fmt.Sprintf("%s · Evidence locked, case %s on %s (%s) by %s", at, e.CaseID, e.CameraName, e.Range, e.LockedBy)
	case v.Kind == "note":
		return fmt.Sprintf("%s · Note by %s: %s", at, v.AddedBy, v.Note)
	}
	return ""
}

// handleV2IncidentReport drafts a markdown report from the incident's items
// into its summary. The operator edits and saves it; nothing is published.
func (s *Server) handleV2IncidentReport(w http.ResponseWriter, r *http.Request) {
	inc, err := s.Store.Incident(r.Context(), r.PathValue("id"))
	if err != nil {
		writeV2Fail(w, err, "incident")
		return
	}
	client, ok := s.aiGate(w)
	if !ok {
		return
	}
	items, err := s.Store.IncidentItems(r.Context(), inc.ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	names := s.cameraNames(r.Context())
	facts := ai.IncidentFacts{Title: inc.Title, Severity: inc.Severity, Status: inc.Status}
	for _, v := range s.resolveIncidentItems(r.Context(), items) {
		if l := s.incidentLine(v, names); l != "" {
			facts.Lines = append(facts.Lines, l)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	draft, err := client.DraftIncidentReport(ctx, facts)
	if err != nil {
		s.aiFailed(w, err)
		return
	}
	s.setAIError(nil)
	updated, err := s.Store.UpdateIncident(r.Context(), inc.ID, store.IncidentPatch{Summary: &draft})
	if err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "AI", "Drafted incident report", inc.Title)
	writeJSON(w, http.StatusOK, toV2Incident(updated))
}
