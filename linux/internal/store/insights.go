package store

import (
	"context"
	"sort"
	"time"
)

// AlarmDayCount is the alarms opened on one local day for one camera and
// kind. CameraID is "" for alarms without a camera (Storage).
type AlarmDayCount struct {
	Day      string // local date, YYYY-MM-DD
	CameraID string
	Kind     string
	Count    int
}

// AlarmActivityByDay counts alarms opened (created_at, Unix seconds) in
// [from, to) per local day in loc (nil = time.Local), camera and kind, oldest
// day first. Camera scope is applied by the caller.
func (s *Store) AlarmActivityByDay(ctx context.Context, from, to int64, loc *time.Location) ([]AlarmDayCount, error) {
	if loc == nil {
		loc = time.Local
	}
	// Days are counted here, not in SQL: a local day isn't a fixed number of
	// seconds (daylight saving time), and SQLite doesn't know loc.
	rows, err := s.DB.QueryContext(ctx, `
		SELECT camera_id, kind, created_at
		FROM alarms
		WHERE created_at >= ? AND created_at < ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ day, camera, kind string }
	counts := map[key]int{}
	for rows.Next() {
		var k key
		var at int64
		if err := rows.Scan(&k.camera, &k.kind, &at); err != nil {
			return nil, err
		}
		k.day = time.Unix(at, 0).In(loc).Format(time.DateOnly)
		counts[k]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AlarmDayCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, AlarmDayCount{Day: k.day, CameraID: k.camera, Kind: k.kind, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.CameraID != b.CameraID {
			return a.CameraID < b.CameraID
		}
		return a.Kind < b.Kind
	})
	return out, nil
}
