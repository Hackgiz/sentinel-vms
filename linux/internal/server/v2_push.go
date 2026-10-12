package server

// API v2: push registration (api/openapi.yaml, tag "push"). Sentinel Mobile
// signs in with a v2 user, not a pairing token, so it registers its APNs
// device token here. The token is kept in the existing paired_devices table
// (the push queue, scope filtering and the dashboard's device list already
// read it): a row is keyed by its APNs token and owned by the signed-in
// user (paired_by), so alarms are pushed only for cameras that user may see.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Push(mux *http.ServeMux) {
	s.v2Route(mux, "PUT /api/v2/push/registration", auth.ViewLive, s.handleV2PushRegister)
	s.v2Route(mux, "DELETE /api/v2/push/registration", auth.ViewLive, s.handleV2PushUnregister)
}

func (s *Server) handleV2PushRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APNSToken  string `json:"apnsToken"`
		DeviceName string `json:"deviceName"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if !apnsTokenPattern.MatchString(in.APNSToken) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "apnsToken must be the device token as hex")
		return
	}
	name := strings.TrimSpace(in.DeviceName)
	if name == "" {
		name = "iOS Device"
	}
	if rs := []rune(name); len(rs) > 80 {
		name = string(rs[:80])
	}
	user := currentUser(r)
	ctx := r.Context()
	now := time.Now().Unix()
	// The same phone signing in as someone else takes the registration over.
	res, err := s.Store.DB.ExecContext(ctx,
		`UPDATE paired_devices SET paired_by = ?, name = ?, last_seen_at = ? WHERE apns_token = ?`,
		user.Name, name, now, in.APNSToken)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		sum := sha256.Sum256(raw) // never presented as a credential; only keeps token_hash unique
		_, err := s.Store.DB.ExecContext(ctx,
			`INSERT INTO paired_devices(id, name, token_hash, paired_by, paired_at, last_seen_at, apns_token) VALUES(?,?,?,?,?,?,?)`,
			store.NewID(), name, "v2:"+hex.EncodeToString(sum[:]), user.Name, now, now, in.APNSToken)
		if err != nil {
			writeV2Err(w, err)
			return
		}
		s.audit(r, "Devices", "Registered iPhone for push", name)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2PushUnregister(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("apnsToken")
	if !apnsTokenPattern.MatchString(token) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "apnsToken must be the device token as hex")
		return
	}
	// Only the owner's registrations; anything else is already "not registered".
	if _, err := s.Store.DB.ExecContext(r.Context(),
		`DELETE FROM paired_devices WHERE apns_token = ? AND paired_by = ?`, token, currentUser(r).Name); err != nil {
		writeV2Err(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
