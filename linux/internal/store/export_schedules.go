package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sentinel-linux/internal/rules"
)

// Scheduled exports (Wave 7, stream C): a camera's footage between two
// times of day is exported into the evidence vault at the end of each
// window. Times are in the server's local zone; a window whose end is
// before its start runs overnight and belongs to the day it started
// (the same convention as rules and time profiles).

// ExportScheduleDest values. Only the vault makes sense unattended.
const ExportDestEvidence = "evidence"

// MaxScheduleWindow bounds one window (the long-export cap).
const MaxScheduleWindow = 12 * time.Hour

type ExportSchedule struct {
	ID        string
	Name      string
	CameraID  string
	Days      []int  // 0 = Sunday … 6, the day each window starts
	StartHM   string // "HH:MM"
	EndHM     string // "HH:MM", or "24:00"
	Dest      string
	Enabled   bool
	CreatedBy string
	CreatedAt int64 // Unix seconds
	// LastRunAt is the end of the last window the scheduler handled (Unix
	// seconds, 0 = never); LastStatus is "ok" or the failure message.
	LastRunAt  int64
	LastStatus string
}

// WindowMinutes returns the window's start and length in minutes.
func (e *ExportSchedule) WindowMinutes() (start, length int, err error) {
	start, err = rules.ParseClock(e.StartHM, false)
	if err != nil {
		return 0, 0, fmt.Errorf("start: %w", err)
	}
	end, err := rules.ParseClock(e.EndHM, true)
	if err != nil {
		return 0, 0, fmt.Errorf("end: %w", err)
	}
	if start == end {
		return 0, 0, errors.New("the window starts and ends at the same time")
	}
	length = end - start
	if length < 0 {
		length += 24 * 60 // overnight
	}
	return start, length, nil
}

// Validate normalizes (trimmed name, sorted unique days, default dest) and
// checks the schedule. The caller checks that the camera exists.
func (e *ExportSchedule) Validate() error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" || len([]rune(e.Name)) > 100 {
		return errors.New("enter a schedule name of up to 100 characters")
	}
	if strings.TrimSpace(e.CameraID) == "" {
		return errors.New("choose a camera")
	}
	if e.Dest == "" {
		e.Dest = ExportDestEvidence
	}
	if e.Dest != ExportDestEvidence {
		return errors.New(`scheduled exports go to the evidence vault (dest "evidence")`)
	}
	if len(e.Days) == 0 {
		return errors.New("choose at least one day")
	}
	seen := map[int]bool{}
	days := make([]int, 0, len(e.Days))
	for _, d := range e.Days {
		if d < 0 || d > 6 {
			return errors.New("days must be 0 (Sunday) to 6 (Saturday)")
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	sort.Ints(days)
	e.Days = days
	_, length, err := e.WindowMinutes()
	if err != nil {
		return err
	}
	if time.Duration(length)*time.Minute > MaxScheduleWindow {
		return errors.New("a window can cover at most 12 hours")
	}
	return nil
}

const exportScheduleCols = `id, name, camera_id, days, start_hm, end_hm, dest, enabled, created_by, created_at, last_run_at, last_status`

func scanExportSchedule(row interface{ Scan(...any) error }) (*ExportSchedule, error) {
	e := &ExportSchedule{}
	var days string
	var enabled int
	err := row.Scan(&e.ID, &e.Name, &e.CameraID, &days, &e.StartHM, &e.EndHM, &e.Dest, &enabled, &e.CreatedBy, &e.CreatedAt, &e.LastRunAt, &e.LastStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Enabled = enabled != 0
	if json.Unmarshal([]byte(days), &e.Days) != nil || e.Days == nil {
		e.Days = []int{}
	}
	return e, nil
}

func daysJSON(days []int) string {
	b, _ := json.Marshal(days)
	return string(b)
}

// ExportSchedules lists the schedules by name.
func (s *Store) ExportSchedules(ctx context.Context) ([]ExportSchedule, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+exportScheduleCols+` FROM export_schedules ORDER BY name COLLATE NOCASE, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExportSchedule{}
	for rows.Next() {
		e, err := scanExportSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ExportSchedule returns one schedule.
func (s *Store) ExportSchedule(ctx context.Context, id string) (*ExportSchedule, error) {
	return scanExportSchedule(s.DB.QueryRowContext(ctx, `SELECT `+exportScheduleCols+` FROM export_schedules WHERE id = ?`, id))
}

// SaveExportSchedule validates and inserts (e.ID "": new ID, CreatedAt now)
// or updates an existing schedule's editable fields (not who created it,
// nor its last-run state). ErrNotFound for an unknown ID or camera.
func (s *Store) SaveExportSchedule(ctx context.Context, e *ExportSchedule) error {
	if err := e.Validate(); err != nil {
		return err
	}
	enabled := 0
	if e.Enabled {
		enabled = 1
	}
	if e.ID == "" {
		e.ID, e.CreatedAt = NewID(), now()
		_, err := s.DB.ExecContext(ctx, `INSERT INTO export_schedules(`+exportScheduleCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,0,'')`,
			e.ID, e.Name, e.CameraID, daysJSON(e.Days), e.StartHM, e.EndHM, e.Dest, enabled, e.CreatedBy, e.CreatedAt)
		if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrNotFound
		}
		return err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE export_schedules SET name = ?, camera_id = ?, days = ?, start_hm = ?, end_hm = ?, dest = ?, enabled = ? WHERE id = ?`,
		e.Name, e.CameraID, daysJSON(e.Days), e.StartHM, e.EndHM, e.Dest, enabled, e.ID)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrNotFound
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExportSchedule removes a schedule.
func (s *Store) DeleteExportSchedule(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM export_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordExportScheduleRun notes that the scheduler handled the window that
// ended at windowEnd (Unix seconds) with the given status.
func (s *Store) RecordExportScheduleRun(ctx context.Context, id string, windowEnd int64, status string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE export_schedules SET last_run_at = ?, last_status = ? WHERE id = ?`, windowEnd, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
