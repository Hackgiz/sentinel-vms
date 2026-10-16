package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AlarmFacts is what the triage model sees of one alarm.
type AlarmFacts struct {
	Title, Detail, Kind, Camera, Severity, State string
	RaisedAt                                     time.Time
	Events                                       []EventLine
}

// Triage is the model's read of an alarm. It is advice only: nothing applies
// SuggestedSeverity until an operator accepts it.
type Triage struct {
	Summary           string
	SuggestedSeverity string // Critical, Warning or Info
	Rationale         string
}

const triageSystem = "You triage alarms for a security operator at a camera system. You get one alarm and the " +
	"detection events behind it (each with time, camera, kind, an AI description, tags and a threat level). " +
	"Reply with a single JSON object and nothing else: " +
	"{\"summary\": \"<one or two factual sentences on what happened>\", " +
	"\"suggestedSeverity\": \"Critical\"|\"Warning\"|\"Info\", " +
	"\"rationale\": \"<one or two sentences on why that severity>\"}. " +
	"Critical means likely harm, intrusion or emergency needing action now; Warning means unusual and worth a look; " +
	"Info means routine or benign. Use only what the alarm and events state; never invent details."

// Severities TriageAlarm may suggest, matching the alarm severity strings.
var triageSeverities = map[string]string{"critical": "Critical", "warning": "Warning", "info": "Info"}

// TriageAlarm summarizes an alarm and suggests a severity with one model call.
func (c *Client) TriageAlarm(ctx context.Context, a AlarmFacts) (Triage, error) {
	var b strings.Builder
	b.WriteString("Alarm: " + a.Title + "\n")
	if a.Detail != "" {
		b.WriteString("Detail: " + a.Detail + "\n")
	}
	b.WriteString("Kind: " + a.Kind + " · Camera: " + a.Camera + " · Current severity: " + a.Severity + " · State: " + a.State + "\n")
	if !a.RaisedAt.IsZero() {
		b.WriteString("Raised: " + a.RaisedAt.Format("Mon Jan 2 2006 3:04 PM") + "\n")
	}
	shown := a.Events
	if len(shown) > MaxDigestEvents {
		shown = shown[:MaxDigestEvents]
	}
	if len(shown) == 0 {
		b.WriteString("\nNo detection events are attached to this alarm.\n")
	} else {
		b.WriteString("\nEvents:\n")
		for _, e := range shown {
			b.WriteString("- " + e.format(-1) + "\n")
		}
	}
	raw, err := c.text(ctx, triageSystem, b.String(), 1000)
	if err != nil {
		return Triage{}, err
	}
	return parseTriage(raw)
}

func parseTriage(raw string) (Triage, error) {
	obj := firstJSONObject(raw)
	if obj == nil {
		return Triage{}, errors.New("Claude did not return a triage")
	}
	var p struct {
		Summary   string `json:"summary"`
		Severity  string `json:"suggestedSeverity"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(obj, &p); err != nil || strings.TrimSpace(p.Summary) == "" {
		return Triage{}, errors.New("Claude did not return a triage")
	}
	// An unknown severity is dropped rather than guessed; the UI then offers no Apply.
	return Triage{Summary: strings.TrimSpace(p.Summary), SuggestedSeverity: triageSeverities[strings.ToLower(strings.TrimSpace(p.Severity))], Rationale: strings.TrimSpace(p.Rationale)}, nil
}

// IncidentFacts is the chronology the report drafter sees.
type IncidentFacts struct {
	Title, Severity, Status string
	// Lines are the incident's items in order, already rendered as text
	// (restricted items left out by the caller).
	Lines []string
}

const reportSystem = "You draft an incident report for a security team from an incident's chronology. " +
	"Write concise markdown with these sections: a one-paragraph Summary, a Timeline (bulleted, in order, with times), " +
	"Findings, and Recommended follow-up. Stay factual: use only what the chronology states and mark anything uncertain as such. " +
	"The operator will edit your draft, so do not add signatures or placeholders beyond obvious ones like [name]."

// DraftIncidentReport writes a markdown report draft from an incident's items.
func (c *Client) DraftIncidentReport(ctx context.Context, in IncidentFacts) (string, error) {
	var b strings.Builder
	b.WriteString("Incident: " + in.Title + "\nSeverity: " + in.Severity + " · Status: " + in.Status + "\n\nChronology:\n")
	if len(in.Lines) == 0 {
		b.WriteString("(no items yet)\n")
	}
	for _, l := range in.Lines {
		b.WriteString("- " + l + "\n")
	}
	out, err := c.text(ctx, reportSystem, b.String(), 4000)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", errors.New("Claude did not return a report")
	}
	return out, nil
}
