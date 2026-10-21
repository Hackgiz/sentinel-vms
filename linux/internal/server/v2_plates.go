package server

// Plate reading API (WAVE16-DETECTION.md, stream D):
//
//	GET    /api/v2/lpr                ViewLive: switch + model status
//	PUT    /api/v2/lpr                ChangeSettings, audited (409 without models)
//	GET    /api/v2/plates             ViewLive: the watchlist
//	POST   /api/v2/plates             ChangeSettings, audited
//	GET    /api/v2/plates/{plateId}   ViewLive: one entry + its newest sightings
//	PATCH  /api/v2/plates/{plateId}   ChangeSettings, audited
//	DELETE /api/v2/plates/{plateId}   ChangeSettings, audited
//
// Last-seen times and sightings only count cameras in the caller's scope.

import (
	"context"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/lpr"
	"sentinel-linux/internal/store"
)

const (
	plateSightings  = 20
	plateMinLen     = 2
	plateMaxLen     = 10
	plateMaxRawLen  = 12
	plateMaxName    = 80
	plateMaxNotes   = 500
	plateReadWindow = 24 * time.Hour
)

func (s *Server) registerV2Plates(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/lpr", auth.ViewLive, s.handleV2LPRStatus)
	s.v2Route(mux, "PUT /api/v2/lpr", auth.ChangeSettings, s.handleV2PutLPR)
	s.v2Route(mux, "GET /api/v2/plates", auth.ViewLive, s.handleV2Plates)
	s.v2Route(mux, "POST /api/v2/plates", auth.ChangeSettings, s.handleV2AddPlate)
	s.v2Route(mux, "GET /api/v2/plates/{plateId}", auth.ViewLive, s.handleV2Plate)
	s.v2Route(mux, "PATCH /api/v2/plates/{plateId}", auth.ChangeSettings, s.handleV2PatchPlate)
	s.v2Route(mux, "DELETE /api/v2/plates/{plateId}", auth.ChangeSettings, s.handleV2DeletePlate)
}

// MARK: - Status

