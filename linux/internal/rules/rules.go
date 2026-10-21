// Package rules is the alarm rules engine: the Linux counterpart of the Mac's
// NotificationRule (Sources/SentinelCore/WorkflowStore.swift).
//
// How rules combine with the per-camera alarm mode: the camera's alarm mode
// (auto / any motion / people & vehicles / people only) still decides whether
// an alarm is raised at all — rules never create or suppress alarms. Rules
// only act on alarms that exist:
//
//   - every enabled rule that matches an alarm contributes its operator
//     instructions (shown on the dashboard and in the iPhone app's alarm list);
//   - an alarm is pushed to paired iPhones only if at least one enabled rule
//     with Delivery "push" matches it when it is raised.
//
// With zero rules nothing changes: alarms are raised exactly as before, carry
// no instructions, and no pushes are sent.
//
// Matching follows the Mac: camera filter (alarms without a camera, e.g.
// Storage, always pass it), alarm kind ("any" when empty), minimum severity
// (Critical > Warning > Info) and an optional active window in the server's
// local time zone. Linux adds minutes (the Mac uses whole hours) and optional
// days of the week. A window that ends before it starts runs overnight
// (22:00–06:00); its early-morning part belongs to the day it started, so
// "Fri 22:00–06:00" covers Saturday 03:00 but not Friday 03:00.
package rules

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// Delivery values.
const (
	DeliveryDashboard = "dashboard" // Mac: "Center"
	DeliveryPush      = "push"      // Mac: "Desktop + Center" — dashboard + iPhone push
)

// Kinds are the alarm kinds the Linux server raises (store.Alarm.Kind).
var Kinds = []string{
	"Motion", "Person", "Vehicle", "Line crossing", "Intrusion", "Loitering", "Tamper", "Camera Offline", "Storage",
	"audio",     // WAVE8(B): loud or distinctive sounds
	"Watchlist", // WAVE16: a watch-listed person (faces) or plate (LPR) was seen
}

// Severities, most severe first (the Mac's AlertSeverity rank order).
var Severities = []string{"Critical", "Warning", "Info"}

// SeverityRank: lower is more severe. Unknown severities rank as Info.
func SeverityRank(s string) int {
	switch s {
	case "Critical":
		return 0
	case "Warning":
		return 1
	}
	return 2
}

