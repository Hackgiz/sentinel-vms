package store

// Faces and people (WAVE16-DETECTION.md, stream C). A face is one face found
// on a Person event's thumbnail: its box, the detector's score, the
// recognizer's vector (float32 little-endian bytes, model = the model
// file's SHA-256 prefix), a small JPEG crop for the UI and the person it
// belongs to ('' = unassigned). People are the gallery. Faces go with their
// event (ON DELETE CASCADE) and with their person (DeletePerson); unassigned
// faces are purged after faces.unassigned_days.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// What a match does to the event's alarm (people.alert, openapi WatchAlert).
const (
	PersonAlertDefault = "default" // the alarm names the person
	PersonAlertNever   = "never"   // the alarm is closed: a known person
	PersonAlertAlways  = "always"  // a Critical Watchlist alarm is raised
)

// ValidPersonAlert reports whether a is a WatchAlert value.
func ValidPersonAlert(a string) bool {
	return a == PersonAlertDefault || a == PersonAlertNever || a == PersonAlertAlways
}

// ErrFaceAssigned: the face already belongs to someone.
var ErrFaceAssigned = errors.New("that face already belongs to someone")

// Person is one gallery entry.
type Person struct {
	ID               string
	Name             string
	Notes            string
	Alert            string
	CreatedAt        int64 // Unix ms
	LastSeenAt       int64 // Unix ms of the newest face of theirs; 0 = never
	LastSeenCameraID string
}

// Face is one stored face.
type Face struct {
	ID         string
	EventID    string
	CameraID   string
	AtMs       int64
	Box        string // JSON [x1,y1,x2,y2], normalized to the thumbnail
	Score      float64
	Model      string
	Vec        []byte
	PersonID   string
	Similarity float64 // cosine to the matched person when matched on arrival
	CreatedAt  int64   // Unix ms
}

const faceCols = `id, event_id, camera_id, at_ms, box, score, model, vec, person_id, similarity, created_at`

