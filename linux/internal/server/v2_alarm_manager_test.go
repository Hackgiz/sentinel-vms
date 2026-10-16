package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/store"
)

func alarmIDs(t *testing.T, m map[string]any) []string {
	t.Helper()
	var ids []string
	for _, a := range items(t, m) {
		ids = append(ids, a["id"].(string))
	}
	return ids
}

func TestV2AlarmFiltersAndCursor(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	// Six more alarms on the back camera, spaced a minute apart.
	var extra []*store.Alarm
	for i := 0; i < 6; i++ {
		sev := []string{"Critical", "Warning", "Info"}[i%3]
		a, _, err := f.s.Store.RaiseAlarm(ctx, store.AlarmInput{CameraID: f.back.ID, CameraName: f.back.Name, Kind: fmt.Sprintf("Kind%d", i),
			Title: fmt.Sprintf("Gate %d opened", i), Detail: "the gate", Severity: sev, At: f.t0.Add(time.Duration(i+1) * time.Minute), Origin: "test"})
		if err != nil {
			t.Fatal(err)
		}
		extra = append(extra, a)
	}
	_, _ = f.s.Store.SetAlarmState(ctx, extra[0].ID, "Resolved", "", "done")
	_, _ = f.s.Store.SetAlarmState(ctx, extra[1].ID, "Acknowledged", "alice", "ack")
	get := func(tok, query string) (int, map[string]any) {
		return v2Call(t, f.h, "GET", "/api/v2/alarms?"+query, tok, "")
	}

	// state=open (default) leaves out the resolved one; states overrides it.
	if code, m := get(f.admin, ""); code != 200 || len(alarmIDs(t, m)) != 8 { // 2 camera alarms + storage + 6 extra - 1 resolved
		t.Fatalf("open: %d %v", code, len(alarmIDs(t, m)))
	}
	if code, m := get(f.admin, "states=Resolved"); code != 200 || strings.Join(alarmIDs(t, m), ",") != extra[0].ID {
		t.Fatalf("states=Resolved: %d %v", code, m)
	}
	if code, m := get(f.admin, "states=Resolved,Acknowledged&cameras="+f.back.ID); code != 200 || len(alarmIDs(t, m)) != 2 {
		t.Fatalf("two states: %d %v", code, m)
	}
	// severities, q, owner, from/to.
	if code, m := get(f.admin, "state=all&severities=Critical&cameras="+f.back.ID); code != 200 || len(alarmIDs(t, m)) != 2 {
		t.Fatalf("severity: %d %v", code, m)
	}
	if code, m := get(f.admin, "state=all&q=gate+3"); code != 200 || strings.Join(alarmIDs(t, m), ",") != extra[3].ID {
		t.Fatalf("q: %d %v", code, m)
	}
	if code, m := get(f.admin, "state=all&owner=alice"); code != 200 || strings.Join(alarmIDs(t, m), ",") != extra[1].ID {
		t.Fatalf("owner: %d %v", code, m)
	}
	if code, m := get(f.admin, "state=all&owner=unassigned"); code != 200 || len(alarmIDs(t, m)) != 8 { // 9 alarms, one owned by alice
		t.Fatalf("unassigned: %d %v", code, len(alarmIDs(t, m)))
	}
	from, to := f.t0.Add(2*time.Minute).UnixMilli(), f.t0.Add(4*time.Minute).UnixMilli()
	if code, m := get(f.admin, fmt.Sprintf("state=all&from=%d&to=%d", from, to)); code != 200 || strings.Join(alarmIDs(t, m), ",") != extra[2].ID+","+extra[1].ID {
		t.Fatalf("from/to: %d %v", code, m)
	}

	// Keyset paging: newest first, before = nextCursor, no overlap.
	code, p1 := get(f.admin, "state=all&cameras="+f.back.ID+"&limit=4")
	if code != 200 || len(alarmIDs(t, p1)) != 4 || p1["nextCursor"] == nil {
		t.Fatalf("page 1: %d %v", code, p1)
	}
	code, p2 := get(f.admin, "state=all&cameras="+f.back.ID+"&limit=4&before="+url.QueryEscape(p1["nextCursor"].(string)))
	if code != 200 || len(alarmIDs(t, p2)) != 3 || p2["nextCursor"] != nil {
		t.Fatalf("page 2: %d %v", code, p2)
	}
	seen := map[string]bool{}
	for _, id := range append(alarmIDs(t, p1), alarmIDs(t, p2)...) {
		if seen[id] {
			t.Fatalf("alarm %s on both pages", id)
		}
		seen[id] = true
	}

	// Scope: the operator on Front sees only Front's alarm, never the site-wide one.
	if code, m := get(f.op, "state=all"); code != 200 || strings.Join(alarmIDs(t, m), ",") != f.alarm[f.front.ID].ID {
		t.Fatalf("scoped: %d %v", code, m)
	}
	if code, m := get(f.op, "state=all&cameras="+f.back.ID); code != 200 || len(alarmIDs(t, m)) != 0 {
		t.Fatalf("scoped to out-of-scope camera: %d %v", code, m)
	}

	for _, bad := range []string{"states=Bogus", "state=maybe", "limit=0", "limit=501", "before=%21%21", "from=abc", "to=-1"} {
		if code, m := get(f.admin, bad); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}

	// Counts follow the other filters and the scope.
	code, m := v2Call(t, f.h, "GET", "/api/v2/alarms/counts?cameras="+f.back.ID, f.admin, "")
	c, _ := m["counts"].(map[string]any)
	if code != 200 || c["New"] != float64(5) || c["Acknowledged"] != float64(1) || c["Resolved"] != float64(1) || c["Snoozed"] != float64(0) {
		t.Fatalf("counts: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "GET", "/api/v2/alarms/counts", f.op, "")
	c, _ = m["counts"].(map[string]any)
	if code != 200 || c["New"] != float64(1) {
		t.Fatalf("scoped counts: %d %v", code, m)
	}
}

func TestV2AlarmAssignAndSeverity(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	viewer := v2Token(t, f.s, "view", "Viewer")
	a := f.alarm[f.front.ID]
	path := "/api/v2/alarms/" + a.ID

	if code, _ := v2Call(t, f.h, "POST", path+"/assign", viewer, `{"owner":"op"}`); code != 403 {
		t.Fatalf("viewer assign: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", path+"/severity", viewer, `{"severity":"Info"}`); code != 403 {
		t.Fatalf("viewer severity: %d", code)
	}
	if code, m := v2Call(t, f.h, "POST", path+"/assign", f.admin, `{"owner":"nobody"}`); code != 400 {
		t.Fatalf("unknown owner: %d %v", code, m)
	}
	code, m := v2Call(t, f.h, "POST", path+"/assign", f.admin, `{"owner":"op"}`)
	log, _ := m["responseLog"].([]any)
	if code != 200 || m["owner"] != "op" || len(log) < 2 || !strings.Contains(log[len(log)-1].(string), "Assigned to op by boss") {
		t.Fatalf("assign: %d %v", code, m)
	}
	// The change is audited and persisted.
	if got, _ := f.s.Store.Alarm(ctx, a.ID); got.Owner != "op" {
		t.Fatalf("stored owner %q", got.Owner)
	}
	if entries, _ := f.s.Store.AuditLog(ctx, 20); !strings.Contains(fmt.Sprint(entries), "Assigned alarm") {
		t.Fatalf("assign not audited: %v", entries)
	}
	if code, m = v2Call(t, f.h, "POST", path+"/assign", f.admin, `{"owner":""}`); code != 200 || m["owner"] != "Unassigned" {
		t.Fatalf("unassign: %d %v", code, m)
	}
	// Out of scope: the operator on Front can't touch Back's alarm.
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/alarms/"+f.alarm[f.back.ID].ID+"/assign", f.op, `{"owner":"op"}`); code != 404 {
		t.Fatalf("out-of-scope assign: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/alarms/nope/assign", f.admin, `{"owner":"op"}`); code != 404 {
		t.Fatalf("missing alarm: %d", code)
	}

	if code, m := v2Call(t, f.h, "POST", path+"/severity", f.admin, `{"severity":"Urgent"}`); code != 400 {
		t.Fatalf("bad severity: %d %v", code, m)
	}
	if code, m := v2Call(t, f.h, "POST", path+"/severity", f.op, `{"severity":"Info"}`); code != 200 || m["severity"] != "Info" {
		t.Fatalf("severity: %d %v", code, m)
	}
}

// fakeClaude answers triage and report requests; calls counts requests.
func fakeClaude(t *testing.T, calls *int, lastBody *string) *httptest.Server {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/models") {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5","type":"model","display_name":"Haiku","created_at":"2025-10-01T00:00:00Z"}],"has_more":false,"first_id":"a","last_id":"a"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		*calls++
		*lastBody = string(b)
		text := `{"summary":"A person lingered at the front door.","suggestedSeverity":"critical","rationale":"Loitering at night."}`
		if strings.Contains(string(b), "Chronology") {
			text = "## Summary\nA person was seen at the front door."
		}
		resp := map[string]any{"id": "m", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5", "stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}, "content": []any{map[string]any{"type": "text", "text": text}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(api.Close)
	return api
}

func TestV2AlarmTriage(t *testing.T) {
	var calls int
	var body string
	api := fakeClaude(t, &calls, &body)
	f := newScopeFixture(t)
	f.s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	a := f.alarm[f.front.ID]
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.front.ID, Kind: "Person", Label: "person", AlarmID: a.ID, CreatedAt: f.t0.Unix() + 11, Description: "Man in a hood tries the handle", Threat: "elevated"})
	path := "/api/v2/alarms/" + a.ID + "/triage"
	viewer := v2Token(t, f.s, "view", "Viewer")

	// AI off: the same shape events/ask uses (409 conflict), and no model call.
	code, m := v2Call(t, f.h, "POST", path, f.admin, "")
	_, ask := v2Call(t, f.h, "POST", "/api/v2/events/ask", f.admin, `{"query":"x"}`)
	if code != 409 || errCode(m) != "conflict" || fmt.Sprint(m) != fmt.Sprint(ask) || calls != 0 {
		t.Fatalf("AI off: %d %v vs %v", code, m, ask)
	}

	_ = f.s.Store.SetSetting(ctx, aiEnabledSetting, "on")
	sealed, _ := f.s.Store.Seal("sk-ant-good")
	_ = f.s.Store.SetSetting(ctx, aiKeySetting, sealed)

	if code, _ := v2Call(t, f.h, "POST", path, viewer, ""); code != 403 {
		t.Fatalf("viewer triage: %d", code)
	}
	if code, _ := v2Call(t, f.h, "POST", "/api/v2/alarms/"+f.alarm[f.back.ID].ID+"/triage", f.op, ""); code != 404 {
		t.Fatalf("out-of-scope triage: %d", code)
	}
	if calls != 0 {
		t.Fatalf("rejected requests reached the model (%d calls)", calls)
	}

	code, m = v2Call(t, f.h, "POST", path, f.op, "")
	if code != 200 || m["summary"] != "A person lingered at the front door." || m["suggestedSeverity"] != "Critical" || m["rationale"] != "Loitering at night." {
		t.Fatalf("triage: %d %v", code, m)
	}
	if !strings.Contains(body, "Man in a hood") || !strings.Contains(body, "Person detected") {
		t.Fatalf("model should see the alarm and its events: %s", body)
	}
	// Advice only: the alarm is untouched.
	if got, _ := f.s.Store.Alarm(ctx, a.ID); got.Severity != "High" {
		t.Fatalf("triage changed the severity to %q", got.Severity)
	}
	if st := f.s.aiStatus(); st["usedToday"] != 1 { // the AI-off attempt took no budget
		t.Fatalf("usedToday = %v", st["usedToday"])
	}

	// The daily limit applies.
	_ = f.s.Store.SetSetting(ctx, aiLimitSetting, "0")
	if code, m := v2Call(t, f.h, "POST", path, f.admin, ""); code != 429 || errCode(m) != "too_many_requests" {
		t.Fatalf("over limit: %d %v", code, m)
	}
}

