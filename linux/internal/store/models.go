package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sentinel-linux/internal/link"
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
	ONVIFURL      string `json:"onvifURL"`      // ONVIF device service; "" = added without ONVIF
	// CameraAnalytics subscribes to the camera's own detections (ONVIF
	// Profile M events) when ONVIFURL is set.
	CameraAnalytics bool  `json:"cameraAnalytics"`
	SortOrder       int   `json:"sortOrder"`
	CreatedAt       int64 `json:"createdAt"`
	// RecorderID is the recording server that has the camera ("local" =
	// the one in this process). Changed with SetCameraRecorder.
	RecorderID string `json:"recorderID"`
	// Audio event detection (audio.go, WAVE8(B)): the switch and the
	// threshold in dB over the noise floor (cameras.audio_detect and
	// cameras.audio_threshold_db).
	AudioDetect      bool `json:"audioDetect"`
	AudioThresholdDB int  `json:"audioThresholdDb"`
	// LoiterSeconds raises a Loitering event when a person's track stays
	// this long (cameras.loiter_seconds, WAVE16); 0 = off.
	LoiterSeconds int `json:"loiterSeconds"`
	// Recording modes (WAVE22, recording.go). RecordingMode is the setting
	// (default|continuous|events|dual|off); EffectiveRecordingMode is what
	// the recorder does (default resolved, dual without a sub-stream →
	// events) and Recording is EffectiveRecordingMode != off.
	RecordingMode          string `json:"recordingMode"`
	EffectiveRecordingMode string `json:"effectiveRecordingMode"`
	RecordTrigger          string `json:"recordTrigger"`       // default|motion|objects
	PreRollSeconds         *int   `json:"preRollSeconds"`      // nil (JSON null) = server default; a number (0 too) is literal
	PostRollSeconds        *int   `json:"postRollSeconds"`     // nil (JSON null) = server default; a number (0 too) is literal
	LowResRetentionDays    int    `json:"lowResRetentionDays"` // 0 = server default
	// Source (WAVE23): "rtsp" or "phone" (an iPhone pushing its video;
	// no RTSP/ONVIF fields). HasPushKey: a phone camera's push key is set.
	Source     string `json:"source"`
	HasPushKey bool   `json:"hasPushKey"`

	passwordEnc string
}

// IsPhone reports whether the camera is an iPhone that pushes its video.
func (c *Camera) IsPhone() bool { return c.Source == link.SourcePhone }

// OptInt is a JSON field with three states: absent (Set false: keep the
// stored value), null (Set true, Value nil: back to the server default) and a
// number (Set true, Value non-nil: that literal value, 0 included).
type OptInt struct {
	Set   bool
	Value *int
}

func (o *OptInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v int
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

func (o OptInt) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*o.Value)
}

// IntPtr is a number as an OptInt (a literal value).
func IntPtr(v int) OptInt { return OptInt{Set: true, Value: &v} }

const cameraCols = `id, name, location, rtsp_url, sub_rtsp_url, username, password_enc, recording, retention_days, sort_order, created_at, fps, motion_level, alert_on, onvif_url, camera_analytics`

// cameraSelectCols adds the columns that inserts leave to their defaults.
const cameraSelectCols = cameraCols + `, recorder_id, audio_detect, audio_threshold_db, loiter_seconds, recording_mode, record_trigger, pre_roll_s, post_roll_s, low_res_retention_days, source, push_key_hash != ''`

