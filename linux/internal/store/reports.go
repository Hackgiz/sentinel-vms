package store

import (
	"context"
	"sort"
	"time"
)

// ActivityCount is one bucket of detections: a camera, a kind and the start of
// a local hour (Unix seconds of its start, like every timestamp in the store). Camera scope is applied by the
// caller, so this stays a plain aggregate.
type ActivityCount struct {
	CameraID string
	Kind     string
	Hour     int64
	Count    int
}

// AlarmCount is one row of alarms opened in a range.
type AlarmCount struct {
	CameraID string
	Kind     string
	Severity string
	Count    int
}

const hourSec = int64(3600)

// quarterSec is the SQL bucket EventActivity folds into local hours: every
// zone's UTC offset is a whole number of quarter hours, so a quarter never
// straddles a local hour boundary.
const quarterSec = int64(900)

// localHourStart is the start (Unix seconds) of the local hour in loc that
// holds the instant at. It is computed from the zone offset in force at that
// instant, so a repeated hour at the end of daylight saving time stays two
// buckets and a zone with a :30 or :45 offset gets :30/:45 UTC hour starts.
func localHourStart(at int64, loc *time.Location) int64 {
	_, off := time.Unix(at, 0).In(loc).Zone()
	local := at + int64(off)
	local -= ((local % hourSec) + hourSec) % hourSec
	return local - int64(off)
}

// EventActivity counts events created in [from, to) (Unix seconds) per camera,
// kind and local hour in loc (nil = time.Local); Hour is the Unix second at
// which that local hour starts. Oldest hour first.
func (s *Store) EventActivity(ctx context.Context, from, to int64, loc *time.Location) ([]ActivityCount, error) {
	if loc == nil {
		loc = time.Local
	}
	// Hours are folded here, not in SQL: SQLite doesn't know loc, and a local
	// hour isn't tied to the UTC hour in zones with a fractional offset.
	rows, err := s.DB.QueryContext(ctx, `
		SELECT camera_id, kind, (created_at / ?) * ?, COUNT(*)
		FROM events
		WHERE created_at >= ? AND created_at < ?
		GROUP BY camera_id, kind, (created_at / ?)`, quarterSec, quarterSec, from, to, quarterSec)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct {
		camera, kind string
		hour         int64
	}
	counts := map[key]int{}
	for rows.Next() {
		var k key
		var at int64
		var n int
		if err := rows.Scan(&k.camera, &k.kind, &at, &n); err != nil {
			return nil, err
		}
		k.hour = localHourStart(at, loc)
		counts[k] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ActivityCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, ActivityCount{CameraID: k.camera, Kind: k.kind, Hour: k.hour, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Hour != b.Hour {
			return a.Hour < b.Hour
		}
		if a.CameraID != b.CameraID {
			return a.CameraID < b.CameraID
		}
		return a.Kind < b.Kind
	})
	return out, nil
}

// AlarmActivity counts alarms opened (created_at, Unix seconds) in [from, to)
// per camera, kind and severity.
func (s *Store) AlarmActivity(ctx context.Context, from, to int64) ([]AlarmCount, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT camera_id, kind, severity, COUNT(*)
		FROM alarms
		WHERE created_at >= ? AND created_at < ?
		GROUP BY camera_id, kind, severity
		ORDER BY 4 DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlarmCount{}
	for rows.Next() {
		var c AlarmCount
		if err := rows.Scan(&c.CameraID, &c.Kind, &c.Severity, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
