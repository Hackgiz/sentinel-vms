package server

// API v2: recording servers (api/openapi.yaml, tag "servers"): the
// registry with each server's live health, renaming, the public URL
// clients reach its video plane at, revoking, and enrollment tokens for
// new servers. /recording-servers/{serverId}/log is in v2_server_log.go.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

const (
	enrollmentTokenTTL = time.Hour
	storageInfoTimeout = 5 * time.Second
)

func (s *Server) registerV2Servers(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/recording-servers", auth.ViewLive, s.handleV2RecordingServers)
	s.v2Route(mux, "GET /api/v2/recording-servers/{serverId}", auth.ViewLive, s.handleV2RecordingServer)
	s.v2Route(mux, "PATCH /api/v2/recording-servers/{serverId}", auth.ManageCameras, s.handleV2PatchRecordingServer)
	s.v2Route(mux, "DELETE /api/v2/recording-servers/{serverId}", auth.ManageCameras, s.handleV2RevokeRecordingServer) // ?remove=true removes a revoked one
	s.v2Route(mux, "POST /api/v2/recording-servers/enrollment-tokens", auth.ManageCameras, s.handleV2EnrollmentToken)
	s.v2Route(mux, "GET /api/v2/recording-servers/{serverId}/log", auth.ViewAudit, s.handleV2RecordingServerLog)            // v2_server_log.go
	s.v2Route(mux, "POST /api/v2/recording-servers/{serverId}/discover", auth.ManageCameras, s.handleV2Discover)            // v2_discover.go
	s.v2Route(mux, "POST /api/v2/recording-servers/{serverId}/discover/probe", auth.ManageCameras, s.handleV2DiscoverProbe) // v2_discover.go
}

type v2RecordingServer struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Local         bool                 `json:"local"`
	PublicURL     string               `json:"publicUrl"`
	EnrolledAt    int64                `json:"enrolledAt"`
	LastSeenAt    int64                `json:"lastSeenAt"`
	Revoked       bool                 `json:"revoked"`
	Online        bool                 `json:"online"`
	Storage       *v2ServerStorage     `json:"storage,omitempty"`
	RecordingsDir string               `json:"recordingsDir"` // configured; "" = <its data directory>/recordings
	Version       string               `json:"version"`
	ConfigRev     int64                `json:"configRev"`
	MediaRunning  bool                 `json:"mediaRunning"`
	MediaError    string               `json:"mediaError,omitempty"`
	Cameras       int                  `json:"cameras"`
	Capabilities  v2ServerCapabilities `json:"capabilities"`
}

type v2ServerStorage struct {
	RecordingsDir   string  `json:"recordingsDir,omitempty"` // the folder in use right now
	DiskFreeBytes   *uint64 `json:"diskFreeBytes,omitempty"`
	DiskTotalBytes  *uint64 `json:"diskTotalBytes,omitempty"`
	RecordingsBytes int64   `json:"recordingsBytes"`
}

type v2ServerCapabilities struct {
	Detection bool `json:"detection"`
	Objects   bool `json:"objects"`
	WebRTC    bool `json:"webrtc"`
}

// v2RecordingServerView combines a registry entry with the server's live
// status (or its last report when offline) and, when online, its storage.
// cams counts the cameras assigned to each server.
func (s *Server) v2RecordingServerView(ctx context.Context, rs *store.RecordingServer, cams map[string]int) v2RecordingServer {
	st, online := s.statusOf(rs.ID)
	v := v2RecordingServer{
		ID: rs.ID, Name: rs.Name, Local: rs.ID == store.LocalRecorderID, PublicURL: rs.PublicURL,
		EnrolledAt: secToMs(rs.EnrolledAt), LastSeenAt: secToMs(rs.LastSeenAt), Revoked: rs.Revoked(), Online: online,
		Version: rs.Version, ConfigRev: st.ConfigRev, MediaRunning: st.Media.Running, MediaError: st.Media.Error,
		RecordingsDir: rs.RecordingsDir,
		Cameras:       cams[rs.ID],
		Capabilities:  v2ServerCapabilities{Detection: st.Capabilities.Detection, Objects: st.Capabilities.Objects, WebRTC: st.Capabilities.WebRTC.Enabled},
	}
	if online {
		v.LastSeenAt = time.Now().UnixMilli()
		if st.Version != "" {
			v.Version = st.Version
		}
		cctx, cancel := context.WithTimeout(ctx, storageInfoTimeout)
		defer cancel()
		var info link.StorageInfo
		if err := s.command(cctx, rs.ID, link.CmdStorageInfo, nil, &info); err == nil {
			v.Storage = &v2ServerStorage{RecordingsDir: info.RecordingsDir, RecordingsBytes: info.RecordingsBytes}
			if info.HasDisk {
				v.Storage.DiskFreeBytes, v.Storage.DiskTotalBytes = &info.DiskFreeBytes, &info.DiskTotalBytes
			}
		}
	}
	return v
}

