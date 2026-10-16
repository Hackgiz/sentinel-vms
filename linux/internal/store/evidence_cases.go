package store

// Evidence cases for the Exports workspace: every row of the evidence table
// (alarm locks and operator exports alike) with the columns wave 4 added.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// EvidenceCase is an evidence row with its note, exported range (Unix ms;
// 0 for locks cut before ranges were recorded) and the recording server
// holding its file.
type EvidenceCase struct {
	Evidence
	Note       string
	FromMs     int64
	ToMs       int64
	RecorderID string
}

const evidenceCaseCols = `id, case_id, alarm_id, camera_id, camera_name, title, range_label, file, size, sha256, locked_at, locked_by, note, from_ms, to_ms, recorder_id`

func scanEvidenceCase(row interface{ Scan(...any) error }) (*EvidenceCase, error) {
	e := &EvidenceCase{}
	if err := row.Scan(&e.ID, &e.CaseID, &e.AlarmID, &e.CameraID, &e.CameraName, &e.Title, &e.Range, &e.File, &e.Size, &e.SHA256,
		&e.LockedAt, &e.LockedBy, &e.Note, &e.FromMs, &e.ToMs, &e.RecorderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return e, nil
}

// AddEvidenceCase records a case filed on recording server e.RecorderID
// ("" is the local one).
func (s *Store) AddEvidenceCase(ctx context.Context, e *EvidenceCase) error {
	if e.RecorderID == "" {
		e.RecorderID = LocalRecorderID
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO evidence(`+evidenceCaseCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.CaseID, e.AlarmID, e.CameraID, e.CameraName, e.Title, e.Range, e.File, e.Size, e.SHA256, e.LockedAt, e.LockedBy,
		e.Note, e.FromMs, e.ToMs, e.RecorderID)
	return err
}

// EvidenceCaseByID reads one case (ErrNotFound if there is none).
func (s *Store) EvidenceCaseByID(ctx context.Context, id string) (*EvidenceCase, error) {
	return scanEvidenceCase(s.DB.QueryRowContext(ctx, `SELECT `+evidenceCaseCols+` FROM evidence WHERE id = ?`, id))
}

// EvidenceCaseFilter narrows EvidenceCases. CameraIDs nil means every
// camera; an empty non-nil slice matches nothing (a scope that sees none).
type EvidenceCaseFilter struct {
	CameraIDs []string
	Limit     int // 0: no limit
}

// EvidenceCases lists cases, newest first.
func (s *Store) EvidenceCases(ctx context.Context, f EvidenceCaseFilter) ([]EvidenceCase, error) {
	q := `SELECT ` + evidenceCaseCols + ` FROM evidence`
	var args []any
	if f.CameraIDs != nil {
		if len(f.CameraIDs) == 0 {
			return []EvidenceCase{}, nil
		}
		q += ` WHERE camera_id IN (?` + strings.Repeat(`,?`, len(f.CameraIDs)-1) + `)`
		for _, id := range f.CameraIDs {
			args = append(args, id)
		}
	}
	q += ` ORDER BY locked_at DESC, case_id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EvidenceCase{}
	for rows.Next() {
		e, err := scanEvidenceCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// DeleteEvidenceCase removes a case's row (its file is the recording
// server's to delete). ErrNotFound if there is none.
func (s *Store) DeleteEvidenceCase(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM evidence WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
