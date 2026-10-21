package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ErrBadCursor is a SearchEvents cursor that did not come from EventCursor.
var ErrBadCursor = errors.New("invalid cursor")

// EventFilter selects detection events for SearchEvents. Zero values mean
// "no constraint". From/To are Unix seconds, From inclusive and To exclusive.
// CameraIDs empty = every camera (the server intersects the caller's
// requested cameras with their scope before it gets here).
type EventFilter struct {
	CameraIDs []string
	Kinds     []string
	Labels    []string
	From, To  int64
	MinScore  float64
	Threats   []string
	Anomaly   *bool
	Text      string // case-insensitive LIKE over label, description and tags
	Plate     string // prefix of events.plate (normalized by the caller) (wave16-plates)
	PersonID  string // exact events.person_id (wave16-faces)
	TrackID   string // exact events.track_id (wave16-tracking)
	Limit     int    // default 100, at most 500
	Before    string // keyset cursor from a previous page (EventCursor)
}

const (
	searchDefaultLimit = 100
	searchMaxLimit     = 500
)

// EventCursor is the keyset position of an event: "<createdAt>.<id>".
func EventCursor(e *Event) string { return strconv.FormatInt(e.CreatedAt, 10) + "." + e.ID }

func parseEventCursor(c string) (PageKey, bool) {
	at, id, ok := strings.Cut(c, ".")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return PageKey{}, false
	}
	return PageKey{CreatedAt: n, ID: id}, true
}

func inClause(col string, vals []string, where *[]string, args *[]any) {
	if len(vals) == 0 {
		return
	}
	*where = append(*where, col+` IN (`+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+`)`)
	for _, v := range vals {
		*args = append(*args, v)
	}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SearchEvents returns one page of matching events, newest first, and the
// cursor for the next page ("" when this is the last).
func (s *Store) SearchEvents(ctx context.Context, f EventFilter) ([]Event, string, error) {
	var where []string
	var args []any
	inClause("camera_id", f.CameraIDs, &where, &args)
	inClause("kind", f.Kinds, &where, &args)
	inClause("label", f.Labels, &where, &args)
	inClause("threat", f.Threats, &where, &args)
	if f.From > 0 {
		where = append(where, `created_at >= ?`)
		args = append(args, f.From)
	}
	if f.To > 0 {
		where = append(where, `created_at < ?`)
		args = append(args, f.To)
	}
	if f.MinScore > 0 {
		where = append(where, `score >= ?`)
		args = append(args, f.MinScore)
	}
	if f.Anomaly != nil {
		where = append(where, `anomaly = ?`)
		args = append(args, boolInt(*f.Anomaly))
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		pat := "%" + escapeLike(t) + "%"
		where = append(where, `(label LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\' OR tags LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat)
	}
	if f.Plate != "" {
		s.ensurePlateIndex(ctx)
		where = append(where, `plate LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(f.Plate)+"%")
	}
	if f.PersonID != "" {
		where = append(where, `person_id = ?`)
		args = append(args, f.PersonID)
	}
	if f.TrackID != "" {
		where = append(where, `track_id = ?`)
		args = append(args, f.TrackID)
	}
	if f.Before != "" {
		k, ok := parseEventCursor(f.Before)
		if !ok {
			return nil, "", ErrBadCursor
		}
		where = append(where, `(created_at < ? OR (created_at = ? AND id < ?))`)
		args = append(args, k.CreatedAt, k.CreatedAt, k.ID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	q := `SELECT ` + eventCols + ` FROM events`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EventCursor(&out[limit-1])
	}
	return out, next, nil
}

// FacetCount is one value of a filter facet and how many events carry it.
type FacetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// EventFacets are the counts that feed the filter bar.
type EventFacets struct {
	Kinds   []FacetCount `json:"kinds"`
	Labels  []FacetCount `json:"labels"`
	Threats []FacetCount `json:"threats"`
}

const maxLabelFacets = 50

// EventFacets counts events by kind, label and threat in [from, to) (Unix
// seconds, 0 = open) on the given cameras (empty = all).
func (s *Store) EventFacets(ctx context.Context, from, to int64, cameraIDs []string) (*EventFacets, error) {
	var where []string
	var args []any
	inClause("camera_id", cameraIDs, &where, &args)
	if from > 0 {
		where = append(where, `created_at >= ?`)
		args = append(args, from)
	}
	if to > 0 {
		where = append(where, `created_at < ?`)
		args = append(args, to)
	}
	clause := ""
	if len(where) > 0 {
		clause = ` WHERE ` + strings.Join(where, ` AND `)
	}
	count := func(col string, limit int) ([]FacetCount, error) {
		q := `SELECT ` + col + `, COUNT(*) AS n FROM events` + clause +
			` GROUP BY ` + col + ` HAVING ` + col + ` <> '' ORDER BY n DESC, ` + col + ` LIMIT ?`
		rows, err := s.DB.QueryContext(ctx, q, append(append([]any{}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []FacetCount{}
		for rows.Next() {
			var c FacetCount
			if err := rows.Scan(&c.Value, &c.Count); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	f := &EventFacets{}
	var err error
	if f.Kinds, err = count("kind", 100); err != nil {
		return nil, err
	}
	if f.Labels, err = count("label", maxLabelFacets); err != nil {
		return nil, err
	}
	if f.Threats, err = count("threat", 10); err != nil {
		return nil, err
	}
	return f, nil
}