// camerasPerRecorder counts the cameras assigned to each recording server.
func (s *Server) camerasPerRecorder(ctx context.Context) (map[string]int, error) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, c := range cams {
		n[c.RecorderID]++
	}
	return n, nil
}

// handleV2RecordingServers lists the registry, the local server first,
// offline and revoked servers included. The local entry (which always
// exists) is left out when no local recording server runs and no camera
// is on it (--role management).
func (s *Server) handleV2RecordingServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.RecordingServers(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	cams, err := s.camerasPerRecorder(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	var shown []store.RecordingServer
	for _, rs := range list {
		if rs.ID == store.LocalRecorderID && !s.recorderConnected(rs.ID) && cams[rs.ID] == 0 {
			continue
		}
		shown = append(shown, rs)
	}
	// Online servers answer storage queries in parallel.
	out := make([]v2RecordingServer, len(shown))
	var wg sync.WaitGroup
	for i := range shown {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = s.v2RecordingServerView(r.Context(), &shown[i], cams)
		}(i)
	}
	wg.Wait()
	v2Items(w, out)
}

func (s *Server) handleV2RecordingServer(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.RecordingServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}
	cams, err := s.camerasPerRecorder(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.v2RecordingServerView(r.Context(), rs, cams))
}

// validPublicURL checks a recording server's public URL: "" (through the
// management server) or an http(s) base URL without credentials, query or
// fragment. It returns the URL without a trailing slash.
func validPublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", inputError{`publicUrl must look like "https://rs2.lan:8090" (or "" to go through this server)`}
	}
	return strings.TrimRight(raw, "/"), nil
}

