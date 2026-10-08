package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/store"
)

func TestAISettingsAndSearch(t *testing.T) {
	var lastBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != "sk-ant-good" {
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/models") {
			io.WriteString(w, `{"data":[{"id":"claude-haiku-4-5","type":"model","display_name":"Haiku","created_at":"2025-10-01T00:00:00Z"}],"has_more":false,"first_id":"a","last_id":"a"}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1},
			"content":[{"type":"text","text":"{\"answer\":\"A person in a red jacket at the Door camera.\",\"matches\":[0]}"}]}`)
	}))
	defer api.Close()

	s := newCompanionServer(t)
	s.AIOptions = []option.RequestOption{option.WithBaseURL(api.URL)}
	ctx := context.Background()
	admin, _ := s.Store.CreateUser(ctx, "admin", "Admin", "x")
	token, _ := s.Store.CreateSession(ctx, admin.ID, time.Hour)
	h := s.Handler()
	req := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := req("POST", "/api/v1/ai/search", `{"query":"red jacket"}`); w.Code != http.StatusConflict {
		t.Fatalf("search with AI off = %d", w.Code)
	}
	if w := req("PUT", "/api/v1/ai", `{"apiKey":"sk-ant-bad","enabled":true}`); w.Code != 400 || !strings.Contains(w.Body.String(), "rejected the API key") {
		t.Fatalf("bad key: %d %s", w.Code, w.Body)
	}
	if w := req("PUT", "/api/v1/ai", `{"apiKey":"sk-ant-good","enabled":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("good key: %d %s", w.Code, w.Body)
	}
	if raw := s.Store.Setting(ctx, aiKeySetting, ""); raw == "" || strings.Contains(raw, "sk-ant-good") {
		t.Fatalf("API key must be stored sealed, got %q", raw)
	}

	c, _ := s.Store.SaveCamera(ctx, "", store.CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.9/s"})
	_ = s.Store.AddEvent(ctx, &store.Event{CameraID: c.ID, Kind: "Person", Score: 0.9, CreatedAt: time.Now().Add(-time.Hour).Unix(), Description: "Person in a red jacket walks to the door."})
	_ = s.Store.AddEvent(ctx, &store.Event{CameraID: c.ID, Kind: "Motion", Score: 0.1, CreatedAt: time.Now().Add(-time.Hour).Unix()})

	w := req("POST", "/api/v1/ai/search", `{"query":"red jacket"}`)
	var res struct {
		Answer   string        `json:"answer"`
		Events   []store.Event `json:"events"`
		Searched int           `json:"searched"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != 200 || len(res.Events) != 1 || res.Events[0].Kind != "Person" || res.Searched != 1 {
		t.Fatalf("search: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(lastBody, "Person in a red jacket") || strings.Contains(lastBody, "· Motion") {
		t.Fatalf("corpus should hold detection events (not plain motion): %s", lastBody)
	}
}
