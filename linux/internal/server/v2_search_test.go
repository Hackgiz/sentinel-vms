package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/store"
)

func searchIDs(m map[string]any) string {
	var ids []string
	raw, _ := m["items"].([]any)
	for _, v := range raw {
		ids = append(ids, v.(map[string]any)["cameraId"].(string))
	}
	return strings.Join(ids, ",")
}

func TestV2EventSearchScopeAndPaging(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	// Extra, searchable events: the fixture has one Person per camera.
	for i := 0; i < 5; i++ {
		_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.back.ID, Kind: "Vehicle", Label: "car", Score: 0.5, CreatedAt: f.t0.Unix() + int64(100+i), Description: "Blue van"})
	}
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.front.ID, Kind: "Vehicle", Label: "car", Score: 0.5, CreatedAt: f.t0.Unix() + 200, Description: "Blue van"})

	// Admin sees both cameras; the operator scoped to Front only theirs.
	code, m := v2Call(t, f.h, "GET", "/api/v2/events/search?q=blue%20van", f.admin, "")
	if code != 200 || len(m["items"].([]any)) != 6 {
		t.Fatalf("admin: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/search?q=blue+van", f.op, "")
	if code != 200 || searchIDs(m) != f.front.ID {
		t.Fatalf("scoped operator: %d %v", code, m)
	}
	// Asking for an out-of-scope camera yields nothing, not an error or a leak.
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/search?cameras="+f.back.ID, f.op, "")
	if code != 200 || len(m["items"].([]any)) != 0 {
		t.Fatalf("out-of-scope camera: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/search?cameras="+f.back.ID+","+f.front.ID, f.op, "")
	if code != 200 || searchIDs(m) != f.front.ID+","+f.front.ID {
		t.Fatalf("mixed cameras: %d %v", code, m)
	}

	// Paging with limit and cursor.
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/search?kinds=Vehicle&limit=4", f.admin, "")
	if code != 200 || len(m["items"].([]any)) != 4 || m["nextCursor"] == nil {
		t.Fatalf("page 1: %d %v", code, m)
	}
	code, m2 := v2Call(t, f.h, "GET", "/api/v2/events/search?kinds=Vehicle&limit=4&before="+url.QueryEscape(m["nextCursor"].(string)), f.admin, "")
	if code != 200 || len(m2["items"].([]any)) != 2 || m2["nextCursor"] != nil {
		t.Fatalf("page 2: %d %v", code, m2)
	}

	for _, bad := range []string{"limit=0", "limit=501", "limit=x", "from=abc", "minScore=2", "anomaly=maybe", "before=junk"} {
		if code, m := v2Call(t, f.h, "GET", "/api/v2/events/search?"+bad, f.admin, ""); code != 400 || errCode(m) != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}
	if code, _ := v2Call(t, f.h, "GET", "/api/v2/events/search", "", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestV2EventFacetsScope(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.back.ID, Kind: "Vehicle", Label: "car", CreatedAt: f.t0.Unix() + 5})
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.front.ID, Kind: "Animal", Label: "dog", CreatedAt: f.t0.Unix() + 6})
	kinds := func(m map[string]any) string {
		var out []string
		for _, v := range m["kinds"].([]any) {
			c := v.(map[string]any)
			out = append(out, fmt.Sprintf("%s=%v", c["value"], c["count"]))
		}
		return strings.Join(out, ",")
	}
	code, m := v2Call(t, f.h, "GET", "/api/v2/events/facets", f.admin, "")
	if code != 200 || !strings.Contains(kinds(m), "Person=2") || !strings.Contains(kinds(m), "Vehicle=1") || m["aiEnabled"] != false {
		t.Fatalf("admin: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/facets", f.op, "")
	if code != 200 || strings.Contains(kinds(m), "Vehicle") || !strings.Contains(kinds(m), "Person=1") || !strings.Contains(kinds(m), "Animal=1") {
		t.Fatalf("scoped: %d %v", code, m)
	}
	code, m = v2Call(t, f.h, "GET", "/api/v2/events/facets?cameras="+f.back.ID, f.op, "")
	if code != 200 || kinds(m) != "" {
		t.Fatalf("out-of-scope camera: %d %v", code, m)
	}
}

func TestV2EventAsk(t *testing.T) {
	var lastBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/models") {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5","type":"model","display_name":"Haiku","created_at":"2025-10-01T00:00:00Z"}],"has_more":false,"first_id":"a","last_id":"a"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},
			"content":[{"type":"text","text":"{\"kinds\":[\"Person\"],\"text\":\"red jacket\"}"}]}`)
	}))
	defer api.Close()

	f := newScopeFixture(t)
	f.s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.front.ID, Kind: "Person", Label: "person", CreatedAt: f.t0.Unix() + 30, Description: "Woman in a red jacket"})
	_ = f.s.Store.AddEvent(ctx, &store.Event{CameraID: f.back.ID, Kind: "Person", Label: "person", CreatedAt: f.t0.Unix() + 31, Description: "Man in a red jacket"})

	ask := func(tok, body string) (int, map[string]any) {
		return v2Call(t, f.h, "POST", "/api/v2/events/ask", tok, body)
	}
	if code, m := ask(f.admin, `{"query":"red jacket"}`); code != 409 {
		t.Fatalf("AI off: %d %v", code, m)
	}
	_ = f.s.Store.SetSetting(ctx, aiEnabledSetting, "on")
	sealed, _ := f.s.Store.Seal("sk-ant-good")
	_ = f.s.Store.SetSetting(ctx, aiKeySetting, sealed)

	if code, m := ask(f.admin, `{"query":"  "}`); code != 400 {
		t.Fatalf("empty: %d %v", code, m)
	}
	code, m := ask(f.admin, `{"query":"someone in a red jacket"}`)
	flt, _ := m["filter"].(map[string]any)
	if code != 200 || flt["q"] != "red jacket" || len(m["items"].([]any)) != 2 {
		t.Fatalf("admin ask: %d %v", code, m)
	}
	// The scoped operator is only offered, and only gets, their cameras.
	code, m = ask(f.op, `{"query":"someone in a red jacket"}`)
	if code != 200 || searchIDs(m) != f.front.ID {
		t.Fatalf("scoped ask: %d %v", code, m)
	}
	if strings.Contains(lastBody, f.back.ID) || !strings.Contains(lastBody, f.front.ID) {
		t.Fatalf("model was offered out-of-scope cameras: %s", lastBody)
	}

	// The daily limit applies.
	_ = f.s.Store.SetSetting(ctx, aiLimitSetting, "0")
	if code, m := ask(f.admin, `{"query":"red jacket"}`); code != 429 {
		t.Fatalf("over limit: %d %v", code, m)
	}
}