type Rule struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Cameras this rule watches; empty = every camera.
	CameraIDs []string `json:"cameraIDs"`
	// Alarm kinds (see Kinds); empty = any alarm.
	Kinds []string `json:"kinds"`
	// Least severe alarm that still matches: "Info" matches everything.
	MinSeverity string `json:"minSeverity"`
	// Active window, minutes after local midnight [From, To). Only used when
	// Schedule is on; To < From runs overnight.
	Schedule   bool `json:"schedule"`
	FromMinute int  `json:"fromMinute"`
	ToMinute   int  `json:"toMinute"`
	// Days the window starts on, 0 = Sunday … 6 = Saturday; empty = every day.
	Days         []int  `json:"days"`
	Delivery     string `json:"delivery"`
	Instructions string `json:"instructions"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
	// TimeProfileID (Wave 6): outside this time profile the rule doesn't
	// fire. Empty = no profile (today's behavior).
	TimeProfileID string `json:"timeProfileId,omitempty"`
	// Actions run when the rule matches a new alarm, besides the alarm
	// behavior above. Absent = none.
	Actions []Action `json:"actions,omitempty"`

	// profile is the resolved TimeProfileID, attached by the server when it
	// loads its rule cache (SetProfile). Not stored.
	profile *TimeProfile
}

// Action is one thing a rule does when it fires.
type Action struct {
	Type      string `json:"type"` // "webhook"
	WebhookID string `json:"webhookId,omitempty"`
}

// ActionWebhook is the Action.Type that calls a webhook.
const ActionWebhook = "webhook"

// SetProfile attaches the resolved time profile used by Matches (nil = none).
func (r *Rule) SetProfile(p *TimeProfile) { r.profile = p }

// Alarm is what a rule is matched against.
type Alarm struct {
	CameraID string
	Kind     string
	Severity string
	At       time.Time
}

// Instruction is one rule's operator instructions, shaped exactly like the
// Mac companion API's {"rule": …, "text": …} entries.
type Instruction struct {
	Rule string `json:"rule"`
	Text string `json:"text"`
}

// Normalize trims and fills defaults; Validate reports what's wrong.
func (r *Rule) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.Instructions = strings.TrimSpace(r.Instructions)
	if r.MinSeverity == "" {
		r.MinSeverity = "Info"
	}
	if r.Delivery == "" {
		r.Delivery = DeliveryDashboard
	}
	r.CameraIDs = dedupe(r.CameraIDs)
	for i, k := range r.Kinds {
		r.Kinds[i] = CanonicalKind(k) // "linecross" -> "Line crossing" (kinds.go)
	}
	r.Kinds = dedupe(r.Kinds)
	seen := map[int]bool{}
	days := r.Days[:0:0]
	for _, d := range r.Days {
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	sort.Ints(days)
	if len(days) == 7 {
		days = nil // every day
	}
	r.Days = days
	if r.CameraIDs == nil {
		r.CameraIDs = []string{}
	}
	if r.Kinds == nil {
		r.Kinds = []string{}
	}
	if r.Days == nil {
		r.Days = []int{}
	}
}

func (r *Rule) Validate() error {
	switch {
	case r.Name == "":
		return errors.New("give the rule a name")
	case len(r.Name) > 120:
		return errors.New("rule name is too long")
	case len(r.Instructions) > 2000:
		return errors.New("instructions are too long (2,000 characters max)")
	case r.Delivery != DeliveryDashboard && r.Delivery != DeliveryPush:
		return errors.New("delivery must be dashboard or push")
	case SeverityRank(r.MinSeverity) == 2 && r.MinSeverity != "Info":
		return errors.New("unknown severity")
	}
	if len(r.Actions) > 20 {
		return errors.New("too many actions")
	}
	for _, a := range r.Actions {
		if a.Type != ActionWebhook || a.WebhookID == "" {
			return errors.New("an action needs type \"webhook\" and a webhookId")
		}
	}
	for _, k := range r.Kinds {
		if !knownKind(k) {
			return errors.New("unknown alarm kind: " + k)
		}
	}
	for _, d := range r.Days {
		if d < 0 || d > 6 {
			return errors.New("days must be 0 (Sunday) to 6 (Saturday)")
		}
	}
	if r.Schedule {
		if r.FromMinute < 0 || r.FromMinute >= 24*60 || r.ToMinute < 0 || r.ToMinute >= 24*60 {
			return errors.New("schedule times must be between 00:00 and 23:59")
		}
		if r.FromMinute == r.ToMinute {
			return errors.New("the schedule starts and ends at the same time")
		}
	}
	return nil
}

func knownKind(k string) bool {
	for _, v := range Kinds {
		if strings.EqualFold(v, CanonicalKind(k)) {
			return true
		}
	}
	return false
}

// ActiveAt reports whether the rule's schedule covers t (in t's location).
func (r *Rule) ActiveAt(t time.Time) bool {
	day := t.Weekday()
	if r.Schedule && r.FromMinute != r.ToMinute {
		m := t.Hour()*60 + t.Minute()
		from, to := r.FromMinute, r.ToMinute
		switch {
		case from < to:
			if m < from || m >= to {
				return false
			}
		case m >= from: // overnight window, evening part
		case m < to: // overnight window, morning part: belongs to yesterday
			day = (day + 6) % 7
		default:
			return false
		}
	}
	if len(r.Days) == 0 {
		return true
	}
	for _, d := range r.Days {
		if time.Weekday(d) == day {
			return true
		}
	}
	return false
}

// Matches applies every filter. The time is converted to loc (nil = Local).
func (r *Rule) Matches(a Alarm, loc *time.Location) bool {
	if !r.Enabled {
		return false
	}
	if loc == nil {
		loc = time.Local
	}
	if !r.ActiveAt(a.At.In(loc)) {
		return false
	}
	if r.profile != nil && !r.profile.ActiveAt(a.At.In(loc)) {
		return false
	}
	if len(r.CameraIDs) > 0 && a.CameraID != "" && !contains(r.CameraIDs, a.CameraID, false) {
		return false
	}
	if len(r.Kinds) > 0 && !kindListed(r.Kinds, a.Kind) {
		return false
	}
	return SeverityRank(a.Severity) <= SeverityRank(r.MinSeverity)
}

// Instructions collects the instructions of every matching rule, in rule order.
func Instructions(rules []Rule, a Alarm, loc *time.Location) []Instruction {
	out := []Instruction{}
	for i := range rules {
		if rules[i].Instructions != "" && rules[i].Matches(a, loc) {
			out = append(out, Instruction{Rule: rules[i].Name, Text: rules[i].Instructions})
		}
	}
	return out
}

// PushRules returns the names of matching rules that deliver by push; an
// alarm is pushed when this is non-empty.
func PushRules(rules []Rule, a Alarm, loc *time.Location) []string {
	var out []string
	for i := range rules {
		if rules[i].Delivery == DeliveryPush && rules[i].Matches(a, loc) {
			out = append(out, rules[i].Name)
		}
	}
	return out
}

// InWindow reports whether minute-of-day m falls in [from, to), wrapping past
// midnight when to < from. from == to means never.
func InWindow(m, from, to int) bool {
	switch {
	case from == to:
		return false
	case from < to:
		return m >= from && m < to
	default:
		return m >= from || m < to
	}
}

// Suggested mirrors the Mac's default rules (ensureDefaultNotificationRules),
// adapted to Linux delivery names. Offered from the dashboard's empty state;
// never created automatically.
func Suggested() []Rule {
	return []Rule{
		{Name: "Critical alarms", Enabled: true, MinSeverity: "Critical", Delivery: DeliveryPush},
		{Name: "Person detection", Enabled: true, Kinds: []string{"Person"}, MinSeverity: "Warning", Delivery: DeliveryPush},
		{Name: "Motion events", Enabled: true, Kinds: []string{"Motion"}, MinSeverity: "Warning", Delivery: DeliveryDashboard},
		{Name: "Recording and camera health", Enabled: true, Kinds: []string{"Camera Offline", "Storage", "Tamper"}, MinSeverity: "Critical", Delivery: DeliveryPush,
			Instructions: "Check the camera's power and network connection. If it stays down, open a ticket with IT."},
	}
}

func contains(list []string, v string, fold bool) bool {
	for _, x := range list {
		if x == v || (fold && strings.EqualFold(x, v)) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
