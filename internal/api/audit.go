package api

import (
	"net/http"
	"strconv"
)

// auditParams reads the cursor and limit query parameters.
func auditParams(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return "", 0, false
		}
		limit = n
	}
	return r.URL.Query().Get("cursor"), limit, true
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request, _ *call) {
	cursor, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.Audit.ListAll(r.Context(), cursor, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) listProjectAudit(w http.ResponseWriter, r *http.Request, c *call) {
	cursor, limit, ok := auditParams(w, r)
	if !ok {
		return
	}
	page, err := s.Audit.ListProject(r.Context(), c.scope, cursor, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
