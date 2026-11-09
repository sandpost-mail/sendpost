package api

import (
	"net/http"

	"github.com/sandpost-mail/sandpost/internal/comments"
)

// listComments serves GET .../messages/{mid}/comments, oldest first.
func (s *server) listComments(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	cursor, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.Comments.List(r.Context(), c.scope, mid, cursor, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// createComment serves POST .../messages/{mid}/comments. The author is the
// authenticated caller, never something from the body.
func (s *server) createComment(w http.ResponseWriter, r *http.Request, c *call) {
	mid, ok := pathID(w, r, "mid")
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	cm, err := s.Comments.Create(r.Context(), c.scope, mid, comments.Author{UserID: c.p.UserID, TokenID: c.p.TokenID}, in.Body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, cm)
}

// listActivity serves GET .../activity for everyone who can read messages.
func (s *server) listActivity(w http.ResponseWriter, r *http.Request, c *call) {
	cursor, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.Activity.List(r.Context(), c.scope, cursor, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
