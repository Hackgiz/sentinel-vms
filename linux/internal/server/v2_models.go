package server

// Model catalog API (WAVE16-DETECTION.md, stream A):
//
//	GET    /api/v2/models                       ViewLive: catalog + install state
//	DELETE /api/v2/models/{modelId}             ChangeSettings, audited
//	POST   /api/v2/models/{modelId}/download    ChangeSettings, audited
//	DELETE /api/v2/models/{modelId}/download    ChangeSettings, audited (cancel)
//
// The state describes <data>/models on this (management) server. A remote
// recording server loads its detector from its own data directory.

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/models"
	"sentinel-linux/internal/tasks"
)

// modelsState keeps the last download job per model, in memory.
type modelsState struct {
	mu   sync.Mutex
	jobs map[string]*models.Job
	reg  *tasks.Registry // nil = tasks.Default (tests set their own)
}

func (s *Server) registerV2Models(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/models", auth.ViewLive, s.handleV2Models)
	s.v2Route(mux, "DELETE /api/v2/models/{modelId}", auth.ChangeSettings, s.handleV2DeleteModel)
	s.v2Route(mux, "POST /api/v2/models/{modelId}/download", auth.ChangeSettings, s.handleV2DownloadModel)
	s.v2Route(mux, "DELETE /api/v2/models/{modelId}/download", auth.ChangeSettings, s.handleV2CancelModelDownload)
	s.v2Route(mux, "GET /api/v2/detector", auth.ViewLive, s.handleV2Detector)
	s.v2Route(mux, "PUT /api/v2/detector", auth.ChangeSettings, s.handleV2PutDetector)
}

// v2Model is the Model schema.
type v2Model struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Purpose   string          `json:"purpose"`
	File      string          `json:"file"`
	SizeBytes int64           `json:"sizeBytes"`
	License   string          `json:"license"`
	Source    string          `json:"source,omitempty"`
	Note      string          `json:"note,omitempty"`
	Bundled   bool            `json:"bundled"`
	Installed bool            `json:"installed"`
	State     string          `json:"state"`
	Download  *models.JobView `json:"download,omitempty"`
}

func (s *Server) modelsDataDir() string { return s.Store.DataDir() }

// modelJob is the last download job for id (nil = none).
func (s *Server) modelJob(id string) *models.Job {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	return s.models.jobs[id]
}

// modelState is the catalog state with a running download on top.
func (s *Server) modelState(id string) string {
	if s.modelJob(id).Running() {
		return models.StateDownloading
	}
	return models.State(s.modelsDataDir(), id)
}

func (s *Server) modelView(e models.Entry) v2Model {
	v := v2Model{ID: e.ID, Title: e.Title, Purpose: e.Purpose, File: e.File, SizeBytes: e.SizeBytes, License: e.License,
		Source: e.Source, Note: e.Note, Bundled: e.Bundled}
	job := s.modelJob(e.ID)
	v.Download = job.View()
	if job.Running() {
		v.State = models.StateDownloading
	} else {
		v.State = models.State(s.modelsDataDir(), e.ID)
	}
	v.Installed = v.State == models.StateInstalled
	return v
}

func (s *Server) handleV2Models(w http.ResponseWriter, r *http.Request) {
	items := []v2Model{}
	for _, e := range models.Catalog() {
		items = append(items, s.modelView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "dir": models.Dir(s.modelsDataDir())})
}

func (s *Server) handleV2DeleteModel(w http.ResponseWriter, r *http.Request) {
	e, ok := models.Lookup(r.PathValue("modelId"))
	if !ok {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "model not found")
		return
	}
	switch {
	case e.Bundled:
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" ships with Sentinel and cannot be removed")
		return
	case s.modelJob(e.ID).Running():
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" is downloading; cancel the download first")
		return
	case e.Purpose == models.PurposeDetector && models.NormDetector(s.Store.Setting(r.Context(), SettingDetectorModel, "")) == e.ID:
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" is the selected detector; choose another one first")
		return
	}
	was := models.State(s.modelsDataDir(), e.ID)
	if err := models.Remove(s.modelsDataDir(), e.ID); err != nil {
		writeV2Err(w, err)
		return
	}
	if was != models.StateMissing {
		s.audit(r, "Detection", "Removed model", e.Title)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2DownloadModel(w http.ResponseWriter, r *http.Request) {
	e, ok := models.Lookup(r.PathValue("modelId"))
	if !ok {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "model not found")
		return
	}
	who := ""
	if u := currentUser(r); u != nil {
		who = u.Name
	}
	st := &s.models
	st.mu.Lock()
	if j := st.jobs[e.ID]; j.Running() {
		st.mu.Unlock()
		writeJSON(w, http.StatusOK, j.View())
		return
	}
	switch {
	case e.Bundled:
		st.mu.Unlock()
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" ships with Sentinel")
		return
	case !e.Downloadable():
		st.mu.Unlock()
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" is installed by hand: put "+e.File+" in "+models.Dir(s.modelsDataDir()))
		return
	case models.State(s.modelsDataDir(), e.ID) == models.StateInstalled:
		st.mu.Unlock()
		writeV2Error(w, http.StatusConflict, codeConflict, e.Title+" is already installed")
		return
	}
	j, err := models.Download(s.ctxOrBackground(), s.modelsDataDir(), e.ID, st.reg)
	if err != nil {
		st.mu.Unlock()
		writeV2Error(w, http.StatusBadGateway, codeUnavailable, "the download could not start: "+err.Error())
		return
	}
	if st.jobs == nil {
		st.jobs = map[string]*models.Job{}
	}
	st.jobs[e.ID] = j
	st.mu.Unlock()
	s.audit(r, "Detection", "Started model download", e.Title)
	go func() {
		j.Wait()
		switch v := j.View(); v.State {
		case models.JobDone:
			s.auditSystem(who, "Detection", "Downloaded model", e.Title)
		case models.JobFailed:
			s.auditSystem(who, "Detection", "Model download failed", fmt.Sprintf("%s: %s", e.Title, v.Error))
		}
	}()
	writeJSON(w, http.StatusCreated, j.View())
}

func (s *Server) handleV2CancelModelDownload(w http.ResponseWriter, r *http.Request) {
	e, ok := models.Lookup(r.PathValue("modelId"))
	if !ok {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "model not found")
		return
	}
	j := s.modelJob(e.ID)
	if j == nil {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no download for "+e.Title)
		return
	}
	if j.Running() {
		j.Cancel(20 * time.Second)
		s.audit(r, "Detection", "Canceled model download", e.Title)
	}
	writeJSON(w, http.StatusOK, j.View())
}
