package store

// Video uploads in progress (WAVE23-PHONE-CAMERA.md section B). The row
// holds what the client declared; the parts are files the server keeps in
// <data>/uploads/<id>/ until the upload completes, is aborted or expires.

import (
	"context"
	"database/sql"
	"errors"
)

// Upload is one video upload in progress. Times are Unix seconds except
// RecordedAt (Unix ms, 0 = unknown).
type Upload struct {
	ID          string
	UserID      string
	UserName    string
	FileName    string
	Size        int64
	SHA256      string // lowercase hex
	ContentType string
	Title       string
	Note        string
	RecordedAt  int64
	CameraID    string
	EventID     string
	AlarmID     string
	IncidentID  string
	Device      string
	CreatedAt   int64
	ExpiresAt   int64
}

const uploadCols = `id, user_id, user_name, file_name, size, sha256, content_type, title, note, recorded_at, camera_id, event_id, alarm_id, incident_id, device, created_at, expires_at`

func scanUpload(row interface{ Scan(...any) error }) (*Upload, error) {
	u := &Upload{}
	if err := row.Scan(&u.ID, &u.UserID, &u.UserName, &u.FileName, &u.Size, &u.SHA256, &u.ContentType, &u.Title, &u.Note, &u.RecordedAt,
		&u.CameraID, &u.EventID, &u.AlarmID, &u.IncidentID, &u.Device, &u.CreatedAt, &u.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// CreateUpload records a new upload (u.ID set by the caller).
func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO evidence_uploads(`+uploadCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.UserID, u.UserName, u.FileName, u.Size, u.SHA256, u.ContentType, u.Title, u.Note, u.RecordedAt,
		u.CameraID, u.EventID, u.AlarmID, u.IncidentID, u.Device, u.CreatedAt, u.ExpiresAt)
	return err
}

// Upload reads one upload (ErrNotFound if there is none).
func (s *Store) Upload(ctx context.Context, id string) (*Upload, error) {
	return scanUpload(s.DB.QueryRowContext(ctx, `SELECT `+uploadCols+` FROM evidence_uploads WHERE id = ?`, id))
}

// DeleteUpload removes an upload's row (the caller removes its parts).
func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM evidence_uploads WHERE id = ?`, id)
	return err
}

// ExpiredUploads lists the ids of uploads that expired before now (Unix s).
func (s *Store) ExpiredUploads(ctx context.Context, now int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM evidence_uploads WHERE expires_at <= ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UploadIDs lists every upload in progress.
func (s *Store) UploadIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM evidence_uploads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
