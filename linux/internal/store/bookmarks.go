package store

// Timeline bookmarks (WAVE7-FEATURES.md, Stream B): moments an operator
// pins on a camera's timeline. at_ms is epoch milliseconds; duration_ms 0
// is a point, >0 a range starting at at_ms.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// BookmarkColors is the fixed palette of color tokens a bookmark may carry.
// The empty color is stored as BookmarkDefaultColor.
var BookmarkColors = []string{"red", "orange", "yellow", "green", "teal", "blue", "purple", "pink", "gray"}

const (
	BookmarkDefaultColor = "blue"
	MaxBookmarkTitle     = 120  // runes
	MaxBookmarkNote      = 2000 // runes
	// MaxBookmarkDurationMs caps a range bookmark (24 hours).
	MaxBookmarkDurationMs int64 = 24 * 60 * 60 * 1000
)

// BookmarkInvalidError is a bad bookmark field (a client mistake, HTTP 400).
type BookmarkInvalidError struct{ Msg string }

func (e BookmarkInvalidError) Error() string { return e.Msg }

type Bookmark struct {
	ID         string
	CameraID   string
	AtMs       int64
	DurationMs int64
	Title      string
	Note       string
	Color      string
	CreatedBy  string // user ID
	CreatedAt  int64  // Unix seconds
}

// NormalizeBookmarkColor validates a palette token ("" means the default).
func NormalizeBookmarkColor(c string) (string, error) {
	c = strings.ToLower(strings.TrimSpace(c))
	if c == "" {
		return BookmarkDefaultColor, nil
	}
	for _, ok := range BookmarkColors {
		if c == ok {
			return c, nil
		}
	}
	return "", BookmarkInvalidError{"color must be one of " + strings.Join(BookmarkColors, ", ")}
}

// ValidateBookmarkDuration checks a range length: 0 (a point) up to 24 h.
func ValidateBookmarkDuration(ms int64) error {
	if ms < 0 || ms > MaxBookmarkDurationMs {
		return BookmarkInvalidError{"durationMs must be from 0 (a point) to 86400000 (24 hours)"}
	}
	return nil
}

// clean validates and trims a bookmark's fields in place.
func (b *Bookmark) clean() error {
	b.Title = strings.TrimSpace(b.Title)
	b.Note = strings.TrimSpace(b.Note)
	switch {
	case b.AtMs <= 0:
		return BookmarkInvalidError{"atMs must be a time in epoch milliseconds"}
	case len([]rune(b.Title)) > MaxBookmarkTitle:
		return BookmarkInvalidError{"the title can be up to 120 characters"}
	case len([]rune(b.Note)) > MaxBookmarkNote:
		return BookmarkInvalidError{"the note can be up to 2000 characters"}
	}
	if err := ValidateBookmarkDuration(b.DurationMs); err != nil {
		return err
	}
	c, err := NormalizeBookmarkColor(b.Color)
	if err != nil {
		return err
	}
	b.Color = c
	return nil
}

const bookmarkCols = `id, camera_id, at_ms, duration_ms, title, note, color, created_by, created_at`

func scanBookmark(row interface{ Scan(...any) error }) (*Bookmark, error) {
	b := &Bookmark{}
	err := row.Scan(&b.ID, &b.CameraID, &b.AtMs, &b.DurationMs, &b.Title, &b.Note, &b.Color, &b.CreatedBy, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// AddBookmark stores a new bookmark (ID and CreatedAt are filled in). The
// camera must exist (ErrNotFound).
func (s *Store) AddBookmark(ctx context.Context, b *Bookmark) error {
	if err := b.clean(); err != nil {
		return err
	}
	var one int
	if err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM cameras WHERE id = ?`, b.CameraID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	b.ID = NewID()
	b.CreatedAt = now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO bookmarks(`+bookmarkCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		b.ID, b.CameraID, b.AtMs, b.DurationMs, b.Title, b.Note, b.Color, b.CreatedBy, b.CreatedAt)
	return err
}

// BookmarkByID reads one bookmark (ErrNotFound if there is none).
func (s *Store) BookmarkByID(ctx context.Context, id string) (*Bookmark, error) {
	return scanBookmark(s.DB.QueryRowContext(ctx, `SELECT `+bookmarkCols+` FROM bookmarks WHERE id = ?`, id))
}

// Bookmarks lists a camera's bookmarks overlapping [fromMs, toMs], oldest
// first. A zero toMs means no upper bound; a zero fromMs no lower bound.
func (s *Store) Bookmarks(ctx context.Context, cameraID string, fromMs, toMs int64) ([]Bookmark, error) {
	q := `SELECT ` + bookmarkCols + ` FROM bookmarks WHERE camera_id = ?`
	args := []any{cameraID}
	if fromMs > 0 {
		q += ` AND at_ms + duration_ms >= ?`
		args = append(args, fromMs)
	}
	if toMs > 0 {
		q += ` AND at_ms <= ?`
		args = append(args, toMs)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY at_ms, created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Bookmark{}
	for rows.Next() {
		b, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// UpdateBookmark rewrites a bookmark's time, title, note and color (the
// camera and author never change). It returns the stored row.
func (s *Store) UpdateBookmark(ctx context.Context, b Bookmark) (*Bookmark, error) {
	if err := b.clean(); err != nil {
		return nil, err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE bookmarks SET at_ms = ?, duration_ms = ?, title = ?, note = ?, color = ? WHERE id = ?`,
		b.AtMs, b.DurationMs, b.Title, b.Note, b.Color, b.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.BookmarkByID(ctx, b.ID)
}

// DeleteBookmark removes a bookmark.
func (s *Store) DeleteBookmark(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
