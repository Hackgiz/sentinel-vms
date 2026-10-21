package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RuleDraftFacts is everything the rule drafter sees (WAVE14-AI-ASSIST.md §1):
// the admin's words, the names a rule may use and the server's clock. No
// frames, events, plates or people.
type RuleDraftFacts struct {
	Text         string
	Cameras      []DraftCamera
	Kinds        []string // alarm kinds (rules.Kinds)
	Severities   []string // most severe first (rules.Severities)
	TimeProfiles []string // time profile names
	Now          time.Time
}

// DraftCamera is a camera as the drafter sees it.
type DraftCamera struct{ Name, Location string }

// RuleDraft is the model's reply: a rule with camera and time profile names
// where a saved rule has ids. The server resolves and validates it
// (rules.ResolveDraft); nothing here is trusted.
type RuleDraft struct {
	Name            string        `json:"name"`
	Explanation     string        `json:"explanation"`
	CameraNames     []string      `json:"cameraNames"`
	Kinds           []string      `json:"kinds"`
	MinSeverity     string        `json:"minSeverity"`
	Schedule        DraftSchedule `json:"schedule"`
	TimeProfileName string        `json:"timeProfileName"`
	Delivery        string        `json:"delivery"`
	Instructions    string        `json:"instructions"`
	Warnings        []string      `json:"warnings"`
}

// DraftSchedule is the drafted window in local "HH:MM" times, on days
// numbered 0 = Sunday … 6 = Saturday.
type DraftSchedule struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
	Days    []int  `json:"days"`
}

// maxDraftWarnings caps the model's own warnings; the server adds its own.
const maxDraftWarnings = 5

const ruleDraftSystem = "You draft one alarm rule for a security camera system from an admin's plain-English request. " +
	"Cameras raise alarms on their own. A rule decides which of those alarms the operator is told about: on which cameras, " +
	"of which kinds, from which severity up, during which hours, whether a phone notification is sent, and what the operator should do. " +
	"Reply with a single JSON object and nothing else:\n" +
	"{\"name\": \"<short name, at most 60 characters>\", " +
	"\"explanation\": \"<one plain-English sentence on what the rule will do>\", " +
	"\"cameraNames\": [<camera names exactly as listed; empty for every camera>], " +
	"\"kinds\": [<alarm kinds exactly as listed; empty for every kind>], " +
	"\"minSeverity\": \"Info\"|\"Warning\"|\"Critical\", " +
	"\"schedule\": {\"enabled\": <true only for a time window>, \"from\": \"HH:MM\", \"to\": \"HH:MM\", \"days\": [<0 = Sunday … 6 = Saturday>]}, " +
	"\"timeProfileName\": \"<a time profile exactly as listed, or empty>\", " +
	"\"delivery\": \"push\"|\"dashboard\", " +
	"\"instructions\": \"<one or two sentences on what the operator should check and do, or empty>\", " +
	"\"warnings\": [<short notes on anything asked for that the rule cannot do>]}\n" +
	"Match places in the request to cameras by name or location, but return camera names. " +
	"Times are 24-hour server local times. A window that ends before it starts runs overnight, so \"after 6pm\" is 18:00 to 07:00 " +
	"unless the request says when it ends. List days only when the request names them; otherwise list all seven. " +
	"minSeverity is Warning unless the request says otherwise. Use push only when the request asks for a phone notification " +
	"or the urgency is clear; otherwise dashboard. A rule cannot watch part of a camera's view, read license plates or recognize " +
	"faces or people: leave that out and say so in warnings. Never invent camera names, kinds or time profiles."

// DraftRule turns a plain-English request into a rule draft with one model
// call. The caller resolves and validates the draft before anyone sees it.
func (c *Client) DraftRule(ctx context.Context, f RuleDraftFacts) (RuleDraft, error) {
	text := strings.TrimSpace(f.Text)
	if text == "" {
		return RuleDraft{}, errors.New("describe the rule you want")
	}
	var b strings.Builder
	b.WriteString("Server local time: " + f.Now.Format("Mon Jan 2 2006 15:04 MST -07:00") + "\n\nCameras:\n")
	if len(f.Cameras) == 0 {
		b.WriteString("(none)\n")
	}
	for _, cam := range f.Cameras {
		b.WriteString("- " + cam.Name)
		if cam.Location != "" {
			b.WriteString(" (location: " + cam.Location + ")")
		}
		b.WriteString("\n")
	}
	profiles := "(none)"
	if len(f.TimeProfiles) > 0 {
		profiles = strings.Join(f.TimeProfiles, ", ")
	}
	b.WriteString("\nAlarm kinds: " + strings.Join(f.Kinds, ", ") + "\nSeverities: " + strings.Join(f.Severities, ", ") +
		"\nTime profiles: " + profiles + "\n\nRequest: " + text)
	// Room to spare: the text model thinks adaptively even at low effort, and
	// a cut-off reply is wasted budget.
	raw, err := c.text(ctx, ruleDraftSystem, b.String(), 4000)
	if err != nil {
		return RuleDraft{}, err
	}
	return ParseRuleDraft(raw)
}

var errNoRuleDraft = errors.New("Claude did not return a rule")

// ParseRuleDraft reads the model's reply: one JSON object of the RuleDraft
// shape, or an error. It only tidies text; rules.ResolveDraft checks values.
func ParseRuleDraft(raw string) (RuleDraft, error) {
	obj := firstJSONObject(raw)
	if obj == nil {
		return RuleDraft{}, errNoRuleDraft
	}
	var d RuleDraft
	if err := json.Unmarshal(obj, &d); err != nil {
		return RuleDraft{}, errNoRuleDraft
	}
	d.Name, d.Explanation = strings.TrimSpace(d.Name), strings.TrimSpace(d.Explanation)
	d.Instructions = strings.TrimSpace(d.Instructions)
	if d.Warnings = clean(d.Warnings); len(d.Warnings) > maxDraftWarnings {
		d.Warnings = d.Warnings[:maxDraftWarnings]
	}
	return d, nil
}
