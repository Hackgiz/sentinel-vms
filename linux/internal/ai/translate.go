package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// CameraRef names a camera the translator may filter on.
type CameraRef struct {
	ID   string
	Name string
}

// FilterSpec is a natural-language query understood as an event filter.
// From/To are Unix seconds (0 = open). Only cameras in the CameraRef list
// the caller passed ever appear in CameraIDs.
type FilterSpec struct {
	CameraIDs []string
	Kinds     []string
	Labels    []string
	From, To  int64
	MinScore  float64
	Threats   []string
	Anomaly   *bool
	Text      string
}

const translateSystem = "You translate a security operator's natural-language question into a filter over a camera " +
	"system's detection-event log. Events have: camera, kind (Motion, Person, Vehicle, Animal), label (the detector's " +
	"class, e.g. person, car, dog), score (0-1 confidence), time, an AI description, tags, threat (none, low, elevated, " +
	"high) and an anomaly flag. Reply with a single JSON object and nothing else, using only the keys that apply: " +
	"{\"cameras\": [<camera ids from the list>], \"kinds\": [..], \"labels\": [..], \"from\": \"<local ISO-8601 time>\", " +
	"\"to\": \"<local ISO-8601 time>\", \"minScore\": <0-1>, \"threats\": [..], \"anomaly\": <true|false>, " +
	"\"text\": \"<words to find in the description/label/tags, e.g. 'red jacket'>\"}. " +
	"Resolve relative times (yesterday, last night, this morning, past hour) from the current time given. " +
	"Use camera ids exactly as listed; omit cameras unless the question names one. Put visual attributes in text, not labels. " +
	"Never invent keys or camera ids."

// TranslateQuery turns a natural-language question into an event filter
// with one model call. Camera ids the model invents are dropped.
func (c *Client) TranslateQuery(ctx context.Context, query string, now time.Time, cameras []CameraRef) (FilterSpec, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return FilterSpec{}, errors.New("type what you're looking for")
	}
	var b strings.Builder
	b.WriteString("Current local time: " + now.Format("Mon Jan 2 2006 3:04 PM -07:00") + "\n\nCameras:\n")
	for _, cam := range cameras {
		b.WriteString("- " + cam.ID + ": " + cam.Name + "\n")
	}
	b.WriteString("\nQuestion: " + query)
	raw, err := c.text(ctx, translateSystem, b.String(), 1000)
	if err != nil {
		return FilterSpec{}, err
	}
	return parseFilterSpec(raw, now, cameras)
}

func parseFilterSpec(raw string, now time.Time, cameras []CameraRef) (FilterSpec, error) {
	obj := firstJSONObject(raw)
	if obj == nil {
		return FilterSpec{}, errors.New("Claude did not return a filter")
	}
	var p struct {
		Cameras  []string `json:"cameras"`
		Kinds    []string `json:"kinds"`
		Labels   []string `json:"labels"`
		From     string   `json:"from"`
		To       string   `json:"to"`
		MinScore float64  `json:"minScore"`
		Threats  []string `json:"threats"`
		Anomaly  *bool    `json:"anomaly"`
		Text     string   `json:"text"`
	}
	if err := json.Unmarshal(obj, &p); err != nil {
		return FilterSpec{}, errors.New("Claude did not return a filter")
	}
	known := map[string]bool{}
	for _, c := range cameras {
		known[c.ID] = true
	}
	f := FilterSpec{Kinds: clean(p.Kinds), Labels: clean(p.Labels), Threats: clean(p.Threats), Anomaly: p.Anomaly, Text: strings.TrimSpace(p.Text)}
	for _, id := range clean(p.Cameras) {
		if known[id] {
			f.CameraIDs = append(f.CameraIDs, id)
		}
	}
	if p.MinScore > 0 && p.MinScore <= 1 {
		f.MinScore = p.MinScore
	}
	f.From, f.To = parseWhen(p.From, now), parseWhen(p.To, now)
	if f.From > 0 && f.To > 0 && f.To <= f.From {
		f.To = 0
	}
	return f, nil
}

func clean(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseWhen reads an ISO-8601 time (zone optional: local to now).
func parseWhen(s string, now time.Time) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t.Unix()
		}
	}
	return 0
}
