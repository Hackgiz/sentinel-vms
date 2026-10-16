package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestTriageAlarmParsesAndSanitizes(t *testing.T) {
	srv, got := fakeAnthropic(t, "```json\n{\"summary\":\"Someone tried the door.\",\"suggestedSeverity\":\"CRITICAL\",\"rationale\":\"Night, hooded.\"}\n```", "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	tr, err := c.TriageAlarm(context.Background(), AlarmFacts{Title: "Person detected", Camera: "Door", Severity: "Warning", State: "New", RaisedAt: time.Now(),
		Events: []EventLine{{ID: "e", Time: time.Now(), Camera: "Door", Kind: "Person", Description: "Man tries the handle", Threat: "elevated"}}})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Summary != "Someone tried the door." || tr.SuggestedSeverity != "Critical" || tr.Rationale != "Night, hooded." {
		t.Fatalf("%+v", tr)
	}
	b, _ := json.Marshal((*got)[0].body)
	if !strings.Contains(string(b), "Man tries the handle") || !strings.Contains(string(b), "Person detected") {
		t.Fatalf("request lacks the facts: %s", b)
	}
	if (*got)[0].body["model"] != TextModel {
		t.Fatalf("model %v", (*got)[0].body["model"])
	}

	// An invented severity is dropped, not passed on; no JSON is an error.
	srv2, _ := fakeAnthropic(t, `{"summary":"x","suggestedSeverity":"apocalyptic","rationale":"y"}`, "end_turn")
	tr, err = New("sk-test", option.WithBaseURL(srv2.URL)).TriageAlarm(context.Background(), AlarmFacts{Title: "a"})
	if err != nil || tr.SuggestedSeverity != "" {
		t.Fatalf("%+v %v", tr, err)
	}
	srv3, _ := fakeAnthropic(t, "I cannot say.", "end_turn")
	if _, err = New("sk-test", option.WithBaseURL(srv3.URL)).TriageAlarm(context.Background(), AlarmFacts{Title: "a"}); err == nil {
		t.Fatal("want an error without JSON")
	}
}

func TestDraftIncidentReport(t *testing.T) {
	srv, got := fakeAnthropic(t, "## Summary\nA delivery.", "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	out, err := c.DraftIncidentReport(context.Background(), IncidentFacts{Title: "Delivery", Severity: "low", Status: "open", Lines: []string{"9:00 AM · Alarm: Person at door"}})
	if err != nil || out != "## Summary\nA delivery." {
		t.Fatalf("%q %v", out, err)
	}
	b, _ := json.Marshal((*got)[0].body)
	if !strings.Contains(string(b), "Person at door") {
		t.Fatalf("request lacks the chronology: %s", b)
	}
}
