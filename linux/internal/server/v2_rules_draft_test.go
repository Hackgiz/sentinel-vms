package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

// fakeRuleDrafter answers every model call with *reply, counting the calls
// and keeping the last request body.
func fakeRuleDrafter(t *testing.T, reply *string, calls *int, lastBody *string) *httptest.Server {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*calls++
		*lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5", "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}, "content": []any{map[string]any{"type": "text", "text": *reply}}})
	}))
	t.Cleanup(api.Close)
	return api
}

// draftReply is a model reply naming cameras and kinds; days are all seven.
func draftReply(cameras, kinds string) string {
	return `{"name":"Front door after hours","explanation":"Notifies your phone about people at the front door from 18:00 to 07:00.",
		"cameraNames":` + cameras + `,"kinds":` + kinds + `,"minSeverity":"Warning",
		"schedule":{"enabled":true,"from":"18:00","to":"07:00","days":[0,1,2,3,4,5,6]},
		"timeProfileName":"","delivery":"push","instructions":"Check the live view.","warnings":["A rule can't recognize faces."]}`
}

func TestV2DraftRule(t *testing.T) {
	var calls int
	var body string
	reply := draftReply(`["front DOOR","Loading dock"]`, `["Person","Face"]`)
	api := fakeRuleDrafter(t, &reply, &calls, &body)
	f := newScopeFixture(t)
	f.s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	const path = "/api/v2/rules/draft"
	const ask = `{"text":" tell me about people at the front door after 6pm "}`
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.front.ID, Kind: "Person", CreatedAt: f.t0.Unix() + 20, Description: "Man in a hood tries the handle"})

	existing := rules.Rule{Name: "Night", Kinds: []string{"Person"}}
	existing.Normalize()
	if err := f.s.Store.SaveAlarmRule(ctx, &existing); err != nil {
		t.Fatal(err)
	}
	unchanged := func(when string) {
		t.Helper()
		list, err := f.s.Store.AlarmRules(ctx)
		if err != nil || len(list) != 1 || list[0].Enabled {
			t.Fatalf("%s: drafting changed the rules: %+v %v", when, list, err)
		}
	}

	// AI off: the same 409 as the other AI routes, and nothing else happens.
	code, m := v2Call(t, f.h, "POST", path, f.admin, ask)
	if code != 409 || errCode(m) != "conflict" || calls != 0 {
		t.Fatalf("AI off: %d %v (%d calls)", code, m, calls)
	}
	unchanged("AI off")

	_ = f.s.Store.SetSetting(ctx, aiEnabledSetting, "on")
	sealed, _ := f.s.Store.Seal("sk-ant-good")
	_ = f.s.Store.SetSetting(ctx, aiKeySetting, sealed)

	// Drafting needs ChangeSettings, and a bad description never reaches the model.
	if code, _ := v2Call(t, f.h, "POST", path, f.allOp, ask); code != 403 {
		t.Fatalf("operator draft: %d", code)
	}
	for _, bad := range []string{`{"text":"  hi  "}`, `{"text":"` + strings.Repeat("é", 501) + `"}`, `{"text":"people","enabled":true}`, `{}`} {
		if code, m := v2Call(t, f.h, "POST", path, f.admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%.40s: %d %v", bad, code, m)
		}
	}
	if code, _ := v2Call(t, f.h, "POST", path, f.admin, `{"text":"`+strings.Repeat("é", 500)+`"}`); code != 200 {
		t.Fatalf("500 characters: %d", code)
	}
	if calls != 1 {
		t.Fatalf("rejected requests reached the model (%d calls)", calls)
	}

	// A reply that isn't a rule is the model's failure: 502, recorded as the AI error.
	reply = "Sorry, I can only help with cameras."
	if code, m := v2Call(t, f.h, "POST", path, f.admin, ask); code != 502 || errCode(m) != "unavailable" {
		t.Fatalf("malformed reply: %d %v", code, m)
	}
	if st := f.s.aiStatus(); st["lastError"] != "Claude did not return a rule" {
		t.Fatalf("lastError %v", st["lastError"])
	}
	unchanged("malformed reply")

	// The draft: names resolved, the rest left out with warnings, disabled, unsaved.
	reply = draftReply(`["front DOOR","Loading dock"]`, `["Person","Face"]`)
	code, m = v2Call(t, f.h, "POST", path, f.admin, ask)
	if code != 200 {
		t.Fatalf("draft: %d %v", code, m)
	}
	rule := m["rule"].(map[string]any)
	want := map[string]any{"name": "Front door after hours", "enabled": false, "cameraIds": []any{f.front.ID}, "kinds": []any{"Person"},
		"minSeverity": "Warning", "schedule": true, "fromMinute": 1080.0, "toMinute": 420.0, "days": []any{}, "delivery": "push",
		"instructions": "Check the live view."}
	if fmt.Sprint(rule) != fmt.Sprint(want) { // also: no timeProfileId or actions
		t.Fatalf("rule\n got %v\nwant %v", rule, want)
	}
	if m["explanation"] != "Notifies your phone about people at the front door from 18:00 to 07:00." {
		t.Fatalf("explanation %v", m["explanation"])
	}
	if w := fmt.Sprint(m["warnings"]); w != `[A rule can't recognize faces. No camera called "Loading dock". No alarm kind called "Face".]` {
		t.Fatalf("warnings %s", w)
	}
	unchanged("draft")
	if !auditActions(t, f.s, "AI")["Drafted rule"] {
		t.Fatal("the draft was not audited")
	}
	if st := f.s.aiStatus(); st["usedToday"] != 3 || st["lastError"] != "" { // the 500-character draft, the bad reply, this one
		t.Fatalf("status %v", st)
	}

	// The model sees the request, the camera names, the kinds and the clock;
	// never events, alarms, evidence or people.
	for _, want := range []string{"tell me about people at the front door after 6pm", "- Front door", "- Back gate",
		"Alarm kinds: Motion, Person, Vehicle", "Severities: Critical, Warning, Info", "Time profiles: (none)", "Server local time: "} {
		if !strings.Contains(body, want) {
			t.Errorf("the model wasn't told %q: %s", want, body)
		}
	}
	for _, leak := range []string{"Man in a hood", "Person detected", "Disk almost full", "CASE-", "boss"} {
		if strings.Contains(body, leak) {
			t.Errorf("the model was told %q", leak)
		}
	}

	// Only an unknown camera: still a rule, for every camera, with warnings.
	reply = draftReply(`["Loading dock"]`, `["Person"]`)
	code, m = v2Call(t, f.h, "POST", path, f.admin, ask)
	if code != 200 || fmt.Sprint(m["rule"].(map[string]any)["cameraIds"]) != "[]" ||
		fmt.Sprint(m["warnings"]) != `[A rule can't recognize faces. No camera called "Loading dock". The rule applies to every camera.]` {
		t.Fatalf("unknown camera: %d %v", code, m)
	}

	// The daily limit applies.
	_ = f.s.Store.SetSetting(ctx, aiLimitSetting, "0")
	if code, m := v2Call(t, f.h, "POST", path, f.admin, ask); code != 429 || errCode(m) != "too_many_requests" {
		t.Fatalf("over limit: %d %v", code, m)
	}
	unchanged("end")
}

