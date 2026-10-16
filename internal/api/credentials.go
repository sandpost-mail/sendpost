package api

import "net/http"

func (s *server) listCredentials(w http.ResponseWriter, r *http.Request, pid int64) {
	list, err := s.Credentials.List(r.Context(), pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// createCredential is the only response that ever carries a secret, and the
// only time it can be read.
func (s *server) createCredential(w http.ResponseWriter, r *http.Request, pid int64) {
	var in struct {
		Label string `json:"label"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	issued, err := s.Credentials.Create(r.Context(), pid, in.Label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, issued)
}

func (s *server) revokeCredential(w http.ResponseWriter, r *http.Request, pid int64) {
	cid, ok := pathID(w, r, "cid")
	if !ok {
		return
	}
	if err := s.Credentials.Revoke(r.Context(), pid, cid); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
