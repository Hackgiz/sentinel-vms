package store

// Device-code sign-in (WAVE10-APPLE.md, Stream C; RFC 8628 style) for
// clients without a keyboard worth typing a password on (Sentinel TV).
//
// A device asks for a code pair: a short user code the person types into
// the dashboard's /link page and a long device code only the device knows.
// Both are stored as SHA-256 hashes in device_codes. A signed-in user
// approves the user code; the device then redeems its device code exactly
// once for an API token pair. Tokens issued this way belong to a token
// family whose id starts with DeviceTokenFamilyPrefix, and every lookup of
// such a token caps the user's role at DeviceCappedRole: the device can
// watch, never manage.
//
// Times: created_at and expires_at are Unix seconds like the rest of the
// schema; last_poll_at is Unix milliseconds (the poll interval check needs
// sub-second resolution).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// DeviceTokenFamilyPrefix marks API token families issued by a device
	// code. Family ids are generated here, never taken from a client.
	DeviceTokenFamilyPrefix = "devc_"
	// DeviceCappedRole is the most a device-code token may do.
	DeviceCappedRole = "Viewer"
	// UserCodeAlphabet has no vowels (no words) and nothing that reads
	// like something else: no 0/O, 1/I/L, 8/B (neither of each pair).
	UserCodeAlphabet = "CDFGHJKMNPQRSTVWXZ2345679"
	// UserCodeLength is the number of significant characters in a user code.
	UserCodeLength = 8
)

// Device code states.
const (
	DeviceCodePending  = "pending"
	DeviceCodeApproved = "approved"
	DeviceCodeDenied   = "denied"
	DeviceCodeRedeemed = "redeemed"
)

// ErrDeviceCodeExpired: the code is past its expiry (or was already used).
var ErrDeviceCodeExpired = errors.New("device code expired")

// DeviceCode is one row of device_codes (hashes never leave the store).
type DeviceCode struct {
	ID         string
	DeviceName string
	Status     string
	ApprovedBy string // user id, once approved
	CreatedAt  int64  // Unix seconds
	ExpiresAt  int64  // Unix seconds
	LastPollAt int64  // Unix ms; 0 = never polled
}

// Expired reports whether the code is past its expiry at t.
func (d *DeviceCode) Expired(t time.Time) bool { return t.Unix() >= d.ExpiresAt }

// CapDeviceTokenUser applies the device-code role cap to u when family is
// a device-code token family.
func CapDeviceTokenUser(family string, u *User) {
	if u != nil && strings.HasPrefix(family, DeviceTokenFamilyPrefix) {
		u.Role = DeviceCappedRole
	}
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// capDeviceToken applies the cap only while the user's current role still
// holds everything the capped role grants (viewLive). Otherwise it returns
// ErrNotFound: the cap may lower a role, never raise one, so a user later
// moved to a role without live view loses the device as well.
func capDeviceToken(ctx context.Context, q rowQuerier, family string, u *User) error {
	if u == nil || !strings.HasPrefix(family, DeviceTokenFamilyPrefix) {
		return nil
	}
	ok, err := roleHasViewLive(ctx, q, u.Role)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	CapDeviceTokenUser(family, u)
	return nil
}

// roleHasViewLive: every built-in role views live; a custom role must list
// viewLive in its stored permissions. An unknown role has nothing.
func roleHasViewLive(ctx context.Context, q rowQuerier, role string) (bool, error) {
	for _, b := range Roles {
		if b == role {
			return true, nil
		}
	}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE name = ? COLLATE NOCASE`, role).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var perms []string
	if json.Unmarshal([]byte(raw), &perms) != nil {
		return false, nil
	}
	for _, p := range perms {
		if p == "viewLive" {
			return true, nil
		}
	}
	return false, nil
}

// NormalizeUserCode upper-cases a typed user code and drops separators and
// spaces ("bcdf-ghjk" → "BCDFGHJK"). It returns "" when what is left isn't
// a well-formed code.
func NormalizeUserCode(typed string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(typed) {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case strings.ContainsRune(UserCodeAlphabet, r):
			b.WriteRune(r)
		default:
			return ""
		}
	}
	if b.Len() != UserCodeLength {
		return ""
	}
	return b.String()
}

// FormatUserCode shows a normalized code as "BCDF-GHJK".
func FormatUserCode(code string) string {
	if len(code) != UserCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func newUserCode() (string, error) {
	raw := make([]byte, UserCodeLength)
	out := make([]byte, UserCodeLength)
	n := byte(len(UserCodeAlphabet))
	limit := 256 - 256%int(n) // reject bytes that would bias the draw
	for i := 0; i < UserCodeLength; {
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		for _, c := range raw {
			if int(c) >= limit || i == UserCodeLength {
				continue
			}
			out[i] = UserCodeAlphabet[c%n]
			i++
		}
	}
	return string(out), nil
}

// CreateDeviceCode starts a device sign-in. It returns the normalized user
// code and the device code in clear text once; only hashes are stored.
func (s *Store) CreateDeviceCode(ctx context.Context, deviceName string, ttl time.Duration) (userCode, deviceCode string, dc *DeviceCode, err error) {
	deviceCode, err = newOpaqueToken("sdc_")
	if err != nil {
		return "", "", nil, err
	}
	t := time.Now()
	dc = &DeviceCode{ID: NewID(), DeviceName: deviceName, Status: DeviceCodePending, CreatedAt: t.Unix(), ExpiresAt: t.Add(ttl).Unix()}
	// Drop long-dead rows first so the table stays small.
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM device_codes WHERE expires_at < ?`, t.Add(-24*time.Hour).Unix())
	for attempt := 0; attempt < 5; attempt++ {
		if userCode, err = newUserCode(); err != nil {
			return "", "", nil, err
		}
		_, err = s.DB.ExecContext(ctx, `INSERT INTO device_codes(id, user_code_hash, device_code_hash, device_name, status, created_at, expires_at)
			VALUES(?,?,?,?,?,?,?)`, dc.ID, hashToken(userCode), hashToken(deviceCode), deviceName, DeviceCodePending, dc.CreatedAt, dc.ExpiresAt)
		if err == nil {
			return userCode, deviceCode, dc, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return "", "", nil, err
		}
		// A live user code collided: draw another.
	}
	return "", "", nil, err
}

