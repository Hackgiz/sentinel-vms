package server

// API v2: GET /api/v2/system/health, the System Monitor's feed. The
// management server asks every connected recording server for a reading in
// parallel (link.CmdHealth, short timeout). A server that is offline, or
// doesn't answer, still appears (online:false / no metrics) with the
// cameras the store last knew on it. Camera rows follow the caller's scope.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/hwinfo"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

// healthTimeout bounds each recorder's answer; a var so tests can shorten it.
var healthTimeout = 3 * time.Second

func (s *Server) registerV2Health(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/system/health", auth.ViewLive, s.handleV2SystemHealth)
}

type v2HealthMeter struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

type v2HealthDisk struct {
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

type v2HealthCamera struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Online            bool     `json:"online"`
	Recording         bool     `json:"recording"`
	LastSegmentAgeSec *int64   `json:"lastSegmentAgeSec"`
	KBPS              *float64 `json:"kbps"`
}

type v2HealthServer struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Online    bool             `json:"online"`
	LatencyMs *int64           `json:"latencyMs"`
	CPU       *float64         `json:"cpu"` // percent of the cores
	Mem       *v2HealthMeter   `json:"mem"`
	Disk      *v2HealthDisk    `json:"disk"`
	DBBytes   *int64           `json:"dbBytes"`
	UptimeSec *int64           `json:"uptimeSec"`
	Version   string           `json:"version"`
	Cameras   []v2HealthCamera `json:"cameras"`
}

func (s *Server) handleV2SystemHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.Store.RecordingServers(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	byServer := map[string][]store.Camera{}
	for _, c := range cams { // all cameras: a server's presence doesn't depend on scope
		byServer[c.RecorderID] = append(byServer[c.RecorderID], c)
	}
	scope := s.cameraScope(ctx)

	var shown []store.RecordingServer
	for _, rs := range list {
		if rs.Revoked() || (rs.ID == store.LocalRecorderID && !s.recorderConnected(rs.ID) && len(byServer[rs.ID]) == 0) {
			continue
		}
		shown = append(shown, rs)
	}
	out := make([]v2HealthServer, len(shown))
	var wg sync.WaitGroup
	for i := range shown {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = s.serverHealth(ctx, &shown[i], byServer[shown[i].ID], scope)
		}(i)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// serverHealth builds one server's entry.
func (s *Server) serverHealth(ctx context.Context, rs *store.RecordingServer, cams []store.Camera, scope *cameraScope) v2HealthServer {
	st, connected := s.statusOf(rs.ID)
	v := v2HealthServer{ID: rs.ID, Name: rs.Name, Version: rs.Version, Cameras: []v2HealthCamera{}}
	var res link.HealthResult
	answered := false
	if connected {
		cctx, cancel := context.WithTimeout(ctx, healthTimeout)
		t0 := time.Now()
		err := s.command(cctx, rs.ID, link.CmdHealth, nil, &res)
		cancel()
		var ce *link.CommandError
		switch {
		case err == nil:
			answered = true
			ms := time.Since(t0).Milliseconds()
			v.Online, v.LatencyMs = true, &ms
		case errors.As(err, &ce) && ce.Code == link.CodeUnknownCommand:
			v.Online = true // an older server: connected, but no metrics
		}
		if st.Version != "" {
			v.Version = st.Version
		}
	}
	if answered {
		if res.Version != "" {
			v.Version = res.Version
		}
		up, db := res.UptimeSec, res.DBBytes
		v.UptimeSec, v.DBBytes = &up, &db
		var h hwinfo.Health
		if len(res.Host) > 0 && json.Unmarshal(res.Host, &h) == nil {
			cpu := h.CPU
			v.CPU = &cpu
			if h.MemTotal > 0 {
				v.Mem = &v2HealthMeter{Used: h.MemUsed, Total: h.MemTotal}
			}
			if h.DiskTotal > 0 {
				v.Disk = &v2HealthDisk{Used: h.DiskUsed, Free: h.DiskFree, Total: h.DiskTotal}
			}
		}
	}
	reported := map[string]link.HealthCamera{}
	for _, c := range res.Cameras {
		reported[c.ID] = c
	}
	for i := range cams {
		c := &cams[i]
		if !scope.camera(c.ID) {
			continue
		}
		row := v2HealthCamera{ID: c.ID, Name: c.Name}
		if hc, ok := reported[c.ID]; ok {
			row.Online, row.Recording, row.LastSegmentAgeSec, row.KBPS = hc.Online, hc.Recording, hc.LastSegmentAgeSec, hc.KBPS
		} else if connected { // no reading: the recorder's status report
			row.Online = st.Cameras[c.ID].Ready
			row.Recording = row.Online && c.Recording
		}
		v.Cameras = append(v.Cameras, row)
	}
	sort.SliceStable(v.Cameras, func(i, j int) bool { return v.Cameras[i].Name < v.Cameras[j].Name })
	return v
}
