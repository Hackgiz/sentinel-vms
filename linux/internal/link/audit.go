package link

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// Audit entries and the system log (PLATFORM.md, "Audit and logs").
//
// A recording server keeps its own hash chain of the actions it takes
// (evidence and export files, applied configuration, expired events) and
// forwards every entry, in chain order, as an EventAudit through the same
// outbox as its other events. The management server appends it to the
// site chain and keeps the entry's ChainHash, so both chains verify.

// EventAudit carries one entry of the recording server's audit chain.
const EventAudit = "audit"

// AuditEntry is one entry as the recording server chained it. The hash is
// the same on both sides (store.AuditEntry): SHA-256 over PreviousHash, ID,
// Time, User, Area, Action and Detail joined by 0x1f.
type AuditEntry struct {
	ID           string `json:"id"`
	Time         int64  `json:"time"` // Unix s
	User         string `json:"user"`
	Area         string `json:"area"`
	Action       string `json:"action"`
	Detail       string `json:"detail"`
	PreviousHash string `json:"previousHash"`
	ChainHash    string `json:"chainHash"`
}

// Hash is the chain hash of e after e.PreviousHash.
func (e AuditEntry) Hash() string {
	payload := strings.Join([]string{e.PreviousHash, e.ID, strconv.FormatInt(e.Time, 10), e.User, e.Area, e.Action, e.Detail}, "\x1f")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// CmdLog fetches the recording server's recent system log (LogArgs →
// LogResult), secrets already stripped.
const CmdLog = "log.recent"

// LogMaxLines bounds a log fetch (and the lines a server keeps).
const LogMaxLines = 2000

type LogArgs struct {
	Lines int `json:"lines"` // 1…LogMaxLines
}

type LogResult struct {
	Text string `json:"text"` // oldest first, one line per log record
}
