package server

// API v2: AI rule drafts and rule templates (WAVE14-AI-ASSIST.md §1–2).
// Neither saves anything: the admin reviews the rule in the editor and saves
// it with POST /rules like any other.

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"sentinel-linux/internal/ai"
	"sentinel-linux/internal/rules"
)

// Bounds on the description a rule is drafted from, in characters.
const (
	minDraftText = 3
	maxDraftText = 500
)

// v2RuleTemplate is RuleTemplate.
type v2RuleTemplate struct {
	ID       string      `json:"id"`
	Category string      `json:"category"`
	Title    string      `json:"title"`
	Summary  string      `json:"summary"`
	Rule     v2RuleInput `json:"rule"`
}

func (s *Server) handleV2RuleTemplates(w http.ResponseWriter, r *http.Request) {
	list := rules.Templates()
	out := make([]v2RuleTemplate, 0, len(list))
	for i := range list {
		t := &list[i]
		out = append(out, v2RuleTemplate{ID: t.ID, Category: t.Category, Title: t.Title, Summary: t.Summary, Rule: toV2Rule(&t.Rule).v2RuleInput})
	}
	v2Items(w, out)
}

// handleV2DraftRule turns the admin's description into a rule with one model
// call. It saves and enables nothing: the draft comes back disabled, with
// what couldn't be resolved listed in warnings.
func (s *Server) handleV2DraftRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if n := utf8.RuneCountInString(in.Text); n < minDraftText || n > maxDraftText {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "describe the rule in 3 to 500 characters")
		return
	}
	client, ok := s.aiGate(w)
	if !ok {
		return
	}
	ctx := r.Context()
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	profiles, err := s.Store.TimeProfiles(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	// Only cameras the caller may see are offered to the model or resolved.
	cams = s.visibleCameras(ctx, cams)
	facts := ai.RuleDraftFacts{Text: in.Text, Kinds: rules.Kinds, Severities: rules.Severities, Now: time.Now().In(s.rulesLoc())}
	camRefs := make([]rules.NameRef, 0, len(cams))
	for _, c := range cams {
		facts.Cameras = append(facts.Cameras, ai.DraftCamera{Name: c.Name, Location: c.Location})
		camRefs = append(camRefs, rules.NameRef{ID: c.ID, Name: c.Name})
	}
	profileRefs := make([]rules.NameRef, 0, len(profiles))
	for _, p := range profiles {
		facts.TimeProfiles = append(facts.TimeProfiles, p.Name)
		profileRefs = append(profileRefs, rules.NameRef{ID: p.ID, Name: p.Name})
	}
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	d, err := client.DraftRule(mctx, facts)
	if err != nil {
		s.aiFailed(w, err)
		return
	}
	s.setAIError(nil)
	rule, unresolved, err := rules.ResolveDraft(rules.Draft{
		Name: d.Name, CameraNames: d.CameraNames, Kinds: d.Kinds, MinSeverity: d.MinSeverity,
		Schedule: d.Schedule.Enabled, From: d.Schedule.From, To: d.Schedule.To, Days: d.Schedule.Days,
		TimeProfileName: d.TimeProfileName, Delivery: d.Delivery, Instructions: d.Instructions,
	}, camRefs, profileRefs)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "Tell me which cameras and what should raise the alarm.")
		return
	}
	// The model's notes on what a rule can't do come first, then what the
	// server couldn't resolve.
	warnings := append(append([]string{}, d.Warnings...), unresolved...)
	s.audit(r, "AI", "Drafted rule", rule.Name)
	writeJSON(w, http.StatusOK, map[string]any{"rule": toV2Rule(&rule).v2RuleInput, "explanation": d.Explanation, "warnings": warnings})
}
