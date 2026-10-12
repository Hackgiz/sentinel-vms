package server

// Audit entries forwarded by recording servers (PLATFORM.md, "Audit and
// logs"): each joins the site chain with the server's id and its own chain
// hash (store.AppendForwardedAudit), so GET /api/v2/audit/verify checks
// both chains.

import (
	"context"
	"log/slog"

	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

// onForwardedAudit stores one entry of serverID's chain. An error leaves
// it in the recording server's outbox for the next replay.
func (s *Server) onForwardedAudit(ctx context.Context, serverID string, e link.AuditEntry) error {
	if serverID == "" || e.ID == "" || e.ChainHash == "" {
		// Never valid: refusing would hold up that server's outbox forever.
		slog.Error("dropped a malformed audit entry from a recording server", "server", serverID, "id", e.ID)
		return nil
	}
	return s.Store.AppendForwardedAudit(ctx, serverID, store.AuditEntry{
		ID: e.ID, Time: e.Time, User: e.User, Area: e.Area, Action: e.Action, Detail: e.Detail,
		PreviousHash: e.PreviousHash, ChainHash: e.ChainHash,
	})
}