func (s *Server) handleV2PatchRecordingServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          *string `json:"name"`
		PublicURL     *string `json:"publicUrl"`
		RecordingsDir *string `json:"recordingsDir"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	rs, err := s.Store.RecordingServer(r.Context(), r.PathValue("serverId"))
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}
	var changes []string
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || len([]rune(name)) > 100 {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "name must be 1 to 100 characters")
			return
		}
		if name != rs.Name {
			changes = append(changes, "name "+rs.Name+" → "+name)
			rs.Name = name
		}
	}
	if in.PublicURL != nil {
		pub, err := validPublicURL(*in.PublicURL)
		if err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
		if pub != rs.PublicURL {
			changes = append(changes, "public URL ["+rs.PublicURL+"] → ["+pub+"]")
			rs.PublicURL = pub
		}
	}
	oldDir, dirChanged := rs.RecordingsDir, false
	if in.RecordingsDir != nil {
		dir, err := validRecordingsDir(*in.RecordingsDir)
		if err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
		if dir != rs.RecordingsDir {
			if rs.ID == store.LocalRecorderID && dir != "" {
				if err := probeFolder(dir); err != nil {
					writeV2Error(w, http.StatusBadRequest, codeBadRequest, "recordings folder: "+err.Error())
					return
				}
			}
			changes = append(changes, "recordings folder ["+rs.RecordingsDir+"] → ["+dir+"]")
			rs.RecordingsDir, dirChanged = dir, true
		}
	}
	if len(changes) > 0 {
		if err := s.Store.UpdateRecordingServer(r.Context(), rs); err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
		s.audit(r, "Servers", "Edited recording server", rs.ID+": "+strings.Join(changes, ", "))
	}
	if dirChanged {
		// The server switches at once when connected (offline ones take
		// it when they reconnect). The local server reports a folder it
		// can't use right away; undo so the page shows what is in effect.
		if err := s.applyRecordingsDir(r.Context(), rs.ID); err != nil && rs.ID == store.LocalRecorderID {
			rs.RecordingsDir = oldDir
			_ = s.Store.UpdateRecordingServer(r.Context(), rs)
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	cams, err := s.camerasPerRecorder(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.v2RecordingServerView(r.Context(), rs, cams))
}

// validRecordingsDir cleans a recordings folder setting: "" (the default)
// or an absolute path.
func validRecordingsDir(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", nil
	}
	if strings.ContainsRune(dir, 0) || len(dir) > 1000 {
		return "", inputError{"recordings folder: not a valid path"}
	}
	if !filepath.IsAbs(dir) {
		return "", inputError{"recordings folder must be an absolute path, like /mnt/video"}
	}
	return filepath.Clean(dir), nil
}

// probeFolder creates the folder if needed and checks it is writable.
func probeFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sentinel-probe-*")
	if err != nil {
		return errors.New("not writable: " + err.Error() + sandboxHint(dir, err))
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// applyRecordingsDir pushes the server's configuration so it switches
// folders, and reports the switch failing on the server itself.
func (s *Server) applyRecordingsDir(ctx context.Context, id string) error {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	applied, err := s.recByID(id).ApplyConfig(ctx, s.snapshot(ctx, id, cams))
	if err != nil {
		return err
	}
	logApplyErrors(applied)
	if msg, bad := applied.Errors["storage"]; bad {
		return errors.New(msg)
	}
	return nil
}

// handleV2RevokeRecordingServer refuses the server's certificate from now
// on and drops its connection. Its cameras stay assigned to it (and offline)
// until they are moved. Revoking twice is a no-op. With ?remove=true a
// revoked server is taken out of the registry instead (409 while it still
// has cameras, or when it is not revoked yet).
func (s *Server) handleV2RevokeRecordingServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("serverId")
	remove := r.URL.Query().Get("remove") == "true"
	if id == store.LocalRecorderID {
		if remove {
			writeV2Error(w, http.StatusConflict, codeConflict, "the local recording server can't be removed")
			return
		}
		writeV2Error(w, http.StatusConflict, codeConflict, "the local recording server can't be revoked")
		return
	}
	rs, err := s.Store.RecordingServer(r.Context(), id)
	if err != nil {
		writeV2Fail(w, err, "recording server")
		return
	}
	if remove {
		if !rs.Revoked() {
			writeV2Error(w, http.StatusConflict, codeConflict, "revoke the server before removing it")
			return
		}
		if err := s.Store.DeleteRecordingServer(r.Context(), id); err != nil {
			if errors.Is(err, store.ErrHasCameras) {
				writeV2Error(w, http.StatusConflict, codeConflict, "move its cameras to another server first")
				return
			}
			writeV2Fail(w, err, "recording server")
			return
		}
		s.audit(r, "Servers", "Removed recording server", rs.Name+" ("+rs.ID+")")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !rs.Revoked() {
		if err := s.Store.RevokeRecordingServer(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
			writeV2Err(w, err)
			return
		}
		s.audit(r, "Servers", "Revoked recording server", rs.Name+" ("+rs.ID+")")
	}
	if s.Control != nil {
		s.Control.Disconnect(id)
	}
	s.ConnectRecorder(id, nil) // whatever transport it used
	w.WriteHeader(http.StatusNoContent)
}

// handleV2EnrollmentToken makes a one-time token (valid 1 hour, shown
// once) for a new recording server to enroll with over the control link.
func (s *Server) handleV2EnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readOptionalV2JSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len([]rune(in.Name)) > 100 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "name must be at most 100 characters")
		return
	}
	if s.Control == nil {
		writeV2Error(w, http.StatusConflict, codeConflict, "the control link isn't enabled on this server")
		return
	}
	secret, exp, err := s.Store.CreateEnrollmentToken(r.Context(), in.Name, currentUser(r).Name, enrollmentTokenTTL)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	token := s.Control.EnrollmentToken(secret)
	detail := "valid until " + exp.Format("Jan 2 15:04")
	if in.Name != "" {
		detail = in.Name + ", " + detail
	}
	s.audit(r, "Servers", "Created enrollment token", detail)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token, "expiresAt": exp.UnixMilli(), "command": s.Control.EnrollCommand(token),
	})
}

// sandboxHint explains a read-only error: the systemd unit (ProtectSystem=
// strict) only lets Sentinel write under its data folder, /srv, /mnt and /media.
func sandboxHint(dir string, err error) string {
	if !errors.Is(err, syscall.EROFS) {
		return ""
	}
	return " (the Sentinel service can only write under /var/lib/sentinel, /srv, /mnt and /media; use a folder there, or add ReadWritePaths=" + dir + " in a systemd drop-in for sentinel.service)"
}
