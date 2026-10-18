package server

import (
	"fmt"
	"strings"
	"time"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

// lineCrossAlarm words the alarm for one of our own line crossings
// (link.SourceLines) at the line's own severity (meta.severity, default
// Critical; wave 9). An "Info" line logs the event but raises no alarm
// (ok false).
func lineCrossAlarm(camera string, zones []store.Zone, d link.Detection, at time.Time) (title, severity, detail string, ok bool) {
	severity = lineSeverityOf(zones, d.Rule)
	if severity == store.LineSeverityInfo {
		return "", "", "", false
	}
	what := strings.ToLower(d.Label)
	if what == "" {
		what = "object"
	}
	detail = fmt.Sprintf("%s: %s crossed the line %q at %s.", camera, aOrAn(what), d.Rule, at.Format("3:04:05 PM"))
	return "Line crossed", severity, detail, true
}

// lineSeverityOf finds the camera's line by the rule name its crossings
// carry. Several lines sharing a name: the most severe wins. A line that is
// gone (renamed or deleted since the crossing) alarms Critical, as before.
func lineSeverityOf(zones []store.Zone, name string) string {
	rank := map[string]int{store.LineSeverityInfo: 1, store.LineSeverityWarning: 2, store.LineSeverityCritical: 3}
	best := ""
	for _, z := range zones {
		if z.Kind != store.ZoneKindLine || z.Name != name {
			continue
		}
		if sev := store.LineSeverity(z); rank[sev] > rank[best] {
			best = sev
		}
	}
	if best == "" {
		return store.LineSeverityCritical
	}
	return best
}

func aOrAn(w string) string {
	if strings.ContainsRune("aeiou", rune(w[0])) {
		return "an " + w
	}
	return "a " + w
}
