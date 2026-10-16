package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestTranslateQuery(t *testing.T) {
	srv, got := fakeAnthropic(t, "```json\n{\"cameras\":[\"cam1\",\"ghost\"],\"kinds\":[\"Person\"],\"from\":\"2026-10-14T18:00:00\",\"to\":\"2026-10-15T06:00:00\",\"minScore\":0.6,\"threats\":[\"high\"],\"anomaly\":true,\"text\":\"red jacket\"}\n```", "end_turn")
	c := New("sk-test", option.WithBaseURL(srv.URL))
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	f, err := c.TranslateQuery(context.Background(), "someone in a red jacket at the door last night", now, []CameraRef{{ID: "cam1", Name: "Door"}, {ID: "cam2", Name: "Drive"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.CameraIDs, ",") != "cam1" || strings.Join(f.Kinds, ",") != "Person" || f.Text != "red jacket" || f.MinScore != 0.6 || f.Anomaly == nil || !*f.Anomaly || strings.Join(f.Threats, ",") != "high" {
		t.Fatalf("%+v", f)
	}
	if f.From != time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC).Unix() || f.To != time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("times %d %d", f.From, f.To)
	}
	req := (*got)[0]
	msgs, _ := json.Marshal(req.body["messages"])
	if req.body["model"] != TextModel || !strings.Contains(string(msgs), "cam2: Drive") || !strings.Contains(string(msgs), "last night") {
		t.Fatalf("request %v", req.body)
	}
	if _, err := c.TranslateQuery(context.Background(), "  ", now, nil); err == nil {
		t.Fatal("empty query accepted")
	}
	bad, _ := fakeAnthropic(t, "I can't do that", "end_turn")
	if _, err := New("sk-test", option.WithBaseURL(bad.URL)).TranslateQuery(context.Background(), "x", now, nil); err == nil {
		t.Fatal("non-JSON reply accepted")
	}
}
