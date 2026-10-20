package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/authz"
)

type tokenInput struct {
	Label  string   `json:"label"`
	Scopes []string `json:"scopes"`
}

// withinCeiling rejects a token request that asks for more than the calling
// principal itself holds, so a read token can not mint a write token.
func (s *server) withinCeiling(w http.ResponseWriter, r *http.Request, p authz.Principal, scopes []string) bool {
	want, ok := authz.CeilingOf(scopes)
	if ok && want > p.Ceiling {
		s.fail(w, r, authz.ErrForbidden)
		return false
	}
	return true
}

func (s *server) listMyTokens(w http.ResponseWriter, r *http.Request, c *call) {
	list, err := s.Tokens.ListForUser(r.Context(), c.p.UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// createMyToken is the only response that ever carries the token secret.
func (s *server) createMyToken(w http.ResponseWriter, r *http.Request, c *call) {
	var in tokenInput
	if !decodeBody(w, r, &in) || !s.withinCeiling(w, r, c.p, in.Scopes) {
		return
	}
	issued, err := s.Tokens.CreateForUser(r.Context(), c.p.UserID, in.Label, in.Scopes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, issued)
}

func (s *server) revokeMyToken(w http.ResponseWriter, r *http.Request, c *call) {
	tid, ok := pathID(w, r, "tid")
	if !ok {
		return
	}
	if err := s.Tokens.RevokeForUser(r.Context(), c.p.UserID, tid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listUserTokens(w http.ResponseWriter, r *http.Request, _ *call) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	list, err := s.Tokens.ListForUser(r.Context(), uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) revokeUserToken(w http.ResponseWriter, r *http.Request, _ *call) {
	uid, ok := pathID(w, r, "uid")
	if !ok {
		return
	}
	tid, ok := pathID(w, r, "tid")
	if !ok {
		return
	}
	if err := s.Tokens.RevokeForUser(r.Context(), uid, tid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listProjectTokens(w http.ResponseWriter, r *http.Request, c *call) {
	list, err := s.Tokens.ListForProject(r.Context(), c.scope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *server) createProjectToken(w http.ResponseWriter, r *http.Request, c *call) {
	var in tokenInput
	if !decodeBody(w, r, &in) || !s.withinCeiling(w, r, c.p, in.Scopes) {
		return
	}
	issued, err := s.Tokens.CreateForProject(r.Context(), c.scope, in.Label, in.Scopes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, issued)
}

func (s *server) revokeProjectToken(w http.ResponseWriter, r *http.Request, c *call) {
	tid, ok := pathID(w, r, "tid")
	if !ok {
		return
	}
	if err := s.Tokens.RevokeForProject(r.Context(), c.scope, tid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