type v2LPRStatus struct {
	Enabled     bool   `json:"enabled"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
	DetModel    string `json:"detModel,omitempty"`
	RecModel    string `json:"recModel,omitempty"`
	Plates      int    `json:"plates"`
	ReadLast24h int    `json:"readLast24h"`
}

func (s *Server) lprStatus(ctx context.Context) (*v2LPRStatus, error) {
	r, reason := s.plateReader()
	out := &v2LPRStatus{Enabled: s.lprEnabled(ctx), Available: r != nil, Reason: reason}
	if r != nil {
		out.DetModel, out.RecModel = r.DetModel(), r.RecModel()
	}
	var err error
	if out.Plates, err = s.Store.CountPlates(ctx); err != nil {
		return nil, err
	}
	if out.ReadLast24h, err = s.Store.CountPlateReads(ctx, time.Now().Add(-plateReadWindow).Unix()); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) handleV2LPRStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.lprStatus(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleV2PutLPR(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "enabled is required")
		return
	}
	if *in.Enabled {
		if rd, reason := s.plateReader(); rd == nil {
			writeV2Error(w, http.StatusConflict, codeConflict, "plate reading is unavailable: "+reason)
			return
		}
	}
	val, word := "0", "off"
	if *in.Enabled {
		val, word = "1", "on"
	}
	if err := s.Store.SetSetting(r.Context(), SettingLPREnabled, val); err != nil {
		writeV2Err(w, err)
		return
	}
	s.audit(r, "Plates", "Changed plate reading", "Plate reading "+word)
	s.handleV2LPRStatus(w, r)
}

// MARK: - Watchlist

type v2Plate struct {
	ID               string     `json:"id"`
	Plate            string     `json:"plate"`
	Name             string     `json:"name"`
	Notes            string     `json:"notes"`
	Alert            string     `json:"alert"`
	LastSeenAt       int64      `json:"lastSeenAt,omitempty"`
	LastSeenCameraID string     `json:"lastSeenCameraId,omitempty"`
	Sightings        *[]v2Event `json:"sightings,omitempty"` // GET one plate only (then always present)
	CreatedAt        int64      `json:"createdAt"`
}

// plateView builds an entry's view; sightings > 0 also lists that many
// newest sightings. Only the caller's cameras count.
func (s *Server) plateView(ctx context.Context, p *store.Plate, sightings int) (v2Plate, error) {
	v := v2Plate{ID: p.ID, Plate: p.Plate, Name: p.Name, Notes: p.Notes, Alert: p.Alert, CreatedAt: secToMs(p.CreatedAt)}
	ids, none := s.scopedCameraIDs(ctx, nil)
	if none {
		if sightings > 0 {
			v.Sightings = &[]v2Event{}
		}
		return v, nil
	}
	n := max(1, sightings)
	events, err := s.Store.PlateSightings(ctx, p.Plate, ids, n)
	if err != nil {
		return v, err
	}
	if len(events) > 0 {
		v.LastSeenAt, v.LastSeenCameraID = secToMs(events[0].CreatedAt), events[0].CameraID
	}
	if sightings > 0 {
		list := make([]v2Event, 0, len(events))
		for i := range events {
			list = append(list, toV2Event(&events[i]))
		}
		v.Sightings = &list
	}
	return v, nil
}

func (s *Server) handleV2Plates(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Plates(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	items := make([]v2Plate, 0, len(list))
	for i := range list {
		v, err := s.plateView(r.Context(), &list[i], 0)
		if err != nil {
			writeV2Err(w, err)
			return
		}
		items = append(items, v)
	}
	v2Items(w, items)
}

func (s *Server) handleV2Plate(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.PlateByID(r.Context(), r.PathValue("plateId"))
	if err != nil {
		writeV2Fail(w, err, "plate")
		return
	}
	v, err := s.plateView(r.Context(), p, plateSightings)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type v2PlateInput struct {
	Plate *string `json:"plate"`
	Name  *string `json:"name"`
	Notes *string `json:"notes"`
	Alert *string `json:"alert"`
}

// apply validates in and copies it onto p; it returns the 400 message.
func (in *v2PlateInput) apply(p *store.Plate) string {
	if in.Plate != nil {
		if utf8.RuneCountInString(*in.Plate) > plateMaxRawLen {
			return "plate is too long"
		}
		n := lpr.Normalize(*in.Plate)
		if len(n) < plateMinLen || len(n) > plateMaxLen {
			return "plate must be 2 to 10 letters or digits"
		}
		p.Plate = n
	}
	if in.Name != nil {
		if utf8.RuneCountInString(*in.Name) > plateMaxName {
			return "name is too long (80 characters max)"
		}
		p.Name = *in.Name
	}
	if in.Notes != nil {
		if utf8.RuneCountInString(*in.Notes) > plateMaxNotes {
			return "notes are too long (500 characters max)"
		}
		p.Notes = *in.Notes
	}
	if in.Alert != nil {
		if !store.ValidPlateAlert(*in.Alert) {
			return "alert must be default, never or always"
		}
		p.Alert = *in.Alert
	}
	return ""
}

func plateLabel(p *store.Plate) string {
	if p.Name != "" {
		return p.Plate + " (" + p.Name + ")"
	}
	return p.Plate
}

func (s *Server) handleV2AddPlate(w http.ResponseWriter, r *http.Request) {
	var in v2PlateInput
	if !readV2JSON(w, r, &in) {
		return
	}
	if in.Plate == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "plate is required")
		return
	}
	p := &store.Plate{Alert: store.PlateAlertDefault}
	if msg := in.apply(p); msg != "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, msg)
		return
	}
	if err := s.Store.AddPlate(r.Context(), p); err != nil {
		s.writePlateErr(w, err)
		return
	}
	s.audit(r, "Plates", "Added plate", plateLabel(p)+", alert "+p.Alert)
	v, err := s.plateView(r.Context(), p, 0)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleV2PatchPlate(w http.ResponseWriter, r *http.Request) {
	var in v2PlateInput
	if !readV2JSON(w, r, &in) {
		return
	}
	p, err := s.Store.PlateByID(r.Context(), r.PathValue("plateId"))
	if err != nil {
		writeV2Fail(w, err, "plate")
		return
	}
	if msg := in.apply(p); msg != "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, msg)
		return
	}
	if err := s.Store.UpdatePlate(r.Context(), p); err != nil {
		s.writePlateErr(w, err)
		return
	}
	s.audit(r, "Plates", "Changed plate", plateLabel(p)+", alert "+p.Alert)
	v, err := s.plateView(r.Context(), p, 0)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleV2DeletePlate(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.PlateByID(r.Context(), r.PathValue("plateId"))
	if err != nil {
		writeV2Fail(w, err, "plate")
		return
	}
	if err := s.Store.DeletePlate(r.Context(), p.ID); err != nil {
		writeV2Fail(w, err, "plate")
		return
	}
	s.audit(r, "Plates", "Removed plate", plateLabel(p))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writePlateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrDuplicatePlate):
		writeV2Error(w, http.StatusConflict, codeConflict, err.Error())
	default:
		writeV2Fail(w, err, "plate")
	}
}
