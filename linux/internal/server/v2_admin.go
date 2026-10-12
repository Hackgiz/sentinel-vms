package server

// API v2: alarm rules and users.

import (
	"net/http"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/rules"
	"sentinel-linux/internal/store"
)

// MARK: - Rules

// v2RuleInput is RuleInput; v2Rule adds the id and times.
type v2RuleInput struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	CameraIDs    []string `json:"cameraIds"`
	Kinds        []string `json:"kinds"`
	MinSeverity  string   `json:"minSeverity"`
	Schedule     bool     `json:"schedule"`
	FromMinute   int      `json:"fromMinute"`
	ToMinute     int      `json:"toMinute"`
	Days         []int    `json:"days"`
	Delivery     string   `json:"delivery"`
	Instructions string   `json:"instructions"`
}

type v2Rule struct {
	ID string `json:"id"`
	v2RuleInput
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

func (in v2RuleInput) rule() rules.Rule {
	return rules.Rule{
		Name: in.Name, Enabled: in.Enabled, CameraIDs: in.CameraIDs, Kinds: in.Kinds, MinSeverity: in.MinSeverity,
		Schedule: in.Schedule, FromMinute: in.FromMinute, ToMinute: in.ToMinute, Days: in.Days,
		Delivery: in.Delivery, Instructions: in.Instructions,
	}
}

func toV2Rule(r *rules.Rule) v2Rule {
	return v2Rule{
		ID: r.ID,
		v2RuleInput: v2RuleInput{
			Name: r.Name, Enabled: r.Enabled, CameraIDs: r.CameraIDs, Kinds: r.Kinds, MinSeverity: r.MinSeverity,
			Schedule: r.Schedule, FromMinute: r.FromMinute, ToMinute: r.ToMinute, Days: r.Days,
			Delivery: r.Delivery, Instructions: r.Instructions,
		},
		CreatedAt: secToMs(r.CreatedAt), UpdatedAt: secToMs(r.UpdatedAt),
	}
}

func (s *Server) handleV2Rules(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.AlarmRules(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Rule, 0, len(list))
	for i := range list {
		out = append(out, toV2Rule(&list[i]))
	}
	v2Items(w, out)
}

// handleV2SaveRule serves POST /rules (create) and PUT /rules/{ruleId}.
func (s *Server) handleV2SaveRule(w http.ResponseWriter, r *http.Request) {
	var in v2RuleInput
	if !readV2JSON(w, r, &in) {
		return
	}
	rule := in.rule()
	created, err := s.saveRule(r, r.PathValue("ruleId"), &rule)
	if err != nil {
		writeV2Fail(w, err, "rule")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toV2Rule(&rule))
}

func (s *Server) handleV2DeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteRule(r, r.PathValue("ruleId")); err != nil {
		writeV2Fail(w, err, "rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MARK: - Users

func (s *Server) handleV2Users(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.Users(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2User, 0, len(users))
	for i := range users {
		out = append(out, s.v2UserView(r.Context(), &users[i]))
	}
	v2Items(w, out)
}

func (s *Server) handleV2CreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string       `json:"name"`
		Role     string       `json:"role"`
		Password string       `json:"password"`
		Scope    *v2UserScope `json:"scope"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	sc, err := s.parseUserScope(r.Context(), in.Role, in.Scope)
	if err != nil {
		writeV2Fail(w, err, "user")
		return
	}
	u, err := s.createUser(r, in.Name, in.Role, in.Password)
	if err != nil {
		writeV2Fail(w, err, "user")
		return
	}
	if in.Scope != nil && !sc.AllCameras {
		if err := s.setUserScope(r, u, sc); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, s.v2UserView(r.Context(), u))
}

// handleV2PatchUser changes a user's role and/or password, then signs
// them out everywhere (dashboard sessions and API tokens) so the change
// takes effect at once; sending the role they already have changes
// nothing. The last Admin can't be demoted.
func (s *Server) handleV2PatchUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role     *string      `json:"role"`
		Password *string      `json:"password"`
		Scope    *v2UserScope `json:"scope"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	u, err := s.Store.UserByID(r.Context(), r.PathValue("userId"))
	if err != nil {
		writeV2Fail(w, err, "user")
		return
	}
	if in.Role == nil && in.Password == nil && in.Scope == nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "send a role, a password or a scope to change")
		return
	}
	if in.Role != nil && !store.ValidRole(*in.Role) {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "role must be Admin, Supervisor, Operator or Viewer")
		return
	}
	var scope store.UserScope
	if in.Scope != nil {
		role := u.Role
		if in.Role != nil {
			role = *in.Role
		}
		if scope, err = s.parseUserScope(r.Context(), role, in.Scope); err != nil {
			writeV2Fail(w, err, "user")
			return
		}
	}
	var hash string
	if in.Password != nil {
		if s.isDirectoryUser(r.Context(), u.ID) { // v2_directory.go
			writeV2Error(w, http.StatusConflict, codeConflict, u.Name+" signs in through the directory; change the password there")
			return
		}
		if hash, err = auth.HashPassword(*in.Password); err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	changed := hash != ""
	if in.Role != nil && *in.Role != u.Role {
		changed = true
		if u.Role == "Admin" {
			if n, _ := s.Store.AdminCount(r.Context()); n <= 1 {
				writeV2Error(w, http.StatusConflict, codeConflict, "can't change the role of the last administrator")
				return
			}
		}
		if err := s.Store.SetUserRole(r.Context(), u.ID, *in.Role); err != nil {
			writeV2Fail(w, err, "user")
			return
		}
		s.audit(r, "Users", "Changed user role", u.Name+": "+u.Role+" → "+*in.Role)
		u.Role = *in.Role
	}
	if hash != "" {
		if err := s.Store.SetUserPassword(r.Context(), u.ID, hash); err != nil {
			writeV2Fail(w, err, "user")
			return
		}
		s.audit(r, "Users", "Changed user password", u.Name)
	}
	if in.Scope != nil {
		// Takes effect on the user's next request; no need to sign them out.
		if err := s.setUserScope(r, u, scope); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	if changed {
		if err := s.signOutEverywhere(r, u.ID); err != nil {
			writeV2Err(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.v2UserView(r.Context(), u))
}

// signOutEverywhere ends a user's dashboard sessions and API sign-ins.
func (s *Server) signOutEverywhere(r *http.Request, userID string) error {
	if err := s.Store.DeleteUserSessions(r.Context(), userID); err != nil {
		return err
	}
	return s.Store.DeleteUserTokens(r.Context(), userID)
}

func (s *Server) handleV2DeleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteUser(r, r.PathValue("userId")); err != nil {
		writeV2Fail(w, err, "user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