func scanCamera(row interface{ Scan(...any) error }) (*Camera, error) {
	c := &Camera{}
	var rec, analytics, audio, audioDB int
	var preRoll, postRoll sql.NullInt64
	if err := row.Scan(&c.ID, &c.Name, &c.Location, &c.RTSPURL, &c.SubRTSPURL, &c.Username, &c.passwordEnc, &rec, &c.RetentionDays, &c.SortOrder, &c.CreatedAt, &c.FPS, &c.MotionLevel, &c.AlertOn, &c.ONVIFURL, &analytics, &c.RecorderID, &audio, &audioDB, &c.LoiterSeconds,
		&c.RecordingMode, &c.RecordTrigger, &preRoll, &postRoll, &c.LowResRetentionDays, &c.Source, &c.HasPushKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Recording = rec == 1
	if preRoll.Valid {
		v := int(preRoll.Int64)
		c.PreRollSeconds = &v
	}
	if postRoll.Valid {
		v := int(postRoll.Int64)
		c.PostRollSeconds = &v
	}
	if c.RecordingMode == "" {
		c.RecordingMode = legacyRecordingMode(c.Recording)
	}
	if c.RecordTrigger == "" {
		c.RecordTrigger = link.TriggerDefault
	}
	c.CameraAnalytics = analytics == 1
	c.AudioDetect, c.AudioThresholdDB = DecodeAudio(audio, audioDB)
	c.HasPassword = c.passwordEnc != ""
	return c, nil
}

func (s *Store) Cameras(ctx context.Context) ([]Camera, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cameraSelectCols+` FROM cameras ORDER BY sort_order, created_at`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	d := s.RecordingDefaults(ctx)
	for i := range out {
		resolveRecording(&out[i], d)
	}
	return out, nil
}

func (s *Store) Camera(ctx context.Context, id string) (*Camera, error) {
	c, err := scanCamera(s.DB.QueryRowContext(ctx, `SELECT `+cameraSelectCols+` FROM cameras WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	resolveRecording(c, s.RecordingDefaults(ctx))
	return c, nil
}

// CameraInput is what the UI sends. A nil Password leaves the stored one alone;
// an empty string clears it.
type CameraInput struct {
	// RecorderID places a new camera on a recording server ("" = local).
	// Not settable from /api/v1 bodies; moves go through SetCameraRecorder.
	RecorderID string `json:"-"`

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
	ONVIFURL      *string `json:"onvifURL"`      // nil keeps the stored value
	// CameraAnalytics: nil keeps the stored value (default on).
	CameraAnalytics *bool `json:"cameraAnalytics"`
	// HomeKit: show in Apple Home; nil keeps the stored value. Persisted by
	// internal/server (homekit.go), not SaveCamera.
	HomeKit *bool `json:"homekit"`
	// AudioDetect and AudioThresholdDB: audio event detection (audio.go);
	// nil keeps the stored value. Persisted by SetCameraAudio after SaveCamera.
	AudioDetect      *bool `json:"audioDetect"`
	AudioThresholdDB *int  `json:"audioThresholdDb"`
	// LoiterSeconds: nil keeps the stored value (0 = off, up to
	// MaxLoiterSeconds). Persisted by SetCameraLoiter after SaveCamera.
	LoiterSeconds *int `json:"loiterSeconds"`
	// Recording modes (WAVE22): nil keeps the stored value (new cameras:
	// recordingMode from Recording, the others "default"/0). An explicit
	// RecordingMode wins over Recording; without it Recording false means
	// "off" and true turns an "off" camera to "default".
	RecordingMode       *string `json:"recordingMode"`
	RecordTrigger       *string `json:"recordTrigger"`
	PreRollSeconds      OptInt  `json:"preRollSeconds"`
	PostRollSeconds     OptInt  `json:"postRollSeconds"`
	LowResRetentionDays *int    `json:"lowResRetentionDays"`
	// Source (WAVE23) is set on new cameras only ("" = rtsp); a camera's
	// source never changes. Not settable from /api/v1 bodies.
	Source string `json:"-"`
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
		onvifURL, analytics := "", true
		if in.ONVIFURL != nil {
			onvifURL = *in.ONVIFURL
		}
		if in.CameraAnalytics != nil {
			analytics = *in.CameraAnalytics
		}
		_, err := s.DB.ExecContext(ctx, `INSERT INTO cameras(`+cameraCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, in.Name, in.Location, in.RTSPURL, in.SubRTSPURL, in.Username, enc, boolInt(in.Recording), in.RetentionDays, maxOrder.Int64+1, now(), in.FPS, level, alertOn, onvifURL, boolInt(analytics))
		if err != nil {
			return nil, err
		}
		recorder := in.RecorderID
		if recorder == "" {
			recorder = LocalRecorderID
		}
		if err := s.SetCameraRecorder(ctx, id, recorder); err != nil {
			return nil, err
		}
		if err := s.saveCameraRecording(ctx, id, in, ""); err != nil {
			return nil, err
		}
		if in.Source != "" && in.Source != link.SourceRTSP {
			if _, err := s.DB.ExecContext(ctx, `UPDATE cameras SET source = ? WHERE id = ?`, in.Source, id); err != nil {
				return nil, err
			}
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
	var analytics *int
	if in.CameraAnalytics != nil {
		v := boolInt(*in.CameraAnalytics)
		analytics = &v
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE cameras SET name=?, location=?, rtsp_url=?, sub_rtsp_url=?, username=?, password_enc=?, recording=?, retention_days=?, fps=COALESCE(NULLIF(?, 0), fps), motion_level=COALESCE(?, motion_level), alert_on=COALESCE(?, alert_on), onvif_url=COALESCE(?, onvif_url), camera_analytics=COALESCE(?, camera_analytics) WHERE id=?`,
		in.Name, in.Location, in.RTSPURL, in.SubRTSPURL, in.Username, enc, boolInt(in.Recording), in.RetentionDays, in.FPS, in.MotionLevel, in.AlertOn, in.ONVIFURL, analytics, id)
	if err != nil {
		return nil, err
	}
	if err := s.saveCameraRecording(ctx, id, in, existing.RecordingMode); err != nil {
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
