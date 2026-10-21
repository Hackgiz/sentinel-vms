package ai

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestParseRuleDraft(t *testing.T) {
	d, err := ParseRuleDraft("Here is the rule:\n```json\n" + `{"name":" Dock after hours ","explanation":" Pushes people at the dock after 6pm. ",
		"cameraNames":["Loading dock"],"kinds":["Person"],"minSeverity":"Warning",
		"schedule":{"enabled":true,"from":"18:00","to":"07:00","days":[0,1,2,3,4,5,6]},
		"timeProfileName":"","delivery":"push","instructions":" Check the live view. ","warnings":[" ","Zones aren't supported."," a "," b "," c "," d "," e "]}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	want := RuleDraft{Name: "Dock after hours", Explanation: "Pushes people at the dock after 6pm.", CameraNames: []string{"Loading dock"},
		Kinds: []string{"Person"}, MinSeverity: "Warning", Schedule: DraftSchedule{Enabled: true, From: "18:00", To: "07:00", Days: []int{0, 1, 2, 3, 4, 5, 6}},
		Delivery: "push", Instructions: "Check the live view.", Warnings: []string{"Zones aren't supported.", "a", "b", "c", "d"}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got  %+v\nwant %+v", d, want)
	}

	// Values are left to rules.ResolveDraft; only the shape is checked here.
	if d, err := ParseRuleDraft(`{"kinds":["Teleport"],"minSeverity":"extreme"}`); err != nil || d.Kinds[0] != "Teleport" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, bad := range []string{"I can't draft that.", `{"name": "x", "schedule": {"days": "weekdays"}}`, `{"name": "x"`} {
		if _, err := ParseRuleDraft(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestDraftRuleSendsOnlyTheFacts(t *testing.T) {
	srv, got := fakeAnthropic(t, `{"name":"Dock","explanation":"x","cameraNames":["Loading dock"],"kinds":["Person"],"minSeverity":"Warning",
		"schedule":{"enabled":false},"timeProfileName":"","delivery":"dashboard","instructions":"","warnings":[]}`, "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	now := time.Date(2026, 10, 21, 14, 30, 0, 0, time.FixedZone("PDT", -7*3600))
	d, err := c.DraftRule(context.Background(), RuleDraftFacts{
		Text:    "  tell me about people at the loading dock after 6pm ",
		Cameras: []DraftCamera{{Name: "Loading dock", Location: "Warehouse north"}, {Name: "Front door"}},
		Kinds:   []string{"Person", "Vehicle"}, Severities: []string{"Critical", "Warning", "Info"},
		TimeProfiles: []string{"Business hours"}, Now: now,
	})
	if err != nil || d.Name != "Dock" || d.CameraNames[0] != "Loading dock" {
		t.Fatalf("%+v %v", d, err)
	}
	req := (*got)[0]
	msgs, _ := json.Marshal(req.body["messages"])
	for _, want := range []string{"Server local time: Wed Oct 21 2026 14:30 PDT -07:00", "- Loading dock (location: Warehouse north)", `- Front door\n`,
		"Alarm kinds: Person, Vehicle", "Severities: Critical, Warning, Info", "Time profiles: Business hours",
		"Request: tell me about people at the loading dock after 6pm"} {
		if !strings.Contains(string(msgs), want) {
			t.Errorf("request lacks %q: %s", want, msgs)
		}
	}
	// Same model and effort as event search.
	if req.body["model"] != TextModel {
		t.Fatalf("model %v", req.body["model"])
	}
	if oc, _ := req.body["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Fatalf("output_config %v", req.body["output_config"])
	}

	if _, err := c.DraftRule(context.Background(), RuleDraftFacts{Text: "  "}); err == nil || len(*got) != 1 {
		t.Fatalf("a blank request reached the model: %v", err)
	}
	bad, _ := fakeAnthropic(t, "Sorry, I can only help with cameras.", "end_turn")
	if _, err := New("sk-test", option.WithBaseURL(bad.URL)).DraftRule(context.Background(), RuleDraftFacts{Text: "hello"}); err == nil {
		t.Fatal("a reply without JSON was accepted")
	}
}
