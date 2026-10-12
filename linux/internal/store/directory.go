package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Directory users (PLATFORM.md, "Permissions"): users signed in through
// LDAP/Active Directory or OIDC are provisioned on first sign-in with
// source "ldap" or "oidc" and the directory's stable id in external_id.
// Their password hash is unusable, so only the directory can sign them in.

// User sources.
const (
	SourceLocal = "local"
	SourceLDAP  = "ldap"
	SourceOIDC  = "oidc"
)

// ErrNameTaken: a provisioned user's name belongs to another user.
var ErrNameTaken = errors.New("a different user already has that name")

// UserSource is where a user signs in: SourceLocal, SourceLDAP or SourceOIDC.
func (s *Store) UserSource(ctx context.Context, userID string) (string, error) {
	var src string
	err := s.DB.QueryRowContext(ctx, `SELECT source FROM users WHERE id = ?`, userID).Scan(&src)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return src, err
}

// DirectoryUser is the user provisioned for source and externalID.
func (s *Store) DirectoryUser(ctx context.Context, source, externalID string) (*User, error) {
	if externalID == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE source = ? AND external_id = ?`, source, externalID))
}

// unusablePasswordHash never matches a password (it isn't bcrypt).
func unusablePasswordHash() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "!directory:" + hex.EncodeToString(b)
}

// ProvisionDirectoryUser finds the user with source and externalID, or
// creates them named name, and sets their role and scope. created reports
// a new user. A new user whose name another user has fails with
// ErrNameTaken; an existing one keeps their name.
func (s *Store) ProvisionDirectoryUser(ctx context.Context, source, externalID, name, role string, scope UserScope) (u *User, created bool, err error) {
	if source != SourceLDAP && source != SourceOIDC {
		return nil, false, fmt.Errorf("unknown user source %q", source)
	}
	if externalID == "" || !ValidRole(role) {
		return nil, false, errors.New("directory user needs an id and a valid role")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE source = ? AND external_id = ?`, source, externalID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id, created = NewID(), true
		_, err = tx.ExecContext(ctx, `INSERT INTO users(id, name, role, password_hash, created_at, source, external_id) VALUES(?,?,?,?,?,?,?)`,
			id, strings.TrimSpace(name), role, unusablePasswordHash(), now(), source, externalID)
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return nil, false, ErrNameTaken
		}
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
	}
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if err := s.SetUserScope(ctx, id, scope); err != nil {
		return nil, false, err
	}
	u, err = s.UserByID(ctx, id)
	return u, created, err
}
