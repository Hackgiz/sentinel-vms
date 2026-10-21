package rules

import (
	"strings"
	"unicode/utf8"
)

// Draft is a rule as the AI drafter describes it (WAVE14-AI-ASSIST.md §1):
// cameras and the time profile by name, the window as "HH:MM" local times.
// Nothing in it is trusted until ResolveDraft has checked it.
type Draft struct {
	Name            string
	CameraNames     []string
	Kinds           []string
	MinSeverity     string
	Schedule        bool
	From, To        string
	Days            []int
	TimeProfileName string
	Delivery        string
	Instructions    string
}

// NameRef is a camera or time profile a draft may name.
type NameRef struct{ ID, Name string }

const (
	defaultDraftName       = "New rule" // for a draft that came without a name
	draftNameChars         = 60
	draftInstructionsChars = 500
)

// ResolveDraft turns a draft into a disabled rule: names become ids, anything
// it can't resolve is left out with a warning for the admin, and missing
// values get defaults. The error is Validate's, for a draft that still isn't
// a valid rule.
func ResolveDraft(d Draft, cameras, profiles []NameRef) (Rule, []string, error) {
	r := Rule{
		// Validate counts the name's bytes (120 max), so non-Latin names are
		// cut on that too.
		Name:         clip(d.Name, draftNameChars, 120),
		Instructions: clip(d.Instructions, draftInstructionsChars, 2000),
		MinSeverity:  "Warning",
		Delivery:     DeliveryDashboard,
	}
	if r.Name == "" {
		r.Name = defaultDraftName
	}
	var warn []string

	for _, name := range d.CameraNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		found := false
		for _, c := range cameras {
			if strings.EqualFold(strings.TrimSpace(c.Name), name) {
				r.CameraIDs, found = append(r.CameraIDs, c.ID), true
			}
		}
		if !found {
			warn = append(warn, `No camera called "`+name+`".`)
		}
	}
	if len(r.CameraIDs) == 0 {
		warn = append(warn, "The rule applies to every camera.")
	}

	for _, k := range d.Kinds {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if kind, ok := kindNamed(k); ok {
			r.Kinds = append(r.Kinds, kind)
		} else {
			warn = append(warn, `No alarm kind called "`+k+`".`)
		}
	}
	if len(r.Kinds) == 0 {
		warn = append(warn, "The rule matches every kind of alarm.")
	}

	for _, s := range Severities {
		if strings.EqualFold(strings.TrimSpace(d.MinSeverity), s) {
			r.MinSeverity = s
		}
	}
	if strings.EqualFold(strings.TrimSpace(d.Delivery), DeliveryPush) {
		r.Delivery = DeliveryPush
	}

	if d.Schedule {
		from, err1 := ParseClock(strings.TrimSpace(d.From), false)
		to, err2 := ParseClock(strings.TrimSpace(d.To), true)
		switch {
		case err1 != nil || err2 != nil:
			warn = append(warn, "The hours couldn't be read, so the rule runs at all hours.")
		case from == 0 && to == 24*60: // the whole day needs no window
		case from == to%(24*60):
			warn = append(warn, "The hours start and end at the same time, so the rule runs at all hours.")
		default:
			r.Schedule, r.FromMinute, r.ToMinute = true, from, to%(24*60) // "24:00" is midnight
		}
	}
	// Days use Rule.Days numbering, 0 = Sunday … 6 = Saturday; 7 is Sunday in
	// ISO numbering. None at all means every day.
	for _, day := range d.Days {
		if day == 7 {
			day = 0
		}
		if day >= 0 && day <= 6 {
			r.Days = append(r.Days, day)
		}
	}
	if len(d.Days) > 0 && len(r.Days) == 0 {
		warn = append(warn, "The days couldn't be read, so the rule runs every day.")
	}

	if name := strings.TrimSpace(d.TimeProfileName); name != "" {
		for _, p := range profiles {
			if strings.EqualFold(strings.TrimSpace(p.Name), name) {
				r.TimeProfileID = p.ID
				break
			}
		}
		if r.TimeProfileID == "" {
			warn = append(warn, `No time profile called "`+name+`".`)
		}
	}

	r.Normalize()
	if err := r.Validate(); err != nil {
		return r, warn, err
	}
	return r, warn, nil
}

// kindNamed finds the alarm kind k names (any case, or an alias), spelled as
// in Kinds.
func kindNamed(k string) (string, bool) {
	c := CanonicalKind(k)
	for _, v := range Kinds {
		if strings.EqualFold(v, c) {
			return v, true
		}
	}
	return "", false
}

// clip trims s and cuts it to at most chars characters and bytes bytes,
// never inside a character.
func clip(s string, chars, bytes int) string {
	s = strings.TrimSpace(s)
	n := 0
	for i, c := range s {
		if n == chars || i+utf8.RuneLen(c) > bytes {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return s
}
