package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NewID returns a random RFC 4122 v4 UUID (uppercase, matching the Mac app's
// camera IDs so recordings and API paths look the same on both).
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}

// MARK: - Users

// Roles mirror the Mac app: Admin, Supervisor, Operator, Viewer.
var Roles = []string{"Admin", "Supervisor", "Operator", "Viewer"}

func ValidRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	PasswordHash string `json:"-"`
	CreatedAt    int64  `json:"createdAt"`
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, name, role, passwordHash string) (*User, error) {
	u := &User{ID: NewID(), Name: strings.TrimSpace(name), Role: role, PasswordHash: passwordHash, CreatedAt: now()}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO users(id, name, role, password_hash, created_at) VALUES(?,?,?,?,?)`,
		u.ID, u.Name, u.Role, u.PasswordHash, u.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, fmt.Errorf("a user named %q already exists", u.Name)
	}
	return u, err
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	if err := row.Scan(&u.ID, &u.Name, &u.Role, &u.PasswordHash, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

const userCols = `id, name, role, password_hash, created_at`

func (s *Store) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name = ?`, strings.TrimSpace(name)))
}

func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'Admin'`).Scan(&n)
	return n, err
}

// MARK: - Sessions (only a SHA-256 of the token is stored)

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, expires_at) VALUES(?,?,?)`,
		hashToken(token), userID, time.Now().Add(ttl).Unix())
	return token, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT u.id, u.name, u.role, u.password_hash, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), now()))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) PruneSessions(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now())
}

// MARK: - Cameras

type Camera struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Location      string `json:"location"`
	RTSPURL       string `json:"rtspURL"`    // without credentials
	SubRTSPURL    string `json:"subRTSPURL"` // optional low-res stream for viewing
	Username      string `json:"username"`
	HasPassword   bool   `json:"hasPassword"`
	Recording     bool   `json:"recording"`
	RetentionDays int    `json:"retentionDays"` // 0 = global default
	FPS           int    `json:"fps"`           // as reported by ONVIF; 0 = unknown
	MotionLevel   int    `json:"motionLevel"`   // 0 off, 1 low, 2 medium, 3 high
	AlertOn       int    `json:"alertOn"`       // 0 auto, 1 any motion, 2 people & vehicles, 3 people only
	SortOrder     int    `json:"sortOrder"`
	CreatedAt     int64  `json:"createdAt"`

	passwordEnc string
}

const cameraCols = `id, name, location, rtsp_url, sub_rtsp_url, username, password_enc, recording, retention_days, sort_order, created_at, fps, motion_level, alert_on`

func scanCamera(row interface{ Scan(...any) error }) (*Camera, error) {
	c := &Camera{}
	var rec int
	if err := row.Scan(&c.ID, &c.Name, &c.Location, &c.RTSPURL, &c.SubRTSPURL, &c.Username, &c.passwordEnc, &rec, &c.RetentionDays, &c.SortOrder, &c.CreatedAt, &c.FPS, &c.MotionLevel, &c.AlertOn); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Recording = rec == 1
	c.HasPassword = c.passwordEnc != ""
	return c, nil
}

