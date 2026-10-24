// Package insights turns a range of activity counts into the Insights report
// (WAVE14-AI-ASSIST.md, "Insights"): every local day's totals, the busiest
// cameras, the alarm mix and a few recommendations (recommend.go). It is
// pure: the server reads the counts, Build does the arithmetic, and nothing
// here calls a model.
package insights

import (
	"fmt"
	"sort"
	"time"
)

// Input is what the server read for one range, already cut to the caller's
// camera scope.
type Input struct {
	From, To time.Time      // To is exclusive
	Loc      *time.Location // whose local days; nil = time.Local
	// Cameras the caller can see. Alarms of a camera not listed (one deleted
	// since) still count toward the totals and the mix.
	Cameras []Camera
	Events  []EventCount
	Alarms  []AlarmCount
	// RuleKinds holds each enabled rule's alarm kinds; an empty list covers
	// every kind.
	RuleKinds [][]string
}

// Camera is one camera's name and alert setting.
type Camera struct {
	ID, Name string
	AlertOn  string    // auto, motion, peopleAndVehicles or people
	Created  time.Time // zero = unknown
}

// EventCount is the events one camera logged of one kind in one hour
// (store.EventActivity). The hours are local hours in the report's zone
// (Hour is the instant each starts), so every event lands on its local day.
type EventCount struct {
	CameraID, Kind string
	Hour           time.Time // start of the hour
	Count          int
}

// AlarmCount is the alarms raised on one local day for one camera and kind
// (store.AlarmActivityByDay).
type AlarmCount struct {
	Day            string // local date, YYYY-MM-DD
	CameraID, Kind string // CameraID is "" for alarms without a camera (Storage)
	Count          int
}

// Report is the Insights schema in api/openapi.yaml, less the range.
type Report struct {
	Totals          Totals           `json:"totals"`
	Daily           []Day            `json:"daily"`
	Cameras         []CameraStat     `json:"cameras"`
	Mix             []KindShare      `json:"mix"`
	Recommendations []Recommendation `json:"recommendations"`
}

// Totals are the range's counts.
type Totals struct {
	Events            int `json:"events"`
	Alarms            int `json:"alarms"`
	CamerasWithAlarms int `json:"camerasWithAlarms"`
	OfflineCameras    int `json:"offlineCameras"` // cameras with a Camera Offline alarm
}

// Day is one local day; ByKind counts its alarms per kind.
type Day struct {
	Day    string         `json:"day"`
	Events int            `json:"events"`
	Alarms int            `json:"alarms"`
	ByKind map[string]int `json:"byKind"`
}

// CameraStat is one row of the busiest cameras.
type CameraStat struct {
	CameraID string `json:"cameraId"`
	Name     string `json:"name"`
	Alarms   int    `json:"alarms"`
	TopKind  string `json:"topKind,omitempty"` // "" without alarms
	AlertOn  string `json:"alertOn"`
}

// KindShare is one alarm kind's part of the range's alarms.
type KindShare struct {
	Kind   string  `json:"kind"`
	Alarms int     `json:"alarms"`
	Share  float64 `json:"share"`
}

