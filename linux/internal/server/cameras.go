package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

var (
	cameraIDPattern  = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	segmentPattern   = regexp.MustCompile(`^(\d{8}-\d{6})-\d+\.mp4$`)
	hlsFilePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(m3u8|ts|mp4|m4s)$`)
	hlsClient        = &http.Client{Timeout: 15 * time.Second}
	defaultRetention = "7"
)

func (s *Server) recordingsRoot() string { return filepath.Join(s.Store.DataDir(), "recordings") }

// ApplyMedia regenerates the MediaMTX config from the camera list. Called at
// startup and after any camera or retention change.
func (s *Server) ApplyMedia(ctx context.Context) error {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	if days, err := strconv.Atoi(s.Store.Setting(ctx, "default_retention_days", defaultRetention)); err == nil {
		s.Media.DefaultRetain = days
	}
	sources := make([]media.Source, 0, len(cams))
	for i := range cams {
		c := &cams[i]
		pass, err := s.Store.CameraPassword(c)
		if err != nil {
			slog.Error("camera password unreadable; skipping", "camera", c.Name, "err", err)
			continue
		}
		main, err := media.WithCredentials(c.RTSPURL, c.Username, pass)
		if err != nil {
			slog.Error("bad camera URL; skipping", "camera", c.Name, "err", err)
			continue
		}
		src := media.Source{ID: c.ID, URL: main, Record: c.Recording, RetentionDays: c.RetentionDays}
		if c.SubRTSPURL != "" {
			if sub, err := media.WithCredentials(c.SubRTSPURL, c.Username, pass); err == nil {
				src.SubURL = sub
			}
		}
		sources = append(sources, src)
	}
	if err := s.Media.Apply(sources); err != nil {
		return err
	}
	if s.Detect.Available() && s.Ctx != nil {
		s.Detect.Sync(s.Ctx, s.detectionSources(cams)) // after Apply: LivePath depends on it
	}
	return nil
}

type cameraView struct {
	store.Camera
	Online    bool   `json:"online"`
	Viewers   int    `json:"viewers"`
	LiveURL   string `json:"liveURL"`
	StatusMsg string `json:"status"`
}

func (s *Server) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	running, _ := s.Media.Status()
	out := make([]cameraView, 0, len(cams))
	for _, c := range cams {
		st := s.Media.State(c.ID)
		v := cameraView{Camera: c, Online: st.Ready, Viewers: st.Readers, LiveURL: "/live/" + c.ID + "/index.m3u8"}
		switch {
		case !running:
			v.StatusMsg = "Media engine starting"
		case st.Ready:
			v.StatusMsg = "Online"
		default:
			v.StatusMsg = "No signal"
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// normalizeCamera validates input and moves any credentials typed into the URL
// into the username/password fields, so the stored URL never holds a secret.
func normalizeCamera(in *store.CameraInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Location = strings.TrimSpace(in.Location)
	if in.Name == "" {
		return errors.New("enter a camera name")
	}
	clean := func(raw string) (string, error) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Host == "" {
			return "", errors.New("stream URL must look like rtsp://camera-address:554/path")
		}
		if u.User != nil {
			if in.Username == "" {
				in.Username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok && in.Password == nil {
				in.Password = &p
			}
			u.User = nil
		}
		return u.String(), nil
	}
	var err error
	if in.RTSPURL, err = clean(in.RTSPURL); err != nil {
		return err
	}
	if strings.TrimSpace(in.SubRTSPURL) != "" {
		if in.SubRTSPURL, err = clean(in.SubRTSPURL); err != nil {
			return errors.New("sub-stream: " + err.Error())
		}
	} else {
		in.SubRTSPURL = ""
	}
	if in.AlertOn != nil && (*in.AlertOn < 0 || *in.AlertOn > 3) {
		return errors.New("alert setting must be auto, any motion, people & vehicles or people only")
	}
	if in.MotionLevel != nil && (*in.MotionLevel < 0 || *in.MotionLevel > 3) {
		return errors.New("motion sensitivity must be off, low, medium or high")
	}
	if in.RetentionDays < 0 || in.RetentionDays > 3650 {
		return errors.New("retention must be between 0 and 3650 days")
	}
	return nil
}

func (s *Server) handleSaveCamera(w http.ResponseWriter, r *http.Request) {
	var in store.CameraInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := normalizeCamera(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	cam, err := s.Store.SaveCamera(r.Context(), id, in)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id == "" {
		s.audit(r, "Cameras", "Added camera", cam.Name)
	} else {
		s.audit(r, "Cameras", "Edited camera", cam.Name)
	}
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, cam)
}

func (s *Server) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cam, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	if err := s.Store.DeleteCamera(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Recordings are kept on disk (deleting a camera must not destroy footage);
	// retention no longer applies to them since MediaMTX drops the path.
	s.audit(r, "Cameras", "Deleted camera", cam.Name+" (recordings kept)")
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// MARK: - Live video proxy

func (s *Server) handleLiveProxy(w http.ResponseWriter, r *http.Request) {
	id, file := r.PathValue("id"), r.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !hlsFilePattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad stream path")
		return
	}
	if _, err := s.Store.Camera(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	upstream := s.Media.HLSUpstream(id, file)
	// MediaMTX >= 1.21 ties HLS requests together with a ?session= parameter;
	// dropping the query gets "session not found".
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	resp, err := hlsClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "stream not available")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasSuffix(file, ".m3u8") {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// MARK: - Recordings

type segment struct {
	Name    string `json:"name"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
	Writing bool   `json:"writing"`
}

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	out, err := s.listSegments(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listSegments returns a camera's recorded segments, newest first.
func (s *Server) listSegments(id string) ([]segment, error) {
	entries, err := os.ReadDir(filepath.Join(s.recordingsRoot(), id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	out := []segment{}
	for _, e := range entries {
		m := segmentPattern.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 {
			continue
		}
		start, err := time.ParseInLocation("20060102-150405", m[1], time.Local)
		if err != nil {
			continue
		}
		out = append(out, segment{
			Name:    e.Name(),
			Start:   start.Unix(),
			End:     info.ModTime().Unix(),
			Size:    info.Size(),
			URL:     "/recordings/" + id + "/" + e.Name(),
			Writing: time.Since(info.ModTime()) < 30*time.Second,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out, nil
}

func (s *Server) handleRecordingFile(w http.ResponseWriter, r *http.Request) {
	s.serveRecording(w, r, r.PathValue("id"), r.PathValue("file"))
}

// serveRecording streams one segment with Range support so players can seek.
func (s *Server) serveRecording(w http.ResponseWriter, r *http.Request, id, file string) {
	if !cameraIDPattern.MatchString(id) || !segmentPattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad recording path")
		return
	}
	f, err := os.Open(filepath.Join(s.recordingsRoot(), id, file))
	if err != nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, file, info.ModTime(), f) // Range requests → seeking works
}

// MARK: - Settings & system

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(s.Store.Setting(r.Context(), "default_retention_days", defaultRetention))
	writeJSON(w, http.StatusOK, map[string]any{"defaultRetentionDays": days})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DefaultRetentionDays int `json:"defaultRetentionDays"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.DefaultRetentionDays < 1 || in.DefaultRetentionDays > 3650 {
		writeError(w, http.StatusBadRequest, "retention must be between 1 and 3650 days")
		return
	}
	old := s.Store.Setting(r.Context(), "default_retention_days", defaultRetention)
	if err := s.Store.SetSetting(r.Context(), "default_retention_days", strconv.Itoa(in.DefaultRetentionDays)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "Storage", "Changed default retention", old+" → "+strconv.Itoa(in.DefaultRetentionDays)+" days")
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	running, lastErr := s.Media.Status()
	resp := map[string]any{
		"version":       s.Version,
		"os":            runtime.GOOS + "/" + runtime.GOARCH,
		"mediaRunning":  running,
		"mediaError":    lastErr,
		"recordingsDir": s.recordingsRoot(),
	}
	var st unix.Statfs_t
	if err := unix.Statfs(s.Store.DataDir(), &st); err == nil {
		resp["diskFreeBytes"] = uint64(st.Bavail) * uint64(st.Bsize)
		resp["diskTotalBytes"] = uint64(st.Blocks) * uint64(st.Bsize)
	}
	var used int64
	_ = filepath.WalkDir(s.recordingsRoot(), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				used += info.Size()
			}
		}
		return nil
	})
	resp["recordingsBytes"] = used
	writeJSON(w, http.StatusOK, resp)
}