// A custom role may change settings with a camera scope: the model then
// only hears of, and the draft only resolves, cameras the caller sees.
func TestV2DraftRuleCameraScope(t *testing.T) {
	var calls int
	var body string
	reply := draftReply(`["Back gate","Front door"]`, `["Person"]`)
	api := fakeRuleDrafter(t, &reply, &calls, &body)
	f := newScopeFixture(t)
	f.s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	_ = f.s.Store.SetSetting(ctx, aiEnabledSetting, "on")
	sealed, _ := f.s.Store.Seal("sk-ant-good")
	_ = f.s.Store.SetSetting(ctx, aiKeySetting, sealed)
	if _, err := f.s.Store.CreateRole(ctx, "Site manager", "", []string{"viewLive", "changeSettings"}); err != nil {
		t.Fatal(err)
	}
	mgr := v2Token(t, f.s, "mgr", "Site manager")
	u, _ := f.s.Store.UserByName(ctx, "mgr")
	if err := f.s.Store.SetUserScope(ctx, u.ID, store.UserScope{GroupIDs: []string{f.frontG.ID}}); err != nil {
		t.Fatal(err)
	}

	code, m := v2Call(t, f.h, "POST", "/api/v2/rules/draft", mgr, `{"text":"people at the back gate or the front door"}`)
	if code != 200 || fmt.Sprint(m["rule"].(map[string]any)["cameraIds"]) != "["+f.front.ID+"]" ||
		!strings.Contains(fmt.Sprint(m["warnings"]), `No camera called "Back gate".`) {
		t.Fatalf("scoped draft: %d %v", code, m)
	}
	if !strings.Contains(body, "- Front door") || strings.Contains(body, "- Back gate") {
		t.Fatalf("the model should only hear of the caller's cameras: %s", body)
	}
}

func TestV2RuleTemplates(t *testing.T) {
	s := newCompanionServer(t)
	h := s.Handler()
	viewer := v2Token(t, s, "view", "Viewer")
	if code, _ := v2Call(t, h, "GET", "/api/v2/rule-templates", "", ""); code != 401 {
		t.Fatalf("signed out: %d", code)
	}
	code, m := v2Call(t, h, "GET", "/api/v2/rule-templates", viewer, "")
	if code != 200 {
		t.Fatalf("templates: %d %v", code, m)
	}
	list := items(t, m)
	if len(list) != len(rules.Templates()) {
		t.Fatalf("%d templates", len(list))
	}
	categories := map[string]bool{"security": true, "health": true, "operations": true}
	for _, tp := range list {
		if !categories[fmt.Sprint(tp["category"])] || tp["title"] == "" || tp["summary"] == "" {
			t.Errorf("template %v", tp)
		}
		r := tp["rule"].(map[string]any)
		var keys []string
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// RuleInput's required fields, and nothing else: no time profile or actions.
		if strings.Join(keys, ",") != "cameraIds,days,delivery,enabled,fromMinute,instructions,kinds,minSeverity,name,schedule,toMinute" {
			t.Errorf("%v: rule keys %v", tp["id"], keys)
		}
		if fmt.Sprint(r["cameraIds"]) != "[]" || r["enabled"] != true {
			t.Errorf("%v: %v", tp["id"], r)
		}
	}
	first := list[0]["rule"].(map[string]any)
	if list[0]["id"] != "after-hours-people" || fmt.Sprint(first["kinds"]) != "[Person]" || first["schedule"] != true ||
		first["fromMinute"] != 1080.0 || first["toMinute"] != 420.0 || first["delivery"] != "push" {
		t.Fatalf("after-hours-people: %v", list[0])
	}
}
