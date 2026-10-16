package rules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TimeProfile is a named weekly schedule (Wave 6). A rule that references one
// (Rule.TimeProfileID) only fires while the profile is active. Times are in
// the server's local time zone; a span whose end is before its start runs
// overnight and belongs to the day it started, exactly like Rule schedules.
type TimeProfile struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"-"`
	Weekly    []Span `json:"weekly"`
}

// Span is one weekly window: on each of Days (0 = Sunday … 6), from Start to
// End ("HH:MM"). End "24:00" means midnight at the end of the day.
type Span struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// ParseClock parses "HH:MM" into minutes after midnight. allow24 accepts
// "24:00" (1440).
func ParseClock(s string, allow24 bool) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("%q is not a time of day (use HH:MM)", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || hh > 24 || (hh == 24 && (mm != 0 || !allow24)) {
		return 0, fmt.Errorf("%q is not a time of day (use HH:MM)", s)
	}
	return hh*60 + mm, nil
}

// Validate checks every span.
func (p *TimeProfile) Validate() error {
	if len(p.Weekly) > 200 {
		return errors.New("too many spans")
	}
	for i, sp := range p.Weekly {
		from, err := ParseClock(sp.Start, false)
		if err != nil {
			return fmt.Errorf("span %d start: %w", i+1, err)
		}
		to, err := ParseClock(sp.End, true)
		if err != nil {
			return fmt.Errorf("span %d end: %w", i+1, err)
		}
		if from == to {
			return fmt.Errorf("span %d starts and ends at the same time", i+1)
		}
		if len(sp.Days) == 0 {
			return fmt.Errorf("span %d has no days", i+1)
		}
		for _, d := range sp.Days {
			if d < 0 || d > 6 {
				return errors.New("days must be 0 (Sunday) to 6 (Saturday)")
			}
		}
	}
	return nil
}

// ActiveAt reports whether t (interpreted in its own location) falls inside
// any span. A profile with no spans is never active.
func (p *TimeProfile) ActiveAt(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	today, yesterday := int(t.Weekday()), (int(t.Weekday())+6)%7
	for _, sp := range p.Weekly {
		from, e1 := ParseClock(sp.Start, false)
		to, e2 := ParseClock(sp.End, true)
		if e1 != nil || e2 != nil || from == to {
			continue
		}
		hasDay := func(d int) bool {
			for _, x := range sp.Days {
				if x == d {
					return true
				}
			}
			return false
		}
		if from < to {
			if m >= from && m < to && hasDay(today) {
				return true
			}
			continue
		}
		// Overnight: the evening part belongs to today, the morning part to yesterday.
		if m >= from && hasDay(today) {
			return true
		}
		if m < to && hasDay(yesterday) {
			return true
		}
	}
	return false
}
