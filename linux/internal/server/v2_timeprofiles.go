package server

// Wave 6: time profiles. Named weekly schedules a rule can reference
// (rules.Rule.TimeProfileID); outside the profile the rule doesn't fire.
// Routes need ChangeSettings and are audited under area "Rules".

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

func (s *Server) registerV2TimeProfiles(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/time-profiles", auth.ChangeSettings, s.handleV2TimeProfiles)
	s.v2Route(mux, "POST /api/v2/time-profiles", auth.ChangeSettings, s.handleV2SaveTimeProfile)
	s.v2Route(mux, "PUT /api/v2/time-profiles/{profileId}", auth.ChangeSettings, s.handleV2SaveTimeProfile)
	s.v2Route(mux, "DELETE /api/v2/time-profiles/{profileId}", auth.ChangeSettings, s.handleV2DeleteTimeProfile)
}

type v2TimeProfile struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Weekly    []rules.Span `json:"weekly"`
	CreatedAt int64        `json:"createdAt"`
}

func toV2TimeProfile(p *rules.TimeProfile) v2TimeProfile {
	weekly := p.Weekly
	if weekly == nil {
		weekly = []rules.Span{}
	}
	for i := range weekly {
		if weekly[i].Days == nil {
			weekly[i].Days = []int{}
		}
	}
	return v2TimeProfile{ID: p.ID, Name: p.Name, Weekly: weekly, CreatedAt: secToMs(p.CreatedAt)}
}

func (s *Server) handleV2TimeProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.TimeProfiles(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2TimeProfile, 0, len(list))
	for i := range list {
		out = append(out, toV2TimeProfile(&list[i]))
	}
	v2Items(w, out)
}

func (s *Server) handleV2SaveTimeProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string       `json:"name"`
		Weekly []rules.Span `json:"weekly"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 100 {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "enter a profile name of up to 100 characters")
		return
	}
	p := &rules.TimeProfile{ID: r.PathValue("profileId"), Name: name, Weekly: in.Weekly}
	if err := p.Validate(); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	creating := p.ID == ""
	if !creating {
		old, err := s.Store.TimeProfile(r.Context(), p.ID)
		if err != nil {
			writeV2Fail(w, err, "time profile")
			return
		}
		p.CreatedAt = old.CreatedAt
	}
	switch err := s.Store.SaveTimeProfile(r.Context(), p); {
	case errors.Is(err, store.ErrDuplicateName):
		writeV2Error(w, http.StatusConflict, codeConflict, "a time profile named "+strconv.Quote(name)+" already exists")
		return
	case err != nil:
		writeV2Fail(w, err, "time profile")
		return
	}
	s.invalidateRules() // rules carry their resolved profile in the cache
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
		s.audit(r, "Rules", "Added time profile", name)
	} else {
		s.audit(r, "Rules", "Edited time profile", name)
	}
	writeJSON(w, status, toV2TimeProfile(p))
}

func (s *Server) handleV2DeleteTimeProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("profileId")
	p, err := s.Store.TimeProfile(r.Context(), id)
	if err != nil {
		writeV2Fail(w, err, "time profile")
		return
	}
	used, err := s.Store.RulesUsingTimeProfile(r.Context(), id)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	if len(used) > 0 {
		writeV2Error(w, http.StatusConflict, codeConflict, "this time profile is used by "+plural(len(used), "rule")+" ("+strings.Join(used, ", ")+"); change them first")
		return
	}
	if err := s.Store.DeleteTimeProfile(r.Context(), id); err != nil {
		writeV2Fail(w, err, "time profile")
		return
	}
	s.invalidateRules()
	s.audit(r, "Rules", "Deleted time profile", p.Name)
	w.WriteHeader(http.StatusNoContent)
}

// attachProfiles resolves each rule's TimeProfileID so Matches can apply it.
// A reference to a missing profile is ignored (the rule isn't gated); the
// delete guard makes that unreachable through the API.
func (s *Server) attachProfiles(ctx context.Context, list []rules.Rule) {
	var byID map[string]*rules.TimeProfile
	for i := range list {
		if list[i].TimeProfileID == "" {
			continue
		}
		if byID == nil {
			byID = map[string]*rules.TimeProfile{}
			profiles, err := s.Store.TimeProfiles(ctx)
			if err != nil {
				return
			}
			for j := range profiles {
				byID[profiles[j].ID] = &profiles[j]
			}
		}
		list[i].SetProfile(byID[list[i].TimeProfileID])
	}
}
