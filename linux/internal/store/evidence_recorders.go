package store

import (
	"context"
	"database/sql"
	"errors"
)

// Evidence lives in the vault of the recording server that cut it
// (evidence.recorder_id); its file is served from there.

// AddEvidenceOn records evidence filed on recording server recorderID.
func (s *Store) AddEvidenceOn(ctx context.Context, e *Evidence, recorderID string) error {
	if recorderID == "" {
		recorderID = LocalRecorderID
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO evidence(`+evidenceCols+`, recorder_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.CaseID, e.AlarmID, e.CameraID, e.CameraName, e.Title, e.Range, e.File, e.Size, e.SHA256, e.LockedAt, e.LockedBy, recorderID)
	return err
}

// EvidenceRecorderID is the recording server holding an evidence file.
func (s *Store) EvidenceRecorderID(ctx context.Context, evidenceID string) (string, error) {
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT recorder_id FROM evidence WHERE id = ?`, evidenceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}