func TestV2IncidentsCRUDScopeAndReport(t *testing.T) {
	var calls int
	var body string
	api := fakeClaude(t, &calls, &body)
	f := newScopeFixture(t)
	f.s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	viewer := v2Token(t, f.s, "view", "Viewer")
	call := func(tok, method, path, body string) (int, map[string]any) { return v2Call(t, f.h, method, path, tok, body) }

	// Viewers read but never write.
	for _, w := range []struct{ method, path, body string }{
		{"POST", "/api/v2/incidents", `{"title":"x"}`},
		{"PATCH", "/api/v2/incidents/x", `{"status":"closed"}`},
		{"POST", "/api/v2/incidents/x/items", `{"kind":"note","note":"n"}`},
		{"DELETE", "/api/v2/incidents/x/items/y", ""},
		{"POST", "/api/v2/incidents/x/report", ""},
	} {
		if code, _ := call(viewer, w.method, w.path, w.body); code != 403 {
			t.Errorf("viewer %s %s = %d, want 403", w.method, w.path, code)
		}
	}
	if code, m := call(viewer, "GET", "/api/v2/incidents", ""); code != 200 || len(items(t, m)) != 0 {
		t.Fatalf("viewer list: %d %v", code, m)
	}

	// Create + validate.
	if code, _ := call(f.admin, "POST", "/api/v2/incidents", `{"title":" "}`); code != 400 {
		t.Fatalf("empty title: %d", code)
	}
	if code, _ := call(f.admin, "POST", "/api/v2/incidents", `{"title":"t","severity":"urgent"}`); code != 400 {
		t.Fatalf("bad severity: %d", code)
	}
	code, inc := call(f.admin, "POST", "/api/v2/incidents", `{"title":"Break-in attempt","severity":"high"}`)
	if code != 201 || inc["status"] != "open" || inc["severity"] != "high" || inc["createdBy"] != "boss" {
		t.Fatalf("create: %d %v", code, inc)
	}
	id := inc["id"].(string)
	base := "/api/v2/incidents/" + id

	// Items: a note, the front and back alarms, an event and an evidence case.
	front, back := f.alarm[f.front.ID], f.alarm[f.back.ID]
	for _, in := range []string{
		`{"kind":"note","note":"Caller reported noise"}`,
		`{"kind":"alarm","refId":"` + front.ID + `"}`,
		`{"kind":"alarm","refId":"` + back.ID + `"}`,
		`{"kind":"event","refId":"` + f.event[f.back.ID].ID + `"}`,
		`{"kind":"evidence","refId":"` + f.evidence[f.front.ID].ID + `"}`,
	} {
		if code, m := call(f.admin, "POST", base+"/items", in); code != 201 {
			t.Fatalf("add %s: %d %v", in, code, m)
		}
	}
	if code, m := call(f.admin, "POST", base+"/items", `{"kind":"alarm","refId":"`+front.ID+`"}`); code != 409 {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	for _, bad := range []string{`{"kind":"photo"}`, `{"kind":"note"}`, `{"kind":"alarm"}`, `{"kind":"alarm","refId":"nope"}`} {
		if code, _ := call(f.admin, "POST", base+"/items", bad); code != 400 && code != 404 {
			t.Errorf("add %s: %d", bad, code)
		}
	}

	// The operator on Front can't add Back's alarm...
	if code, _ := call(f.op, "POST", base+"/items", `{"kind":"alarm","refId":"`+f.alarm[f.back.ID].ID+`"}`); code != 409 && code != 404 {
		t.Fatalf("scoped add: %d", code)
	}
	if code, _ := call(f.op, "POST", base+"/items", `{"kind":"event","refId":"`+f.event[f.back.ID].ID+`"}`); code != 404 {
		t.Fatalf("scoped add of an out-of-scope event: %d", code)
	}
	// ...and sees Back's items as restricted, without losing the incident.
	code, d := call(f.op, "GET", base, "")
	its, _ := d["items"].([]any)
	if code != 200 || len(its) != 5 {
		t.Fatalf("scoped detail: %d %v", code, d)
	}
	var restricted, resolved int
	for _, raw := range its {
		it := raw.(map[string]any)
		if it["restricted"] == true {
			restricted++
			if it["alarm"] != nil || it["event"] != nil || it["refId"] != nil {
				t.Fatalf("restricted item leaks: %v", it)
			}
		} else if it["alarm"] != nil || it["evidence"] != nil || it["kind"] == "note" {
			resolved++
		}
	}
	if restricted != 2 || resolved != 3 {
		t.Fatalf("restricted %d resolved %d: %v", restricted, resolved, its)
	}
	// The admin sees everything, in chronological order.
	code, d = call(f.admin, "GET", base, "")
	its, _ = d["items"].([]any)
	var last float64
	for _, raw := range its {
		it := raw.(map[string]any)
		if it["restricted"] != nil || it["at"].(float64) < last {
			t.Fatalf("admin items: %v", its)
		}
		last = it["at"].(float64)
	}
	if code != 200 || d["incident"].(map[string]any)["itemCount"] != float64(5) {
		t.Fatalf("admin detail: %d %v", code, d)
	}

	// Status transitions are validated and audited.
	if code, _ := call(f.admin, "PATCH", base, `{"status":"done"}`); code != 400 {
		t.Fatalf("bad status: %d", code)
	}
	if code, m := call(f.op, "PATCH", base, `{"status":"investigating","title":"Break-in"}`); code != 200 || m["status"] != "investigating" || m["title"] != "Break-in" {
		t.Fatalf("patch: %d %v", code, m)
	}
	if entries, _ := f.s.Store.AuditLog(ctx, 50); !strings.Contains(fmt.Sprint(entries), "open → investigating") {
		t.Fatalf("transition not audited: %v", entries)
	}
	if code, m := call(f.admin, "PATCH", "/api/v2/incidents/nope", `{"status":"closed"}`); code != 404 {
		t.Fatalf("patch missing: %d %v", code, m)
	}

	// List filters and cursor.
	_, _ = call(f.admin, "POST", "/api/v2/incidents", `{"title":"Second"}`)
	if code, m := call(f.admin, "GET", "/api/v2/incidents?status=investigating", ""); code != 200 || len(items(t, m)) != 1 {
		t.Fatalf("status filter: %d %v", code, m)
	}
	if code, m := call(f.admin, "GET", "/api/v2/incidents?q=second", ""); code != 200 || len(items(t, m)) != 1 {
		t.Fatalf("q filter: %d %v", code, m)
	}
	code, p1 := call(f.admin, "GET", "/api/v2/incidents?limit=1", "")
	if code != 200 || p1["nextCursor"] == nil {
		t.Fatalf("page 1: %d %v", code, p1)
	}
	if code, p2 := call(f.admin, "GET", "/api/v2/incidents?limit=1&before="+url.QueryEscape(p1["nextCursor"].(string)), ""); code != 200 || len(items(t, p2)) != 1 || p2["nextCursor"] != nil {
		t.Fatalf("page 2: %d %v", code, p2)
	}
	if code, _ := call(f.admin, "GET", "/api/v2/incidents?before=junk", ""); code != 400 {
		t.Fatalf("bad cursor: %d", code)
	}

	// Report: AI off → the events/ask shape; on → markdown lands in the summary.
	if code, m := call(f.admin, "POST", base+"/report", ""); code != 409 || errCode(m) != "conflict" || calls != 0 {
		t.Fatalf("report AI off: %d %v", code, m)
	}
	_ = f.s.Store.SetSetting(ctx, aiEnabledSetting, "on")
	sealed, _ := f.s.Store.Seal("sk-ant-good")
	_ = f.s.Store.SetSetting(ctx, aiKeySetting, sealed)
	code, m := call(f.op, "POST", base+"/report", "")
	if code != 200 || !strings.HasPrefix(m["summary"].(string), "## Summary") {
		t.Fatalf("report: %d %v", code, m)
	}
	// The scoped operator's report never shows the model Back's items.
	if !strings.Contains(body, "Caller reported noise") || strings.Contains(body, f.back.Name) {
		t.Fatalf("report corpus: %s", body)
	}
	if got, _ := f.s.Store.Incident(ctx, id); !strings.HasPrefix(got.Summary, "## Summary") {
		t.Fatalf("summary not stored: %q", got.Summary)
	}
	_ = f.s.Store.SetSetting(ctx, aiLimitSetting, "0")
	if code, m := call(f.admin, "POST", base+"/report", ""); code != 429 {
		t.Fatalf("report over limit: %d %v", code, m)
	}

	// Removing an item.
	its, _ = d["items"].([]any)
	itemID := its[0].(map[string]any)["id"].(string)
	if code, _ := call(f.op, "DELETE", base+"/items/"+itemID, ""); code != 204 {
		t.Fatalf("remove item: %d", code)
	}
	if code, _ := call(f.op, "DELETE", base+"/items/"+itemID, ""); code != 404 {
		t.Fatalf("remove again: %d", code)
	}
}
