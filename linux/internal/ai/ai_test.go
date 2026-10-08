package ai

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
)

type captured struct {
	header http.Header
	body   map[string]any
}

func fakeAnthropic(t *testing.T, reply string, stop string) (*httptest.Server, *[]captured) {
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got = append(got, captured{r.Header.Clone(), m})
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": m["model"],
			"content": []any{map[string]any{"type": "text", "text": reply}}, "stop_reason": stop,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 10}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestAnalyzeSceneRequestAndParse(t *testing.T) {
	srv, got := fakeAnthropic(t, "Sure: ```json\n{\"description\":\"Person in a red jacket carries a box to the door.\",\"threat\":\"low\",\"anomaly\":false,\"reason\":\"routine delivery\",\"tags\":[\"person\",\"package\"]}\n```", "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	a, err := c.AnalyzeScene(context.Background(), [][]byte{{0xFF, 0xD8, 1}, {0xFF, 0xD8, 2}}, "Person")
	if err != nil {
		t.Fatal(err)
	}
	if a.Description != "Person in a red jacket carries a box to the door." || a.Threat != "low" || len(a.Tags) != 2 {
		t.Fatalf("%+v", a)
	}
	req := (*got)[0]
	if req.body["model"] != VisionModel || req.header.Get("X-Api-Key") != "sk-test" {
		t.Fatalf("model/key: %v %q", req.body["model"], req.header.Get("X-Api-Key"))
	}
	content := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["type"] != "image" || content[2].(map[string]any)["type"] != "text" {
		t.Fatalf("content blocks %v", content)
	}
}

func TestSearchUsesLowEffortAndDefaultFallback(t *testing.T) {
	srv, got := fakeAnthropic(t, `{"answer":"One person in red at the front door.","matches":[1, "0", 99]}`, "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	events := []EventLine{
		{ID: "a", Time: time.Now(), Camera: "Door", Kind: "Person", Description: "Person in red"},
		{ID: "b", Time: time.Now(), Camera: "Drive", Kind: "Vehicle"},
	}
	answer, ids, err := c.Search(context.Background(), "someone in red", events, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if answer != "One person in red at the front door." || strings.Join(ids, ",") != "b,a" {
		t.Fatalf("answer %q ids %v", answer, ids)
	}
	req := (*got)[0]
	if req.body["model"] != TextModel || req.body["fallbacks"] != "default" {
		t.Fatalf("model/fallbacks: %v %v", req.body["model"], req.body["fallbacks"])
	}
	if oc, _ := req.body["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Fatalf("output_config %v", req.body["output_config"])
	}
	if !strings.Contains(req.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("beta header %q", req.header.Get("Anthropic-Beta"))
	}
	if _, ok := req.body["thinking"]; ok {
		t.Fatal("thinking must be left unset on Sonnet 5.5")
	}
}

func TestRefusalIsNotAnAnswer(t *testing.T) {
	srv, _ := fakeAnthropic(t, "partial text", "refusal")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	if _, err := c.Digest(context.Background(), "today", []EventLine{{ID: "a", Time: time.Now(), Camera: "Door", Kind: "Person"}}); err != ErrRefused {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}
