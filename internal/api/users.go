package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/users"
)

func (s *server) listUsers(w http.ResponseWriter, r *http.Request, _ *call) {
	list, err := s.Users.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request, _ *call) {
	var in users.CreateInput
	if !decodeBody(w, r, &in) {
		return
	}
	u, err := s.Users.Create(r.Context(), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *server) getUser(w http.ResponseWriter, r *http.Request, _ *call) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	u, err := s.Users.Get(r.Context(), uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request, _ *call) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	var in users.UpdateInput
	if !decodeBody(w, r, &in) {
		return
	}
	u, err := s.Users.Update(r.Context(), uid, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *server) resetPassword(w http.ResponseWriter, r *http.Request, _ *call) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	var in struct {
		NewPassword string `json:"new_password"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if err := s.Users.ResetPassword(r.Context(), uid, in.NewPassword); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