func (s *Store) Cameras(ctx context.Context) ([]Camera, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cameraCols+` FROM cameras ORDER BY sort_order, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Camera{}
	for rows.Next() {
		c, err := scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) Camera(ctx context.Context, id string) (*Camera, error) {
	return scanCamera(s.DB.QueryRowContext(ctx, `SELECT `+cameraCols+` FROM cameras WHERE id = ?`, id))
}

// CameraInput is what the UI sends. A nil Password leaves the stored one alone;
// an empty string clears it.
type CameraInput struct {
	Name          string  `json:"name"`
	Location      string  `json:"location"`
	RTSPURL       string  `json:"rtspURL"`
	SubRTSPURL    string  `json:"subRTSPURL"`
	Username      string  `json:"username"`
	Password      *string `json:"password"`
	Recording     bool    `json:"recording"`
	RetentionDays int     `json:"retentionDays"`
	FPS           int     `json:"fps,omitempty"` // 0 keeps the stored value
	MotionLevel   *int    `json:"motionLevel"`   // nil keeps the stored value (default medium)
	AlertOn       *int    `json:"alertOn"`       // nil keeps the stored value (default auto)
}

func (s *Store) SaveCamera(ctx context.Context, id string, in CameraInput) (*Camera, error) {
	if id == "" {
		var maxOrder sql.NullInt64
		_ = s.DB.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM cameras`).Scan(&maxOrder)
		id = NewID()
		enc := ""
		if in.Password != nil {
			var err error
			if enc, err = s.Seal(*in.Password); err != nil {
				return nil, err
			}
		}
		level := 2
		if in.MotionLevel != nil {
			level = *in.MotionLevel
		}
		alertOn := 0
		if in.AlertOn != nil {
			alertOn = *in.AlertOn
		}
		_, err := s.DB.ExecContext(ctx, `INSERT INTO cameras(`+cameraCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, in.Name, in.Location, in.RTSPURL, in.SubRTSPURL, in.Username, enc, boolInt(in.Recording), in.RetentionDays, maxOrder.Int64+1, now(), in.FPS, level, alertOn)
		if err != nil {
			return nil, err
		}
		return s.Camera(ctx, id)
	}
	existing, err := s.Camera(ctx, id)
	if err != nil {
		return nil, err
	}
	enc := existing.passwordEnc
	if in.Password != nil {
		if enc, err = s.Seal(*in.Password); err != nil {
			return nil, err
		}
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE cameras SET name=?, location=?, rtsp_url=?, sub_rtsp_url=?, username=?, password_enc=?, recording=?, retention_days=?, fps=COALESCE(NULLIF(?, 0), fps), motion_level=COALESCE(?, motion_level), alert_on=COALESCE(?, alert_on) WHERE id=?`,
		in.Name, in.Location, in.RTSPURL, in.SubRTSPURL, in.Username, enc, boolInt(in.Recording), in.RetentionDays, in.FPS, in.MotionLevel, in.AlertOn, id)
	if err != nil {
		return nil, err
	}
	return s.Camera(ctx, id)
}

func (s *Store) DeleteCamera(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM cameras WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CameraPassword decrypts a camera's stored password (for building source URLs).
func (s *Store) CameraPassword(c *Camera) (string, error) { return s.Unseal(c.passwordEnc) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// MARK: - Audit log (hash-chained, same scheme as the Mac app)

type AuditEntry struct {
	ID           string `json:"id"`
	Time         int64  `json:"time"`
	User         string `json:"user"`
	Area         string `json:"area"`
	Action       string `json:"action"`
	Detail       string `json:"detail"`
	PreviousHash string `json:"previousHash"`
	ChainHash    string `json:"chainHash"`
}

func (e AuditEntry) computeHash() string {
	payload := strings.Join([]string{e.PreviousHash, e.ID, fmt.Sprintf("%d", e.Time), e.User, e.Area, e.Action, e.Detail}, "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Audit appends a chained entry. Errors are returned but callers usually log
// and continue — an audit failure must not block the operator's action.
func (s *Store) Audit(ctx context.Context, user, area, action, detail string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prev string
	if err := tx.QueryRowContext(ctx, `SELECT chain_hash FROM audit ORDER BY seq DESC LIMIT 1`).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e := AuditEntry{ID: NewID(), Time: now(), User: user, Area: area, Action: action, Detail: detail, PreviousHash: prev}
	e.ChainHash = e.computeHash()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit(id, time, user, area, action, detail, previous_hash, chain_hash) VALUES(?,?,?,?,?,?,?,?)`,
		e.ID, e.Time, e.User, e.Area, e.Action, e.Detail, e.PreviousHash, e.ChainHash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, time, user, area, action, detail, previous_hash, chain_hash FROM audit ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// VerifyAudit walks the whole chain oldest→newest; returns the ID of the first
// entry that doesn't verify, or "" when intact.
func (s *Store) VerifyAudit(ctx context.Context) (checked int, brokenID string, err error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, time, user, area, action, detail, previous_hash, chain_hash FROM audit ORDER BY seq ASC`)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	prev := ""
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Time, &e.User, &e.Area, &e.Action, &e.Detail, &e.PreviousHash, &e.ChainHash); err != nil {
			return checked, "", err
		}
		if e.PreviousHash != prev || e.computeHash() != e.ChainHash {
			return checked, e.ID, nil
		}
		prev = e.ChainHash
		checked++
	}
	return checked, "", rows.Err()
}
