package server

// Faces and people API (WAVE16-DETECTION.md, stream C; openapi tag faces).
// Routes are registered in v2_resources.go under "wave16-faces":
//
//	GET    /api/v2/faces                    ViewLive: settings + status
//	PUT    /api/v2/faces                    ChangeSettings, audited
//	GET    /api/v2/faces/unassigned         ChangeSettings
//	DELETE /api/v2/faces/{faceId}           ChangeSettings, audited
//	GET    /api/v2/faces/{faceId}/crop.jpg  ViewLive (camera scope via the face's event)
//	GET    /api/v2/people                   ViewLive
//	POST   /api/v2/people                   ChangeSettings, audited
//	GET    /api/v2/people/{personId}        ViewLive
//	PATCH  /api/v2/people/{personId}        ChangeSettings, audited
//	DELETE /api/v2/people/{personId}        ChangeSettings, audited
//	POST   /api/v2/people/{personId}/faces  ChangeSettings, audited
//	GET    /api/v2/events/{eventId}/faces   ViewLive
//
// Faces are shown only for cameras in the caller's scope.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"sentinel-linux/internal/store"
)

const facesDefaultLimit = 100

type v2FacesStatus struct {
	Enabled        bool   `json:"enabled"`
	Available      bool   `json:"available"`
	Reason         string `json:"reason,omitempty"`
	DetectModel    string `json:"detectModel,omitempty"`
	EmbedModel     string `json:"embedModel,omitempty"`
	People         int    `json:"people"`
	Unassigned     int    `json:"unassigned"`
	UnassignedDays int    `json:"unassignedDays"`
}

type v2Face struct {
	ID         string    `json:"id"`
	EventID    string    `json:"eventId"`
	CameraID   string    `json:"cameraId"`
	At         int64     `json:"at"`
	Box        []float64 `json:"box,omitempty"`
	Score      float64   `json:"score"`
	PersonID   string    `json:"personId,omitempty"`
	Similarity *float64  `json:"similarity,omitempty"`
}

type v2Person struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Notes            string   `json:"notes,omitempty"`
	Alert            string   `json:"alert"`
	Faces            []v2Face `json:"faces"`
	LastSeenAt       int64    `json:"lastSeenAt,omitempty"`
	LastSeenCameraID string   `json:"lastSeenCameraId,omitempty"`
	CreatedAt        int64    `json:"createdAt"`
}

func toV2Face(f *store.Face) v2Face {
	v := v2Face{ID: f.ID, EventID: f.EventID, CameraID: f.CameraID, At: f.AtMs, Score: f.Score, PersonID: f.PersonID}
	var box []float64
	if json.Unmarshal([]byte(f.Box), &box) == nil && len(box) == 4 {
		v.Box = box
	}
	if f.PersonID != "" && f.Similarity > 0 {
		sim := f.Similarity
		v.Similarity = &sim
	}
	return v
}

// scopedFaces converts the faces the caller may see.
func (s *Server) scopedFaces(ctx context.Context, list []store.Face) []v2Face {
	sc := s.cameraScope(ctx)
	out := []v2Face{}
	for i := range list {
		if sc.camera(list[i].CameraID) {
			out = append(out, toV2Face(&list[i]))
		}
	}
	return out
}

// v2PersonView is a person with the faces the caller may see.
func (s *Server) v2PersonView(ctx context.Context, p *store.Person) (v2Person, error) {
	list, err := s.Store.PersonFaces(ctx, p.ID)
	if err != nil {
		return v2Person{}, err
	}
	v := v2Person{ID: p.ID, Name: p.Name, Notes: p.Notes, Alert: p.Alert, Faces: s.scopedFaces(ctx, list), CreatedAt: p.CreatedAt}
	if p.LastSeenAt > 0 && s.cameraScope(ctx).camera(p.LastSeenCameraID) {
		v.LastSeenAt, v.LastSeenCameraID = p.LastSeenAt, p.LastSeenCameraID
	}
	return v, nil
}

func (s *Server) facesStatus(ctx context.Context) (v2FacesStatus, error) {
	st := v2FacesStatus{Enabled: s.facesEnabled(ctx), UnassignedDays: s.facesUnassignedDays(ctx)}
	eng, reason := s.faceEngine()
	if eng == nil {
		st.Reason = reason
	} else {
		st.Available, st.DetectModel, st.EmbedModel = true, eng.DetectModel(), eng.EmbedModel()
	}
	var err error
	if st.People, err = s.Store.CountPeople(ctx); err != nil {
		return st, err
	}
	st.Unassigned, err = s.Store.CountUnassignedFaces(ctx)
	return st, err
}