// Recommendation is one thing worth changing. ID is stable per check and
// camera ("motion-only:<cameraId>"), so a client can tell them apart.
type Recommendation struct {
	ID       string  `json:"id"`
	Level    string  `json:"level"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	CameraID string  `json:"cameraId,omitempty"`
	Action   *Action `json:"action,omitempty"`
}

// Action is what a recommendation's button does in the web app.
type Action struct {
	Type       string `json:"type"`
	CameraID   string `json:"cameraId,omitempty"`   // setAlertOn, openCamera
	AlertOn    string `json:"alertOn,omitempty"`    // setAlertOn
	TemplateID string `json:"templateId,omitempty"` // openTemplate
}

// topCameras caps Report.Cameras.
const topCameras = 10

// Build makes the report. now marks how much of the range has happened,
// which the quiet-camera check needs.
func Build(in Input, now time.Time) Report {
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	rep := Report{Cameras: []CameraStat{}, Mix: []KindShare{}}
	t := tally{
		alarms:      map[string]map[string]int{},
		offlineDays: map[string]map[string]bool{},
		events:      map[string]int{},
		kinds:       map[string]int{},
	}

	days := localDays(in.From, in.To, loc)
	index := make(map[string]int, len(days))
	for i, d := range days {
		index[d] = i
		rep.Daily = append(rep.Daily, Day{Day: d, ByKind: map[string]int{}})
	}
	// on finds a date's day. A date outside the range (an hour bucket that
	// began before it) goes to the nearest end, so the days add up to the
	// totals.
	on := func(date string) *Day {
		i, ok := index[date]
		switch {
		case ok:
		case date < days[0]:
			i = 0
		default:
			i = len(days) - 1
		}
		return &rep.Daily[i]
	}

	for _, e := range in.Events {
		on(e.Hour.In(loc).Format(time.DateOnly)).Events += e.Count
		rep.Totals.Events += e.Count
		t.events[e.CameraID] += e.Count
	}
	for _, a := range in.Alarms {
		d := on(a.Day)
		d.Alarms += a.Count
		d.ByKind[a.Kind] += a.Count
		rep.Totals.Alarms += a.Count
		t.kinds[a.Kind] += a.Count
		if a.CameraID == "" {
			continue
		}
		if t.alarms[a.CameraID] == nil {
			t.alarms[a.CameraID] = map[string]int{}
		}
		t.alarms[a.CameraID][a.Kind] += a.Count
		if a.Kind == kindOffline {
			if t.offlineDays[a.CameraID] == nil {
				t.offlineDays[a.CameraID] = map[string]bool{}
			}
			t.offlineDays[a.CameraID][a.Day] = true
		}
	}
	rep.Totals.CamerasWithAlarms = len(t.alarms)
	rep.Totals.OfflineCameras = len(t.offlineDays)

	for _, c := range in.Cameras {
		kinds := t.alarms[c.ID]
		rep.Cameras = append(rep.Cameras, CameraStat{CameraID: c.ID, Name: c.Name, Alarms: sum(kinds), TopKind: top(kinds), AlertOn: c.AlertOn})
	}
	sort.Slice(rep.Cameras, func(i, j int) bool {
		a, b := rep.Cameras[i], rep.Cameras[j]
		if a.Alarms != b.Alarms {
			return a.Alarms > b.Alarms
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.CameraID < b.CameraID
	})
	if len(rep.Cameras) > topCameras {
		rep.Cameras = rep.Cameras[:topCameras]
	}

	for k, n := range t.kinds {
		rep.Mix = append(rep.Mix, KindShare{Kind: k, Alarms: n, Share: share(n, rep.Totals.Alarms)})
	}
	sort.Slice(rep.Mix, func(i, j int) bool {
		if rep.Mix[i].Alarms != rep.Mix[j].Alarms {
			return rep.Mix[i].Alarms > rep.Mix[j].Alarms
		}
		return rep.Mix[i].Kind < rep.Mix[j].Kind
	})

	rep.Recommendations = recommend(in, t, now)
	return rep
}

// localDays lists the local dates [from, to) touches, oldest first, and at
// least from's date.
func localDays(from, to time.Time, loc *time.Location) []string {
	last := from
	if to.After(from) {
		last = to.Add(-time.Nanosecond)
	}
	end := last.In(loc).Format(time.DateOnly)
	y, m, d := from.In(loc).Date()
	var out []string
	// Step from noon: on some daylight saving days local midnight doesn't
	// exist, and time.Date may then land on the day before.
	for day := time.Date(y, m, d, 12, 0, 0, 0, loc); ; day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		out = append(out, date)
		if date >= end {
			return out
		}
	}
}

func sum(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// top is the kind with the most alarms, the first by name on a tie; "" for
// none.
func top(counts map[string]int) string {
	best, most := "", 0
	for k, n := range counts {
		if n > most || (n == most && k < best) {
			best, most = k, n
		}
	}
	return best
}

// share is n's part of total, 0 when there is nothing to divide (JSON has no
// NaN).
func share(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(n) / float64(total)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