func scanFace(row interface{ Scan(...any) error }) (*Face, error) {
	f := &Face{}
	if err := row.Scan(&f.ID, &f.EventID, &f.CameraID, &f.AtMs, &f.Box, &f.Score, &f.Model, &f.Vec, &f.PersonID, &f.Similarity, &f.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *Store) faceList(ctx context.Context, q string, args ...any) ([]Face, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+faceCols+` FROM faces `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Face{}
	for rows.Next() {
		f, err := scanFace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func nowMs() int64 { return time.Now().UnixMilli() }

// AddFace stores a face (ID and CreatedAt are filled when empty) with its
// crop.
func (s *Store) AddFace(ctx context.Context, f *Face, crop []byte) error {
	if f.ID == "" {
		f.ID = NewID()
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = nowMs()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO faces(`+faceCols+`, crop) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.EventID, f.CameraID, f.AtMs, f.Box, f.Score, f.Model, f.Vec, f.PersonID, f.Similarity, f.CreatedAt, crop)
	return err
}

// Face reads one face (without its crop).
func (s *Store) Face(ctx context.Context, id string) (*Face, error) {
	return scanFace(s.DB.QueryRowContext(ctx, `SELECT `+faceCols+` FROM faces WHERE id = ?`, id))
}

// FaceCrop returns a face's JPEG crop and its camera (for the scope check).
func (s *Store) FaceCrop(ctx context.Context, id string) (crop []byte, cameraID string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT crop, camera_id FROM faces WHERE id = ?`, id).Scan(&crop, &cameraID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return crop, cameraID, err
}

// EventFaces lists the faces found on an event, best first.
func (s *Store) EventFaces(ctx context.Context, eventID string) ([]Face, error) {
	return s.faceList(ctx, `WHERE event_id = ? ORDER BY score DESC, id`, eventID)
}

// UnassignedFaces lists faces that belong to nobody, newest first.
func (s *Store) UnassignedFaces(ctx context.Context, limit int) ([]Face, error) {
	return s.faceList(ctx, `WHERE person_id = '' ORDER BY at_ms DESC, id DESC LIMIT ?`, limit)
}

// CountUnassignedFaces counts faces that belong to nobody.
func (s *Store) CountUnassignedFaces(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM faces WHERE person_id = ''`).Scan(&n)
	return n, err
}

// PersonFaces lists a person's faces, newest first.
func (s *Store) PersonFaces(ctx context.Context, personID string) ([]Face, error) {
	return s.faceList(ctx, `WHERE person_id = ? ORDER BY at_ms DESC, id DESC`, personID)
}

// GalleryFace is one enrolled vector.
type GalleryFace struct {
	PersonID string
	Vec      []byte
}

// Gallery lists every assigned face's vector from model.
func (s *Store) Gallery(ctx context.Context, model string) ([]GalleryFace, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT f.person_id, f.vec FROM faces f JOIN people p ON p.id = f.person_id WHERE f.model = ?`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GalleryFace
	for rows.Next() {
		var g GalleryFace
		if err := rows.Scan(&g.PersonID, &g.Vec); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AssignFace gives an unassigned face to a person (ErrFaceAssigned when it
// belongs to someone already, ErrNotFound when the face or person is gone).
func (s *Store) AssignFace(ctx context.Context, faceID, personID string) error {
	if _, err := s.Person(ctx, personID); err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE faces SET person_id = ? WHERE id = ? AND person_id = ''`, personID, faceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.Face(ctx, faceID); err != nil {
		return err
	}
	return ErrFaceAssigned
}

// DeleteFace removes one face.
func (s *Store) DeleteFace(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM faces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeUnassignedFaces deletes faces that belong to nobody and were seen
// before (Unix ms); it returns how many went.
func (s *Store) PurgeUnassignedFaces(ctx context.Context, before int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM faces WHERE person_id = '' AND at_ms < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MARK: - People

const personCols = `p.id, p.name, p.notes, p.alert, p.created_at,
	COALESCE((SELECT MAX(f.at_ms) FROM faces f WHERE f.person_id = p.id), 0),
	COALESCE((SELECT f.camera_id FROM faces f WHERE f.person_id = p.id ORDER BY f.at_ms DESC LIMIT 1), '')`

func scanPerson(row interface{ Scan(...any) error }) (*Person, error) {
	p := &Person{}
	if err := row.Scan(&p.ID, &p.Name, &p.Notes, &p.Alert, &p.CreatedAt, &p.LastSeenAt, &p.LastSeenCameraID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// People lists the gallery by name.
func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+personCols+` FROM people p ORDER BY p.name COLLATE NOCASE, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CountPeople counts the gallery.
func (s *Store) CountPeople(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM people`).Scan(&n)
	return n, err
}

// Person reads one person.
func (s *Store) Person(ctx context.Context, id string) (*Person, error) {
	return scanPerson(s.DB.QueryRowContext(ctx, `SELECT `+personCols+` FROM people p WHERE p.id = ?`, id))
}

// AddPerson creates a person (ID, CreatedAt and Alert filled when empty).
func (s *Store) AddPerson(ctx context.Context, p *Person) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = nowMs()
	}
	if p.Alert == "" {
		p.Alert = PersonAlertDefault
	}
	p.Name = strings.TrimSpace(p.Name)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO people(id, name, notes, alert, created_at) VALUES(?,?,?,?,?)`,
		p.ID, p.Name, p.Notes, p.Alert, p.CreatedAt)
	return err
}

// UpdatePerson saves a person's name, notes and alert.
func (s *Store) UpdatePerson(ctx context.Context, p *Person) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE people SET name = ?, notes = ?, alert = ? WHERE id = ?`, p.Name, p.Notes, p.Alert, p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePerson deletes a person and their faces. Their past events keep
// person_id and person_name.
func (s *Store) DeletePerson(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM people WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM faces WHERE person_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetEventPerson records the person matched on an event.
func (s *Store) SetEventPerson(ctx context.Context, eventID, personID, name string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE events SET person_id = ?, person_name = ? WHERE id = ?`, personID, name, eventID)
	return err
}

// RenamePersonEvents updates person_name on a person's past events.
func (s *Store) RenamePersonEvents(ctx context.Context, personID, name string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE events SET person_name = ? WHERE person_id = ?`, name, personID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetPersonAlarmText replaces an alarm's title and detail (a face match
// naming the person on a Person alarm).
func (s *Store) SetPersonAlarmText(ctx context.Context, alarmID, title, detail string) (*Alarm, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE alarms SET title = ?, detail = ?, updated_at = ? WHERE id = ?`, title, detail, now(), alarmID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Alarm(ctx, alarmID)
}
