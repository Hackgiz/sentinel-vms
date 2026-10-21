package insights

// Recommendations (WAVE14-AI-ASSIST.md, "Insights"). Each check is one
// threshold over the range's counts; the thresholds are named so each
// boundary has a test.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Recommendation levels, most urgent first.
const (
	levelAttention  = "attention"
	levelSuggestion = "suggestion"
	levelInfo       = "info"
)

// Action types the web app acts on.
const (
	actionSetAlertOn   = "setAlertOn"
	actionOpenTemplate = "openTemplate"
	actionOpenCamera   = "openCamera"
	actionOpenStorage  = "openStorage"
)

// Alarm kinds the checks look at (rules.Kinds).
const (
	kindMotion  = "Motion"
	kindPerson  = "Person"
	kindOffline = "Camera Offline"
	kindStorage = "Storage"
)

// Camera alert settings (AlertOn in api/openapi.yaml).
const (
	alertAuto              = "auto"
	alertMotion            = "motion"
	alertPeopleAndVehicles = "peopleAndVehicles"
)

const (
	// motion-only: at least this many alarms on a camera that alerts on
	// motion, at least this percent of them plain motion.
	motionOnlyMinAlarms  = 20
	motionOnlyMinPercent = 70
	// repeat-offline: Camera Offline alarms on at least this many local days.
	repeatOfflineMinDays = 3
	// no-people-rule: at least this many Person alarms, and no enabled rule
	// covers Person.
	noPeopleRuleMinAlarms = 5
	// storage-alarms: at least this many Storage alarms.
	storageMinAlarms = 1
	// quiet-camera: watched for at least this long with no events.
	quietMinRange = 7 * 24 * time.Hour

	maxRecommendations = 6
	// peopleTemplateID is the rule template no-people-rule opens.
	peopleTemplateID = "after-hours-people"
)

// tally is what Build counted, keyed for the checks.
type tally struct {
	alarms      map[string]map[string]int  // camera → kind → alarms
	offlineDays map[string]map[string]bool // camera → local dates with a Camera Offline alarm
	events      map[string]int             // camera → events
	kinds       map[string]int             // kind → alarms, with or without a camera
}

// recommend runs every check and keeps the first maxRecommendations:
// attention, then suggestion, then info, each by id.
func recommend(in Input, t tally, now time.Time) []Recommendation {
	out := []Recommendation{}
	// Only the part of the range that has happened can be quiet.
	end := in.To
	if now.Before(end) {
		end = now
	}
	for _, c := range in.Cameras {
		total, motion := sum(t.alarms[c.ID]), t.alarms[c.ID][kindMotion]
		if total >= motionOnlyMinAlarms && motion*100 >= total*motionOnlyMinPercent && (c.AlertOn == alertAuto || c.AlertOn == alertMotion) {
			out = append(out, Recommendation{
				ID: "motion-only:" + c.ID, Level: levelSuggestion, CameraID: c.ID,
				Title:  "Mostly motion alarms on " + c.Name,
				Detail: fmt.Sprintf("%d of %d alarms were plain motion. Switching to people & vehicles keeps the people and vehicle alerts.", motion, total),
				Action: &Action{Type: actionSetAlertOn, CameraID: c.ID, AlertOn: alertPeopleAndVehicles},
			})
		}
		if n := len(t.offlineDays[c.ID]); n >= repeatOfflineMinDays {
			out = append(out, Recommendation{
				ID: "repeat-offline:" + c.ID, Level: levelAttention, CameraID: c.ID,
				Title:  c.Name + " keeps going offline",
				Detail: fmt.Sprintf("It went offline on %d different days. Check its power and network connection.", n),
				Action: &Action{Type: actionOpenCamera, CameraID: c.ID},
			})
		}
		// A camera added during the range was watched for less of it.
		since := in.From
		if c.Created.After(since) {
			since = c.Created
		}
		if watched := end.Sub(since); watched >= quietMinRange && t.events[c.ID] == 0 {
			out = append(out, Recommendation{
				ID: "quiet-camera:" + c.ID, Level: levelInfo, CameraID: c.ID,
				Title:  "No events from " + c.Name,
				Detail: fmt.Sprintf("Nothing was detected in %d days. Check that the camera still shows the scene and that motion detection is on.", int(watched/(24*time.Hour))),
			})
		}
	}
	if n := t.kinds[kindPerson]; n >= noPeopleRuleMinAlarms && !coversPerson(in.RuleKinds) {
		out = append(out, Recommendation{
			ID: "no-people-rule", Level: levelSuggestion,
			Title:  "No rule covers people",
			Detail: fmt.Sprintf("%d person alarms came in, and no enabled rule covers them. A rule can push them to phones and tell operators what to do.", n),
			Action: &Action{Type: actionOpenTemplate, TemplateID: peopleTemplateID},
		})
	}
	if n := t.kinds[kindStorage]; n >= storageMinAlarms {
		out = append(out, Recommendation{
			ID: "storage-alarms", Level: levelAttention,
			Title:  "Storage alarms",
			Detail: plural(n, "storage alarm") + " in this range. Check the free space on the recordings disk.",
			Action: &Action{Type: actionOpenStorage},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := levelRank(out[i].Level), levelRank(out[j].Level); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > maxRecommendations {
		out = out[:maxRecommendations]
	}
	return out
}

func levelRank(level string) int {
	switch level {
	case levelAttention:
		return 0
	case levelSuggestion:
		return 1
	}
	return 2
}

// coversPerson reports whether an enabled rule matches Person alarms: one
// with no kinds, or with Person among them. Rule kinds compare without case,
// as the rules engine does.
func coversPerson(ruleKinds [][]string) bool {
	for _, kinds := range ruleKinds {
		if len(kinds) == 0 {
			return true
		}
		for _, k := range kinds {
			if strings.EqualFold(k, kindPerson) {
				return true
			}
		}
	}
	return false
}
