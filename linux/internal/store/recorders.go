package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// LocalRecorderID is the recording server that runs inside this process
// (--role all). It always exists in recording_servers.
const LocalRecorderID = "local"

// RecordingServer is a registry entry (PLATFORM.md, "Control link").
type RecordingServer struct {
	ID              string
	Name            string
	PublicURL       string // where clients reach its video plane; "" = through the management server
	CertFingerprint string // SHA-256 of its client certificate, hex
	SealPublicKey   string // X25519, base64: camera credentials are sealed to it
	Version         string
	EnrolledAt      int64
	LastSeenAt      int64
	RevokedAt       int64 // 0 = active
	// RecordingsDir is the folder recordings go to on that server (an
	// absolute path there); "" = <its data directory>/recordings.
	RecordingsDir string
}

func (r *RecordingServer) Revoked() bool { return r.RevokedAt != 0 }

const recorderCols = `id, name, public_url, cert_fingerprint, seal_public_key, version, enrolled_at, last_seen_at, revoked_at, recordings_dir`

func scanRecorder(row interface{ Scan(...any) error }) (*RecordingServer, error) {
	r := &RecordingServer{}
	err := row.Scan(&r.ID, &r.Name, &r.PublicURL, &r.CertFingerprint, &r.SealPublicKey, &r.Version, &r.EnrolledAt, &r.LastSeenAt, &r.RevokedAt, &r.RecordingsDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) RecordingServers(ctx context.Context) ([]RecordingServer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+recorderCols+` FROM recording_servers ORDER BY id = 'local' DESC, enrolled_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecordingServer
	for rows.Next() {
		r, err := scanRecorder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) RecordingServer(ctx context.Context, id string) (*RecordingServer, error) {
	return scanRecorder(s.DB.QueryRowContext(ctx, `SELECT `+recorderCols+` FROM recording_servers WHERE id = ?`, id))
}

// RecordingServerByFingerprint finds an enrolled server by its client
// certificate (the control link's mTLS identity).
func (s *Store) RecordingServerByFingerprint(ctx context.Context, fp string) (*RecordingServer, error) {
	return scanRecorder(s.DB.QueryRowContext(ctx, `SELECT `+recorderCols+` FROM recording_servers WHERE cert_fingerprint = ? AND cert_fingerprint != ''`, fp))
}

// AddRecordingServer registers a newly enrolled server.
func (s *Store) AddRecordingServer(ctx context.Context, r *RecordingServer) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	r.EnrolledAt = now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO recording_servers(`+recorderCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.PublicURL, r.CertFingerprint, r.SealPublicKey, r.Version, r.EnrolledAt, r.LastSeenAt, r.RevokedAt, r.RecordingsDir)
	return err
}

// UpdateRecordingServer saves name, public URL, recordings folder,
// certificate, keys and version (not the enrolment or revocation times).
func (s *Store) UpdateRecordingServer(ctx context.Context, r *RecordingServer) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE recording_servers SET name = ?, public_url = ?, cert_fingerprint = ?, seal_public_key = ?, version = ?, recordings_dir = ? WHERE id = ?`,
		r.Name, r.PublicURL, r.CertFingerprint, r.SealPublicKey, r.Version, r.RecordingsDir, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchRecordingServer records that a server was heard from.
func (s *Store) TouchRecordingServer(ctx context.Context, id string, at time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE recording_servers SET last_seen_at = ? WHERE id = ?`, at.Unix(), id)
	return err
}

// RevokeRecordingServer refuses the server's certificate from now on. The
// local server can't be revoked.
func (s *Store) RevokeRecordingServer(ctx context.Context, id string) error {
	if id == LocalRecorderID {
		return errors.New("the local recording server can't be revoked")
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE recording_servers SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrHasCameras: a recording server still has cameras assigned to it.
var ErrHasCameras = errors.New("cameras are still assigned to this recording server")

// DeleteRecordingServer removes a revoked server from the registry. The
// local server can't be removed, nor can a server that is not revoked
// (ErrNotFound) or still has cameras (ErrHasCameras); move them first.
// Its history (camera placements, evidence) keeps its id.
func (s *Store) DeleteRecordingServer(ctx context.Context, id string) error {
	if id == LocalRecorderID {
		return errors.New("the local recording server can't be removed")
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cameras WHERE recorder_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrHasCameras
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM recording_servers WHERE id = ? AND revoked_at != 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MARK: - Enrollment tokens

// EnrollmentToken is a one-time secret an admin hands to a new recording
// server. Only its hash is stored.
type EnrollmentToken struct {
	Name      string
	CreatedBy string
	CreatedAt int64
	ExpiresAt int64
}

// CreateEnrollmentToken returns a new secret, valid for ttl.
func (s *Store) CreateEnrollmentToken(ctx context.Context, name, createdBy string, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	exp := time.Now().Add(ttl)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO enrollment_tokens(token_hash, name, created_by, created_at, expires_at) VALUES(?,?,?,?,?)`,
		hashToken(secret), name, createdBy, now(), exp.Unix())
	return secret, exp, err
}

// ConsumeEnrollmentToken burns a secret for serverID. ErrNotFound if it
// is unknown, expired or already used.
func (s *Store) ConsumeEnrollmentToken(ctx context.Context, secret, serverID string) (*EnrollmentToken, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t := &EnrollmentToken{}
	err = tx.QueryRowContext(ctx, `SELECT name, created_by, created_at, expires_at FROM enrollment_tokens
		WHERE token_hash = ? AND used_at = 0 AND expires_at > ?`, hashToken(secret), now()).Scan(&t.Name, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE enrollment_tokens SET used_at = ?, server_id = ? WHERE token_hash = ?`, now(), serverID, hashToken(secret)); err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

// MARK: - Camera placement

// SetCameraRecorder assigns a camera to a recording server and records the
// move, so footage recorded before it stays findable on the old server.
func (s *Store) SetCameraRecorder(ctx context.Context, cameraID, recorderID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT recorder_id FROM cameras WHERE id = ?`, cameraID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var open int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM camera_recorder_history WHERE camera_id = ? AND to_at = 0`, cameraID).Scan(&open)
	if current == recorderID && open > 0 {
		return nil
	}
	t := now()
	if _, err := tx.ExecContext(ctx, `UPDATE camera_recorder_history SET to_at = ? WHERE camera_id = ? AND to_at = 0`, t, cameraID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO camera_recorder_history(camera_id, recorder_id, from_at) VALUES(?,?,?)`, cameraID, recorderID, t); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cameras SET recorder_id = ? WHERE id = ?`, recorderID, cameraID); err != nil {
		return err
	}
	return tx.Commit()
}

// CameraPlacement is one stretch of time a camera was on a recording server.
type CameraPlacement struct {
	RecorderID string
	From, To   int64 // Unix s; To 0 = still there
}

// CameraPlacements lists where a camera recorded, oldest first (moves
// within one second: the current placement last).
func (s *Store) CameraPlacements(ctx context.Context, cameraID string) ([]CameraPlacement, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT recorder_id, from_at, to_at FROM camera_recorder_history WHERE camera_id = ? ORDER BY from_at, to_at = 0, to_at`, cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CameraPlacement
	for rows.Next() {
		var p CameraPlacement
		if err := rows.Scan(&p.RecorderID, &p.From, &p.To); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
