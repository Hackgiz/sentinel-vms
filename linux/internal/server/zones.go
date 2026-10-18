package server

// API v2: detection zones and privacy masks (WAVE7-FEATURES.md, Stream A).
// GET/PUT /api/v2/cameras/{cameraId}/zones; PUT replaces the camera's whole
// set atomically (ManageCameras, audited) and pushes the new configuration
// to the recording servers, which gate detection and burn masks. Enabled
// mask polygons also ride on the v2 camera payload (`masks`).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/media"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2Zones(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/cameras/{cameraId}/zones", auth.ViewLive, s.inScope("cameraId", s.handleV2Zones))
	s.v2Route(mux, "PUT /api/v2/cameras/{cameraId}/zones", auth.ManageCameras, s.inScope("cameraId", s.handleV2SetZones))
}

type v2Zone struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind"`
	Name      string       `json:"name"`
	Points    [][2]float64 `json:"points"`
	Meta      any          `json:"meta,omitempty"` // lines: {"direction":"ltr"|"rtl"|"both"}
	Enabled   bool         `json:"enabled"`
	CreatedAt int64        `json:"createdAt"`
	UpdatedAt int64        `json:"updatedAt"`
}

type v2ZoneInput struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	Points  [][2]float64    `json:"points"`
	Meta    json.RawMessage `json:"meta"`    // lines only; omitted = {"direction":"both"}
	Enabled *bool           `json:"enabled"` // omitted = enabled
}

func toV2Zones(zs []store.Zone) []v2Zone {
	out := make([]v2Zone, 0, len(zs))
	for _, z := range zs {
		out = append(out, v2Zone{ID: z.ID, Kind: z.Kind, Name: z.Name, Points: z.Points, Meta: zoneMetaView(z), Enabled: z.Enabled,
			CreatedAt: secToMs(z.CreatedAt), UpdatedAt: secToMs(z.UpdatedAt)})
	}
	return out
}

func (s *Server) handleV2Zones(w http.ResponseWriter, r *http.Request) {
	id, ok := s.v2CameraExists(w, r)
	if !ok {
		return
	}
	zs, err := s.Store.CameraZones(r.Context(), id)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2Items(w, toV2Zones(zs))
}

func (s *Server) handleV2SetZones(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Zones []v2ZoneInput `json:"zones"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	cam, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	set := make([]store.ZoneInput, 0, len(in.Zones))
	for _, z := range in.Zones {
		set = append(set, store.ZoneInput{ID: z.ID, Kind: z.Kind, Name: z.Name, Points: z.Points, Meta: string(z.Meta), Enabled: z.Enabled == nil || *z.Enabled})
	}
	if err := store.ValidateZones(set); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	zs, err := s.Store.SetCameraZones(r.Context(), cam.ID, set)
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	masks, detects, lines := 0, 0, 0
	for _, z := range zs {
		if !z.Enabled {
			continue
		}
		switch z.Kind {
		case store.ZoneKindMask:
			masks++
		case store.ZoneKindLine:
			lines++
		default:
			detects++
		}
	}
	s.audit(r, "Cameras", "Set camera zones", cam.Name+": "+strconv.Itoa(detects)+" detection, "+strconv.Itoa(masks)+" privacy mask, "+strconv.Itoa(lines)+" line (enabled), "+strconv.Itoa(len(zs))+" total")
	// Recording servers pick the zones up with the next configuration; the
	// detector reader restarts with the new rasterization.
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	v2Items(w, toV2Zones(zs))
}

// fillCameraMasks sets masks (enabled mask polygons) on camera views.
func (s *Server) fillCameraMasks(ctx context.Context, cams []v2Camera) {
	all, err := s.Store.AllCameraZones(ctx)
	if err != nil {
		return
	}
	for i := range cams {
		for _, z := range all[cams[i].ID] {
			if z.Enabled && z.Kind == store.ZoneKindMask {
				cams[i].Masks = append(cams[i].Masks, z.Points)
			}
		}
	}
}

// MaskCameraJPEG burns the camera's enabled privacy masks into a JPEG (the
// management server's use of the media.MaskJPEG choke point: MQTT snapshots;
// HomeKit snapshots do the same in homekit). ok=false means the image could
// not be masked and must be dropped.
func (s *Server) MaskCameraJPEG(ctx context.Context, cameraID string, jpg []byte) ([]byte, bool) {
	return MaskJPEGFor(ctx, s.Store, cameraID, jpg)
}

// MaskJPEGFor is MaskCameraJPEG for any store handle.
func MaskJPEGFor(ctx context.Context, st *store.Store, cameraID string, jpg []byte) ([]byte, bool) {
	zs, err := st.CameraZones(ctx, cameraID)
	if err != nil {
		return nil, false
	}
	rects := media.MaskRects(store.LinkZones(zs))
	if len(rects) == 0 {
		return jpg, true
	}
	out, err := media.MaskJPEG(jpg, rects)
	if err != nil {
		slog.Warn("dropping a snapshot that could not be masked", "camera", cameraID, "err", err)
		return nil, false
	}
	return out, true
}