const deviceCodeCols = `id, device_name, status, approved_by, created_at, expires_at, last_poll_at`

func scanDeviceCode(row interface{ Scan(...any) error }) (*DeviceCode, error) {
	d := &DeviceCode{}
	if err := row.Scan(&d.ID, &d.DeviceName, &d.Status, &d.ApprovedBy, &d.CreatedAt, &d.ExpiresAt, &d.LastPollAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return d, nil
}

// PendingDeviceCode finds a pending, unexpired code by its normalized user
// code. Anything else (unknown, used, expired) is ErrNotFound, so a guess
// learns nothing.
func (s *Store) PendingDeviceCode(ctx context.Context, userCode string) (*DeviceCode, error) {
	if userCode == "" {
		return nil, ErrNotFound
	}
	return scanDeviceCode(s.DB.QueryRowContext(ctx, `SELECT `+deviceCodeCols+` FROM device_codes
		WHERE user_code_hash = ? AND status = ? AND expires_at > ?`, hashToken(userCode), DeviceCodePending, now()))
}

// DecideDeviceCode approves (or denies) a pending code on behalf of userID.
// A code is decided once: a second decision is ErrNotFound.
func (s *Store) DecideDeviceCode(ctx context.Context, userCode, userID string, approve bool) (*DeviceCode, error) {
	d, err := s.PendingDeviceCode(ctx, userCode)
	if err != nil {
		return nil, err
	}
	status := DeviceCodeDenied
	if approve {
		status = DeviceCodeApproved
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE device_codes SET status = ?, approved_by = ? WHERE id = ? AND status = ? AND expires_at > ?`,
		status, userID, d.ID, DeviceCodePending, now())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	d.Status, d.ApprovedBy = status, userID
	return d, nil
}

// PollDeviceCode records a poll of deviceCode and returns the code as it
// was before this poll (LastPollAt is the previous poll, for the
// interval check). Unknown codes are ErrNotFound.
func (s *Store) PollDeviceCode(ctx context.Context, deviceCode string) (*DeviceCode, error) {
	if deviceCode == "" {
		return nil, ErrNotFound
	}
	h := hashToken(deviceCode)
	d, err := scanDeviceCode(s.DB.QueryRowContext(ctx, `SELECT `+deviceCodeCols+` FROM device_codes WHERE device_code_hash = ?`, h))
	if err != nil {
		return nil, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE device_codes SET last_poll_at = ? WHERE id = ?`, time.Now().UnixMilli(), d.ID)
	return d, err
}

// RedeemDeviceCode turns an approved code into a token pair for the user
// who approved it, exactly once: the approved → redeemed flip and the new
// token family commit together. A code that isn't approved any more (used,
// expired, raced) is ErrDeviceCodeExpired.
func (s *Store) RedeemDeviceCode(ctx context.Context, id, client string, accessTTL, refreshTTL time.Duration) (TokenPair, *User, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TokenPair{}, nil, err
	}
	defer tx.Rollback()
	var userID string
	err = tx.QueryRowContext(ctx, `SELECT approved_by FROM device_codes WHERE id = ? AND status = ? AND expires_at > ?`,
		id, DeviceCodeApproved, now()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenPair{}, nil, ErrDeviceCodeExpired
	}
	if err != nil {
		return TokenPair{}, nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE device_codes SET status = ? WHERE id = ? AND status = ?`, DeviceCodeRedeemed, id, DeviceCodeApproved)
	if err != nil {
		return TokenPair{}, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return TokenPair{}, nil, ErrDeviceCodeExpired
	}
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, userID))
	if err != nil {
		return TokenPair{}, nil, err
	}
	p, err := newPair(accessTTL, refreshTTL)
	if err != nil {
		return p, nil, err
	}
	p.FamilyID = DeviceTokenFamilyPrefix + NewID()
	n := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO api_tokens(id, family_id, user_id, client, access_hash, access_expires_at, refresh_hash, refresh_expires_at, created_at, used_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, NewID(), p.FamilyID, u.ID, client, hashToken(p.AccessToken), p.AccessExpiresAt.Unix(),
		hashToken(p.RefreshToken), p.RefreshExpiresAt.Unix(), n, n); err != nil {
		return p, nil, err
	}
	if err := tx.Commit(); err != nil {
		return p, nil, err
	}
	CapDeviceTokenUser(p.FamilyID, u)
	return p, u, nil
}
