package recorder

// Video tokens (internal/videotoken). The management server signs them and
// sends its public keys in every config snapshot; the recorder keeps a
// verifier for tokens addressed to it. Phase 1 the management server is the
// video gateway and verifies tokens itself; phase 2 clients bring tokens
// straight here.

import (
	"crypto/ed25519"
	"log/slog"

	"sentinel-linux/internal/videotoken"
)

// tokenVerifier builds the verifier for a snapshot's keys. Keys that don't
// parse are logged and ignored, so one bad key can't lock every client out.
func (r *Recorder) tokenVerifier(encoded []string) *videotoken.Verifier {
	var keys []ed25519.PublicKey
	for _, e := range encoded {
		k, err := videotoken.ParsePublicKeys([]string{e})
		if err != nil {
			slog.Error("ignoring a video token key from the management server", "err", err)
			continue
		}
		keys = append(keys, k...)
	}
	return videotoken.NewVerifier(r.ServerID, keys...)
}

// VideoTokens verifies video tokens issued for this recorder (audience =
// ServerID) by the management server whose keys came in the last applied
// snapshot. Before any snapshot it refuses every token.
func (r *Recorder) VideoTokens() *videotoken.Verifier {
	r.cfgMu.RLock()
	v := r.tokens
	r.cfgMu.RUnlock()
	if v == nil {
		return videotoken.NewVerifier(r.ServerID)
	}
	return v
}