func (s *Server) handleV2FacesStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.facesStatus(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleV2PutFaces(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled        *bool `json:"enabled"`
		UnassignedDays *int  `json:"unassignedDays"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.UnassignedDays != nil && (*in.UnassignedDays < 1 || *in.UnassignedDays > facesMaxDays) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "unassignedDays must be a number from 1 to 90")
		return
	}
	if in.Enabled != nil && *in.Enabled {
		if eng, reason := s.faceEngine(); eng == nil {
			writeV2Error(w, http.StatusConflict, codeConflict, "face recognition is unavailable: "+reason)
			return
		}
	}
	var changes []string
	if in.Enabled != nil {
		v := "0"
		changes = append(changes, "Face recognition off")
		if *in.Enabled {
			v = "1"
			changes[0] = "Face recognition on"
		}
		if err := s.Store.SetSetting(ctx, SettingFacesEnabled, v); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	if in.UnassignedDays != nil {
		if err := s.Store.SetSetting(ctx, SettingFacesUnassignedDays, strconv.Itoa(*in.UnassignedDays)); err != nil {
			writeV2Err(w, err)
			return
		}
		changes = append(changes, fmt.Sprintf("unassigned faces kept %d days", *in.UnassignedDays))
	}
	if len(changes) > 0 {
		s.audit(r, "Faces", "Changed face recognition", strings.Join(changes, ", "))
	}
	s.handleV2FacesStatus(w, r)
}

// facesLimit reads ?limit= (1…500, default 100).
func facesLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return facesDefaultLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 500 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "limit must be a number from 1 to 500")
		return 0, false
	}
	return n, true
}

func (s *Server) handleV2UnassignedFaces(w http.ResponseWriter, r *http.Request) {
	limit, ok := facesLimit(w, r)
	if !ok {
		return
	}
	list, err := s.Store.UnassignedFaces(r.Context(), limit)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2Items(w, s.scopedFaces(r.Context(), list))
}

// scopedFace reads a face whose camera the caller may see.
func (s *Server) scopedFace(ctx context.Context, id string) (*store.Face, error) {
	f, err := s.Store.Face(ctx, id)
	if err == nil && !s.cameraScope(ctx).camera(f.CameraID) {
		return nil, store.ErrNotFound
	}
	return f, err
}

func (s *Server) handleV2DeleteFace(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := s.scopedFace(ctx, r.PathValue("faceId"))
	if err != nil {
		writeV2Fail(w, err, "face")
		return
	}
	if err := s.Store.DeleteFace(ctx, f.ID); err != nil {
		writeV2Fail(w, err, "face")
		return
	}
	detail := "unassigned face from " + s.appearanceCamLabel(r, f.CameraID)
	if f.PersonID != "" {
		if p, err := s.Store.Person(ctx, f.PersonID); err == nil {
			detail = "a face of " + p.Name
		}
	}
	s.audit(r, "Faces", "Deleted face", detail)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2FaceCrop(w http.ResponseWriter, r *http.Request) {
	crop, cam, err := s.Store.FaceCrop(r.Context(), r.PathValue("faceId"))
	if err == nil && (len(crop) == 0 || !s.cameraScope(r.Context()).camera(cam)) {
		err = store.ErrNotFound
	}
	if err != nil {
		writeV2Fail(w, err, "face")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(crop)))
	_, _ = w.Write(crop)
}

func (s *Server) handleV2People(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	people, err := s.Store.People(ctx)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Person, 0, len(people))
	for i := range people {
		v, err := s.v2PersonView(ctx, &people[i])
		if err != nil {
			writeV2Err(w, err)
			return
		}
		out = append(out, v)
	}
	v2Items(w, out)
}

func (s *Server) handleV2Person(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Person(r.Context(), r.PathValue("personId"))
	if err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	s.writeV2Person(w, r, http.StatusOK, p)
}

func (s *Server) writeV2Person(w http.ResponseWriter, r *http.Request, status int, p *store.Person) {
	v, err := s.v2PersonView(r.Context(), p)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, status, v)
}

// personInput is PersonInput (plus faceId on create).
type personInput struct {
	Name   *string `json:"name"`
	Notes  *string `json:"notes"`
	Alert  *string `json:"alert"`
	FaceID string  `json:"faceId"`
}

// apply validates in and copies it onto p.
func (in *personInput) apply(p *store.Person) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" || utf8.RuneCountInString(name) > 80 {
			return inputError{"name must be 1 to 80 characters"}
		}
		p.Name = name
	}
	if in.Notes != nil {
		if utf8.RuneCountInString(*in.Notes) > 500 {
			return inputError{"notes must be at most 500 characters"}
		}
		p.Notes = *in.Notes
	}
	if in.Alert != nil {
		if !store.ValidPersonAlert(*in.Alert) {
			return inputError{"alert must be default, never or always"}
		}
		p.Alert = *in.Alert
	}
	return nil
}

func (s *Server) handleV2CreatePerson(w http.ResponseWriter, r *http.Request) {
	var in personInput
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.Name == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "name is required")
		return
	}
	p := &store.Person{Alert: store.PersonAlertDefault}
	if err := in.apply(p); err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	var face *store.Face
	if in.FaceID != "" {
		f, err := s.scopedFace(ctx, in.FaceID)
		if err != nil {
			writeV2Fail(w, err, "face")
			return
		}
		if f.PersonID != "" {
			writeV2Error(w, http.StatusConflict, codeConflict, store.ErrFaceAssigned.Error())
			return
		}
		face = f
	}
	if err := s.Store.AddPerson(ctx, p); err != nil {
		writeV2Err(w, err)
		return
	}
	detail := p.Name
	if face != nil {
		if err := s.enrollFace(ctx, p, face.ID); err != nil {
			writeV2Fail(w, err, "face")
			return
		}
		detail += ", with a face from " + s.appearanceCamLabel(r, face.CameraID)
	}
	s.audit(r, "Faces", "Added person", detail)
	if fresh, err := s.Store.Person(ctx, p.ID); err == nil {
		p = fresh // with last seen
	}
	s.writeV2Person(w, r, http.StatusCreated, p)
}

// enrollFace assigns an unassigned face to p and names p on its event.
func (s *Server) enrollFace(ctx context.Context, p *store.Person, faceID string) error {
	if err := s.Store.AssignFace(ctx, faceID, p.ID); err != nil {
		if errors.Is(err, store.ErrFaceAssigned) {
			return conflictError{err.Error()}
		}
		return err
	}
	f, err := s.Store.Face(ctx, faceID)
	if err != nil {
		return err
	}
	if err := s.Store.SetEventPerson(ctx, f.EventID, p.ID, p.Name); err != nil {
		return err
	}
	s.publishEventUpdated(f.EventID)
	return nil
}

func (s *Server) handleV2PatchPerson(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  *string `json:"name"`
		Notes *string `json:"notes"`
		Alert *string `json:"alert"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	p, err := s.Store.Person(ctx, r.PathValue("personId"))
	if err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	oldName, oldAlert := p.Name, p.Alert
	pin := personInput{Name: in.Name, Notes: in.Notes, Alert: in.Alert}
	if err := pin.apply(p); err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	if err := s.Store.UpdatePerson(ctx, p); err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	var changes []string
	if p.Name != oldName {
		if _, err := s.Store.RenamePersonEvents(ctx, p.ID, p.Name); err != nil {
			writeV2Err(w, err)
			return
		}
		changes = append(changes, "renamed from "+oldName)
	}
	if p.Alert != oldAlert {
		changes = append(changes, "alert "+p.Alert)
	}
	if in.Notes != nil {
		changes = append(changes, "notes")
	}
	detail := p.Name
	if len(changes) > 0 {
		detail += ": " + strings.Join(changes, ", ")
	}
	s.audit(r, "Faces", "Changed person", detail)
	s.writeV2Person(w, r, http.StatusOK, p)
}

func (s *Server) handleV2DeletePerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.Person(ctx, r.PathValue("personId"))
	if err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	list, _ := s.Store.PersonFaces(ctx, p.ID)
	if err := s.Store.DeletePerson(ctx, p.ID); err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	s.audit(r, "Faces", "Deleted person", fmt.Sprintf("%s and %d faces", p.Name, len(list)))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleV2EnrollFace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FaceID string `json:"faceId"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.FaceID == "" {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "faceId is required")
		return
	}
	p, err := s.Store.Person(ctx, r.PathValue("personId"))
	if err != nil {
		writeV2Fail(w, err, "person")
		return
	}
	f, err := s.scopedFace(ctx, in.FaceID)
	if err != nil {
		writeV2Fail(w, err, "face")
		return
	}
	if err := s.enrollFace(ctx, p, f.ID); err != nil {
		writeV2Fail(w, err, "face")
		return
	}
	s.audit(r, "Faces", "Enrolled face", p.Name+", from "+s.appearanceCamLabel(r, f.CameraID))
	if fresh, err := s.Store.Person(ctx, p.ID); err == nil {
		p = fresh // with last seen
	}
	s.writeV2Person(w, r, http.StatusOK, p)
}

func (s *Server) handleV2EventFaces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.scopedEvent(ctx, r.PathValue("eventId"))
	if err != nil {
		writeV2Fail(w, err, "event")
		return
	}
	list, err := s.Store.EventFaces(ctx, e.ID)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	v2Items(w, s.scopedFaces(ctx, list))
}
