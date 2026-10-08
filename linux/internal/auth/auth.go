// Package auth holds password hashing, role permissions and login throttling.
package auth

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const MinPasswordLength = 8

func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength {
		return "", errors.New("password must be at least 8 characters")
	}
	if len(password) > 72 {
		return "", errors.New("password must be at most 72 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash lets a login for an unknown user take as long as a real one, so
// response timing doesn't reveal which usernames exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("sentinel-timing-equalizer"), bcrypt.DefaultCost)

func EqualizeTiming(password string) { _ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password)) }

// Permission mirrors the Mac app's OperatorPermission.
type Permission int

const (
	ManageCameras Permission = iota
	ManageUsers
	ChangeSettings
	ViewAudit
	AcknowledgeAlarms
	ExportEvidence
	ViewLive
)

func Can(role string, p Permission) bool {
	switch p {
	case ManageCameras, ManageUsers, ChangeSettings:
		return role == "Admin"
	case ViewAudit:
		return role == "Admin" || role == "Supervisor"
	case AcknowledgeAlarms, ExportEvidence:
		return role != "Viewer"
	case ViewLive:
		return true
	}
	return false
}

// Limiter locks a key (username or client IP) for LockFor after MaxFailures
// failed attempts inside Window.
type Limiter struct {
	MaxFailures int
	Window      time.Duration
	LockFor     time.Duration

	mu      sync.Mutex
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	failures    int
	first       time.Time
	lockedUntil time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{MaxFailures: 5, Window: 15 * time.Minute, LockFor: 15 * time.Minute, entries: map[string]*limiterEntry{}}
}

// Locked reports whether any of the keys is currently locked out.
func (l *Limiter) Locked(keys ...string) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if e := l.entries[k]; e != nil && now.Before(e.lockedUntil) {
			return true, e.lockedUntil
		}
	}
	return false, time.Time{}
}

func (l *Limiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		e := l.entries[k]
		if e == nil || now.Sub(e.first) > l.Window {
			e = &limiterEntry{first: now}
			l.entries[k] = e
		}
		e.failures++
		if e.failures >= l.MaxFailures {
			e.lockedUntil = now.Add(l.LockFor)
			e.failures = 0
			e.first = now
		}
	}
	// Bound memory against a flood of distinct keys.
	if len(l.entries) > 10_000 {
		for k, e := range l.entries {
			if now.After(e.lockedUntil) && now.Sub(e.first) > l.Window {
				delete(l.entries, k)
			}
		}
	}
}

func (l *Limiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.entries, k)
	}
}
